// Package store persists settings, sessions, batches and uploads in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

var migrations = []string{
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE sessions (
		id TEXT PRIMARY KEY, user TEXT NOT NULL,
		created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
	CREATE TABLE batches (
		id TEXT PRIMARY KEY, user TEXT NOT NULL, kind TEXT NOT NULL,
		provider TEXT NOT NULL DEFAULT '', provider_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '', year INTEGER NOT NULL DEFAULT 0,
		language TEXT NOT NULL DEFAULT '', library_dir TEXT NOT NULL DEFAULT '',
		folder TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
	CREATE INDEX batches_user ON batches(user, created_at);
	CREATE TABLE items (
		id TEXT PRIMARY KEY, batch_id TEXT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
		idx INTEGER NOT NULL, client_key TEXT NOT NULL, role TEXT NOT NULL,
		original_name TEXT NOT NULL, ext TEXT NOT NULL, size INTEGER NOT NULL,
		target TEXT NOT NULL, alt_prefix TEXT NOT NULL DEFAULT '', alt_suffix TEXT NOT NULL DEFAULT '',
		policy TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
		upload_id TEXT NOT NULL DEFAULT '', final_path TEXT NOT NULL DEFAULT '',
		trashed_path TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
		perm_warning TEXT NOT NULL DEFAULT '', published_at INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX items_batch ON items(batch_id, idx);
	CREATE TABLE uploads (
		id TEXT PRIMARY KEY, item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
		batch_id TEXT NOT NULL, user TEXT NOT NULL, length INTEGER NOT NULL,
		created_at INTEGER NOT NULL, completed_at INTEGER NOT NULL DEFAULT 0);`,
	// M2: TV episodes and library files displaced by "replace".
	`ALTER TABLE items ADD COLUMN season INTEGER NOT NULL DEFAULT -1;
	ALTER TABLE items ADD COLUMN episode INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE items ADD COLUMN episode_end INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE items ADD COLUMN displace TEXT NOT NULL DEFAULT '';`,
	// M3: history, undo, quick upload, Jellyfin hook.
	`ALTER TABLE batches ADD COLUMN hook TEXT NOT NULL DEFAULT '';
	ALTER TABLE items ADD COLUMN undo_path TEXT NOT NULL DEFAULT '';`,
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serializes writes and avoids SQLITE_BUSY; the
	// workload is tiny (one household).
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database is usable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// NewID returns a random 128-bit hex identifier.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// --- settings ---

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// --- sessions ---

type Session struct {
	ID        string
	User      string
	ExpiresAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, user string, ttl time.Duration) (*Session, error) {
	now := time.Now()
	sess := &Session{ID: NewID() + NewID(), User: user, ExpiresAt: now.Add(ttl)}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id,user,created_at,expires_at) VALUES(?,?,?,?)`,
		sess.ID, user, now.Unix(), sess.ExpiresAt.Unix())
	return sess, err
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	var sess Session
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT id,user,expires_at FROM sessions WHERE id=? AND expires_at>?`,
		id, time.Now().Unix()).Scan(&sess.ID, &sess.User, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	sess.ExpiresAt = time.Unix(exp, 0)
	return &sess, err
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}

func (s *Store) PurgeSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<=?`, time.Now().Unix())
	return err
}

// --- batches and items ---

// Batch statuses.
const (
	BatchActive    = "active"
	BatchDone      = "done"
	BatchCancelled = "cancelled"
	BatchUndone    = "undone"
)

// Item statuses.
const (
	ItemPending   = "pending"
	ItemUploading = "uploading"
	ItemPublished = "published"
	ItemSkipped   = "skipped"
	ItemConflict  = "conflict" // destination appeared after planning; needs a decision
	ItemFailed    = "failed"
	ItemCancelled = "cancelled"
	ItemUndone    = "undone"
)

type Batch struct {
	ID, User, Kind       string
	Provider, ProviderID string
	Title                string
	Year                 int
	Language             string
	LibraryDir, Folder   string
	Status               string
	CreatedAt, UpdatedAt time.Time
	Hook                 string // Jellyfin notification result
	Items                []*Item
}

type Item struct {
	ID, BatchID          string
	Idx                  int
	Key, Role            string
	OriginalName, Ext    string
	Size                 int64
	Target               string
	AltPrefix, AltSuffix string
	Policy, Status       string
	UploadID             string
	FinalPath            string
	TrashedPath          string
	Error, PermWarning   string
	PublishedAt          time.Time
	// TV only: Season is -1 for movies; EpisodeEnd is 0 unless a range.
	Season, Episode, EpisodeEnd int
	// Displace lists other library files of the same episode that "replace"
	// moves to the trash. TrashedPath is newline-separated for the same reason.
	Displace []string
	UndoPath string // where undo moved the published file
}

func (s *Store) CreateBatch(ctx context.Context, b *Batch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	b.CreatedAt, b.UpdatedAt = now, now
	if _, err := tx.ExecContext(ctx, `INSERT INTO batches(id,user,kind,provider,provider_id,title,year,language,library_dir,folder,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, b.ID, b.User, b.Kind, b.Provider, b.ProviderID, b.Title, b.Year,
		b.Language, b.LibraryDir, b.Folder, b.Status, now.Unix(), now.Unix()); err != nil {
		return err
	}
	for _, it := range b.Items {
		it.BatchID = b.ID
		if _, err := tx.ExecContext(ctx, `INSERT INTO items(id,batch_id,idx,client_key,role,original_name,ext,size,target,alt_prefix,alt_suffix,policy,status,season,episode,episode_end,displace)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, it.ID, b.ID, it.Idx, it.Key, it.Role, it.OriginalName, it.Ext, it.Size,
			it.Target, it.AltPrefix, it.AltSuffix, it.Policy, it.Status, it.Season, it.Episode, it.EpisodeEnd, strings.Join(it.Displace, "\n")); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const itemCols = `id,batch_id,idx,client_key,role,original_name,ext,size,target,alt_prefix,alt_suffix,policy,status,upload_id,final_path,trashed_path,error,perm_warning,published_at,season,episode,episode_end,displace,undo_path`

func scanItem(sc interface{ Scan(...any) error }) (*Item, error) {
	var it Item
	var pub int64
	var displace string
	err := sc.Scan(&it.ID, &it.BatchID, &it.Idx, &it.Key, &it.Role, &it.OriginalName, &it.Ext, &it.Size, &it.Target,
		&it.AltPrefix, &it.AltSuffix, &it.Policy, &it.Status, &it.UploadID, &it.FinalPath, &it.TrashedPath, &it.Error, &it.PermWarning, &pub,
		&it.Season, &it.Episode, &it.EpisodeEnd, &displace, &it.UndoPath)
	if displace != "" {
		it.Displace = strings.Split(displace, "\n")
	}
	if pub > 0 {
		it.PublishedAt = time.Unix(pub, 0)
	}
	return &it, err
}

// GetBatch loads a batch with its items.
func (s *Store) GetBatch(ctx context.Context, id string) (*Batch, error) {
	var b Batch
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id,user,kind,provider,provider_id,title,year,language,library_dir,folder,status,created_at,updated_at,hook
		FROM batches WHERE id=?`, id).Scan(&b.ID, &b.User, &b.Kind, &b.Provider, &b.ProviderID, &b.Title, &b.Year,
		&b.Language, &b.LibraryDir, &b.Folder, &b.Status, &created, &updated, &b.Hook)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.CreatedAt, b.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemCols+` FROM items WHERE batch_id=? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		b.Items = append(b.Items, it)
	}
	return &b, rows.Err()
}

func (s *Store) GetItem(ctx context.Context, id string) (*Item, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, `SELECT `+itemCols+` FROM items WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

// UpdateItem writes the mutable item fields.
func (s *Store) UpdateItem(ctx context.Context, it *Item) error {
	var pub int64
	if !it.PublishedAt.IsZero() {
		pub = it.PublishedAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE items SET policy=?,status=?,upload_id=?,final_path=?,trashed_path=?,error=?,perm_warning=?,published_at=?,undo_path=? WHERE id=?`,
		it.Policy, it.Status, it.UploadID, it.FinalPath, it.TrashedPath, it.Error, it.PermWarning, pub, it.UndoPath, it.ID)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE batches SET updated_at=? WHERE id=?`, time.Now().Unix(), it.BatchID)
	}
	return err
}

func (s *Store) SetBatchStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE batches SET status=?, updated_at=? WHERE id=?`, status, time.Now().Unix(), id)
	return err
}

func (s *Store) SetBatchHook(ctx context.Context, id, hook string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE batches SET hook=? WHERE id=?`, hook, id)
	return err
}

// BatchSummary is a history row.
type BatchSummary struct {
	ID, User, Kind, Title string
	Year                  int
	LibraryDir, Folder    string
	Status, Hook          string
	CreatedAt, UpdatedAt  time.Time
	Items, Published      int
	Skipped, Failed       int
	Undone                int
	PublishedBytes        int64
}

// ListBatches returns the newest batches of a user, with item counts.
func (s *Store) ListBatches(ctx context.Context, user string, limit, offset int) ([]BatchSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.id,b.user,b.kind,b.title,b.year,b.library_dir,b.folder,b.status,b.hook,b.created_at,b.updated_at,
		COUNT(i.id),
		COALESCE(SUM(i.status='published'),0), COALESCE(SUM(i.status='skipped'),0),
		COALESCE(SUM(i.status IN ('failed','conflict')),0), COALESCE(SUM(i.status='undone'),0),
		COALESCE(SUM(CASE WHEN i.status='published' THEN i.size ELSE 0 END),0)
		FROM batches b LEFT JOIN items i ON i.batch_id=b.id
		WHERE b.user=? GROUP BY b.id ORDER BY b.created_at DESC, b.id LIMIT ? OFFSET ?`, user, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BatchSummary
	for rows.Next() {
		var b BatchSummary
		var created, updated int64
		if err := rows.Scan(&b.ID, &b.User, &b.Kind, &b.Title, &b.Year, &b.LibraryDir, &b.Folder, &b.Status, &b.Hook, &created, &updated,
			&b.Items, &b.Published, &b.Skipped, &b.Failed, &b.Undone, &b.PublishedBytes); err != nil {
			return nil, err
		}
		b.CreatedAt, b.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// --- uploads ---

type Upload struct {
	ID, ItemID, BatchID, User string
	Length                    int64
	CreatedAt, CompletedAt    time.Time
}

func (s *Store) CreateUpload(ctx context.Context, u *Upload) error {
	u.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO uploads(id,item_id,batch_id,user,length,created_at) VALUES(?,?,?,?,?,?)`,
		u.ID, u.ItemID, u.BatchID, u.User, u.Length, u.CreatedAt.Unix())
	return err
}

func (s *Store) GetUpload(ctx context.Context, id string) (*Upload, error) {
	var u Upload
	var created, completed int64
	err := s.db.QueryRowContext(ctx, `SELECT id,item_id,batch_id,user,length,created_at,completed_at FROM uploads WHERE id=?`, id).
		Scan(&u.ID, &u.ItemID, &u.BatchID, &u.User, &u.Length, &created, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	u.CreatedAt = time.Unix(created, 0)
	if completed > 0 {
		u.CompletedAt = time.Unix(completed, 0)
	}
	return &u, err
}

func (s *Store) CompleteUpload(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE uploads SET completed_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM uploads WHERE id=?`, id)
	return err
}

// StaleUploads returns incomplete uploads created before t.
func (s *Store) StaleUploads(ctx context.Context, before time.Time) ([]*Upload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,item_id,batch_id,user,length,created_at FROM uploads WHERE completed_at=0 AND created_at<?`, before.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Upload
	for rows.Next() {
		var u Upload
		var created int64
		if err := rows.Scan(&u.ID, &u.ItemID, &u.BatchID, &u.User, &u.Length, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0)
		out = append(out, &u)
	}
	return out, rows.Err()
}

// UploadExists reports whether an upload row exists (used to find orphan staging files).
func (s *Store) UploadExists(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM uploads WHERE id=?`, id).Scan(&n)
	return n > 0, err
}
