package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/configtheworld/media-digest/internal/jellyfin"
	"github.com/configtheworld/media-digest/internal/parser"
	"github.com/configtheworld/media-digest/internal/store"
)

// --- parser settings ---

const parserKey = "parser"

// ParserSettings choose rule packs and optional custom rules (YAML).
type ParserSettings struct {
	Packs  []string `json:"packs"`
	Custom string   `json:"custom"`
}

func (s *Service) packsDir() string { return filepath.Join(s.cfg.DataDir, "rules") }

// GetParserSettings returns the stored settings; by default every built-in
// pack is enabled.
func (s *Service) GetParserSettings(ctx context.Context) (ParserSettings, []parser.Pack, error) {
	packs, err := parser.LoadPacks(s.packsDir())
	if err != nil {
		s.log.Warn("some rule packs could not be loaded", "err", err)
	}
	ps := ParserSettings{Packs: parser.BuiltinIDs()}
	raw, ok, err := s.store.GetSetting(ctx, parserKey)
	if err != nil {
		return ps, packs, err
	}
	if ok {
		if err := json.Unmarshal([]byte(raw), &ps); err != nil {
			s.log.Warn("ignoring invalid parser settings", "err", err)
			ps = ParserSettings{Packs: parser.BuiltinIDs()}
		}
	}
	return ps, packs, nil
}

// SetParserSettings validates and stores parser settings.
func (s *Service) SetParserSettings(ctx context.Context, ps ParserSettings) error {
	_, packs, err := s.GetParserSettings(ctx)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, p := range packs {
		known[p.ID] = true
	}
	seen := map[string]bool{}
	var clean []string
	for _, id := range ps.Packs {
		if !known[id] {
			return invalid("unknown_rule_pack", id)
		}
		if !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	ps.Packs = clean
	if strings.TrimSpace(ps.Custom) != "" {
		if _, _, _, err := parser.ParseYAML([]byte(ps.Custom)); err != nil {
			return invalid("invalid_custom_rules", err.Error())
		}
	}
	raw, _ := json.Marshal(ps)
	return s.store.SetSetting(ctx, parserKey, string(raw))
}

// EffectiveRules returns custom rules, enabled packs and defaults, in order.
func (s *Service) EffectiveRules(ctx context.Context) (parser.Rules, error) {
	ps, packs, err := s.GetParserSettings(ctx)
	if err != nil {
		return parser.Rules{}, err
	}
	var custom *parser.Rules
	if strings.TrimSpace(ps.Custom) != "" {
		if _, _, r, err := parser.ParseYAML([]byte(ps.Custom)); err == nil {
			custom = &r
		}
	}
	return parser.Compose(custom, packs, ps.Packs), nil
}

// --- Jellyfin hook ---

// notify reports a batch's changes to Jellyfin in the background. It never
// affects the batch itself ("fails soft"); the result is stored for history.
func (s *Service) notify(batchID string, updates []jellyfin.Update) {
	if s.jf == nil || len(updates) == 0 {
		return
	}
	s.hooks.Add(1)
	go func() {
		defer s.hooks.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		res := s.jf.Notify(ctx, updates)
		if res.Method == "failed" {
			s.log.Warn("jellyfin hook failed", "batch", batchID, "err", res.Error)
		} else {
			s.log.Info("jellyfin notified", "batch", batchID, "method", res.Method, "paths", len(updates))
		}
		_ = s.store.SetBatchHook(ctx, batchID, res.String())
	}()
}

// WaitHooks waits for running Jellyfin notifications (tests, shutdown).
func (s *Service) WaitHooks() { s.hooks.Wait() }

// originalOf maps a trash path back to the library path it came from:
// <TRASH_DIR>/<stamp>/<original path>.
func (s *Service) originalOf(trash string) string {
	rel, ok := strings.CutPrefix(trash, s.cfg.TrashDir+"/")
	if !ok {
		return ""
	}
	_, orig, ok := strings.Cut(rel, "/")
	if !ok {
		return ""
	}
	return orig
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// publishUpdates describes a finished batch for Jellyfin.
func (s *Service) publishUpdates(b *store.Batch) []jellyfin.Update {
	var out []jellyfin.Update
	for _, it := range b.Items {
		if it.Status != store.ItemPublished {
			continue
		}
		typ := jellyfin.Created
		for _, t := range splitLines(it.TrashedPath) {
			if orig := s.originalOf(t); orig == it.FinalPath {
				typ = jellyfin.Modified
			} else if orig != "" {
				out = append(out, jellyfin.Update{Path: orig, Type: jellyfin.Deleted})
			}
		}
		out = append(out, jellyfin.Update{Path: it.FinalPath, Type: typ})
	}
	return out
}

// --- history ---

type HistoryEntry struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Year       int       `json:"year,omitempty"`
	LibraryDir string    `json:"libraryDir"`
	Folder     string    `json:"folder"`
	Status     string    `json:"status"`
	Hook       string    `json:"hook,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	Items      int       `json:"items"`
	Published  int       `json:"published"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	Undone     int       `json:"undone"`
	Bytes      int64     `json:"bytes"`
	CanUndo    bool      `json:"canUndo"`
}

func (s *Service) History(ctx context.Context, user string, limit, offset int) ([]HistoryEntry, error) {
	rows, err := s.store.ListBatches(ctx, user, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, HistoryEntry{ID: r.ID, Kind: r.Kind, Title: r.Title, Year: r.Year, LibraryDir: r.LibraryDir, Folder: r.Folder,
			Status: r.Status, Hook: r.Hook, CreatedAt: r.CreatedAt, Items: r.Items, Published: r.Published, Skipped: r.Skipped,
			Failed: r.Failed, Undone: r.Undone, Bytes: r.PublishedBytes, CanUndo: r.Published > 0 && r.Status != store.BatchUndone})
	}
	return out, nil
}

// --- undo ---

// Undo moves every file a batch published to the trash (never deletes) and
// restores the files it had replaced, where their old place is still free.
// A published file that was changed since (different size) is left alone.
func (s *Service) Undo(ctx context.Context, user, batchID string) (*BatchView, error) {
	b, err := s.ownedBatch(ctx, user, batchID)
	if err != nil {
		return nil, err
	}
	if b.Status == store.BatchUndone {
		return nil, invalid("already_undone", "")
	}
	if b.Status == store.BatchActive {
		if _, err := s.Cancel(ctx, user, batchID); err != nil {
			return nil, err
		}
		if b, err = s.ownedBatch(ctx, user, batchID); err != nil {
			return nil, err
		}
	}
	undoDir := path.Join(s.cfg.TrashDir, time.Now().UTC().Format("20060102-150405")+"-"+b.ID[:8]+"-undo")
	stop := b.LibraryDir
	var updates []jellyfin.Update
	for _, it := range b.Items {
		if it.Status != store.ItemPublished {
			continue
		}
		fi, err := s.fs.Lstat(it.FinalPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			it.Error = "missing_at_undo"
		case err != nil:
			return nil, err
		case !fi.Mode().IsRegular() || fi.Size() != it.Size:
			it.Error = "changed_since_publish"
			_ = s.store.UpdateItem(ctx, it)
			s.log.Warn("undo skipped a file changed since publish", "batch", b.ID, "path", it.FinalPath)
			continue
		default:
			dst := path.Join(undoDir, it.FinalPath)
			if err := s.fs.MoveToTrash(it.FinalPath, dst); err != nil {
				it.Error = err.Error()
				_ = s.store.UpdateItem(ctx, it)
				s.log.Error("undo failed", "batch", b.ID, "path", it.FinalPath, "err", err)
				continue
			}
			it.UndoPath = dst
			updates = append(updates, jellyfin.Update{Path: it.FinalPath, Type: jellyfin.Deleted})
		}
		var kept []string
		for _, t := range splitLines(it.TrashedPath) {
			orig := s.originalOf(t)
			if orig == "" {
				continue
			}
			if err := s.fs.Restore(t, orig); err != nil {
				kept = append(kept, t)
				s.log.Warn("could not restore replaced file", "trash", t, "path", orig, "err", err)
				continue
			}
			updates = append(updates, jellyfin.Update{Path: orig, Type: jellyfin.Created})
		}
		it.TrashedPath = strings.Join(kept, "\n")
		if len(kept) > 0 && it.Error == "" {
			it.Error = "not_all_restored"
		}
		s.fs.PruneEmptyDirs(path.Dir(it.FinalPath), stop)
		it.Status = store.ItemUndone
		if err := s.store.UpdateItem(ctx, it); err != nil {
			return nil, err
		}
		s.log.Info("undone", "batch", b.ID, "path", it.FinalPath, "trash", it.UndoPath)
	}
	if err := s.store.SetBatchStatus(ctx, b.ID, store.BatchUndone); err != nil {
		return nil, err
	}
	s.notify(b.ID, updates)
	return s.GetBatch(ctx, user, batchID)
}

// --- trash retention ---

var trashEntryRe = regexp.MustCompile(`^(\d{8}-\d{6})-[0-9a-f]{8}(-undo)?$`)

// purgeTrash deletes trash entries older than TRASH_RETENTION_DAYS. Only
// entries created by this app (timestamped names) are touched.
func (s *Service) purgeTrash() {
	if s.cfg.TrashRetention <= 0 {
		return
	}
	entries, err := s.fs.ReadDir(s.cfg.TrashDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		m := trashEntryRe.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		ts, err := time.Parse("20060102-150405", m[1])
		if err != nil || time.Since(ts) < s.cfg.TrashRetention {
			continue
		}
		if err := s.fs.RemoveAll(path.Join(s.cfg.TrashDir, e.Name())); err != nil {
			s.log.Warn("trash purge failed", "entry", e.Name(), "err", err)
			continue
		}
		s.log.Info("purged expired trash", "entry", e.Name())
	}
}
