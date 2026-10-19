package ingest

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/configtheworld/media-digest/internal/mediafs"
	"github.com/configtheworld/media-digest/internal/store"
)

// Conflict policies accepted from the client.
const (
	PolicySkip     = "skip"
	PolicyReplace  = "replace"
	PolicyKeepBoth = "keep_both"
)

func validPolicy(p string, allowEmpty bool) bool {
	switch p {
	case PolicySkip, PolicyReplace, PolicyKeepBoth:
		return true
	case "":
		return allowEmpty
	}
	return false
}

// ErrForbidden is returned when a user touches someone else's batch.
var ErrForbidden = errors.New("forbidden")

// CreateMovieBatch re-plans the request (the dry-run result is never
// trusted), requires a decision for every conflict and stores the batch.
func (s *Service) CreateMovieBatch(ctx context.Context, user string, req *MovieRequest) (*BatchView, error) {
	plan, err := s.PlanMovie(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.createBatch(ctx, user, req.Language, plan)
}

// CreateTVBatch is CreateMovieBatch for episodes.
func (s *Service) CreateTVBatch(ctx context.Context, user string, req *TVRequest) (*BatchView, error) {
	plan, err := s.PlanTV(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.createBatch(ctx, user, req.Language, plan)
}

func (s *Service) createBatch(ctx context.Context, user, language string, plan *Plan) (*BatchView, error) {
	b := &store.Batch{ID: store.NewID(), User: user, Kind: plan.Kind, Provider: plan.Provider, ProviderID: plan.ID,
		Title: plan.Title, Year: plan.Year, Language: language, LibraryDir: plan.LibraryDir, Folder: plan.Folder,
		Status: store.BatchActive}
	var missing []string
	for i, pi := range plan.Items {
		if !validPolicy(pi.Policy, true) {
			return nil, invalid("invalid_policy", pi.Key)
		}
		if pi.Conflict && pi.Policy == "" {
			missing = append(missing, pi.Key)
		}
		it := &store.Item{ID: store.NewID(), Idx: i, Key: pi.Key, Role: pi.Role, OriginalName: pi.Name, Ext: pi.Ext,
			Size: pi.Size, Target: pi.Path, AltPrefix: pi.altPrefix, AltSuffix: pi.altSuffix, Policy: pi.Policy, Status: store.ItemPending,
			Season: pi.season, Episode: pi.episode, EpisodeEnd: pi.episodeEnd}
		if pi.Policy == PolicyReplace {
			it.Displace = pi.Existing
		}
		if pi.Conflict && pi.Policy == PolicySkip {
			it.Status = store.ItemSkipped
		}
		b.Items = append(b.Items, it)
	}
	if len(missing) > 0 {
		return nil, &ValidationError{Code: "conflict_policy_required", Params: map[string]any{"keys": missing}}
	}
	if _, err := s.fs.EnsureDir(s.cfg.StagingDir, false); err != nil {
		return nil, err
	}
	s.refreshBatchStatus(b)
	if err := s.store.CreateBatch(ctx, b); err != nil {
		return nil, err
	}
	s.log.Info("batch created", "batch", b.ID, "user", user, "kind", b.Kind, "provider_id", b.ProviderID, "items", len(b.Items))
	return s.view(b), nil
}

// BatchView is the API representation of a batch.
type BatchView struct {
	ID     string     `json:"id"`
	Kind   string     `json:"kind"`
	Title  string     `json:"title"`
	Year   int        `json:"year,omitempty"`
	Folder string     `json:"folder"`
	Status string     `json:"status"`
	Hook   string     `json:"hook,omitempty"`
	Items  []ItemView `json:"items"`
}

type ItemView struct {
	ID          string `json:"id"`
	Episode     string `json:"episode,omitempty"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Size        int64  `json:"size"`
	Offset      int64  `json:"offset"`
	Target      string `json:"target"`
	FinalPath   string `json:"finalPath,omitempty"`
	TrashedPath string `json:"trashedPath,omitempty"`
	Status      string `json:"status"`
	Policy      string `json:"policy,omitempty"`
	Error       string `json:"error,omitempty"`
	PermWarning string `json:"permWarning,omitempty"`
	UploadURL   string `json:"uploadUrl,omitempty"`
	UndoPath    string `json:"undoPath,omitempty"`
}

func (s *Service) view(b *store.Batch) *BatchView {
	v := &BatchView{ID: b.ID, Kind: b.Kind, Title: b.Title, Year: b.Year, Folder: b.Folder, Status: b.Status, Hook: b.Hook, Items: []ItemView{}}
	for _, it := range b.Items {
		iv := ItemView{ID: it.ID, Episode: episodeLabel(it.Season, it.Episode, it.EpisodeEnd), Key: it.Key, Name: it.OriginalName, Role: it.Role, Size: it.Size, Target: it.Target,
			FinalPath: it.FinalPath, TrashedPath: it.TrashedPath, Status: it.Status, Policy: it.Policy, Error: it.Error, PermWarning: it.PermWarning,
			UndoPath: it.UndoPath}
		switch it.Status {
		case store.ItemPublished:
			iv.Offset = it.Size
		case store.ItemUploading, store.ItemConflict:
			if it.UploadID != "" {
				iv.UploadURL = UploadPath + it.UploadID
				if fi, err := s.fs.Lstat(s.stagingPath(it.UploadID)); err == nil {
					iv.Offset = fi.Size()
				}
			}
		}
		v.Items = append(v.Items, iv)
	}
	return v
}

// GetBatch returns a batch owned by user.
func (s *Service) GetBatch(ctx context.Context, user, id string) (*BatchView, error) {
	b, err := s.ownedBatch(ctx, user, id)
	if err != nil {
		return nil, err
	}
	return s.view(b), nil
}

var idRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (s *Service) ownedBatch(ctx context.Context, user, id string) (*store.Batch, error) {
	if !idRe.MatchString(id) {
		return nil, store.ErrNotFound
	}
	b, err := s.store.GetBatch(ctx, id)
	if err != nil {
		return nil, err
	}
	if b.User != user {
		return nil, store.ErrNotFound // do not reveal other users' batches
	}
	return b, nil
}

func (s *Service) stagingPath(uploadID string) string {
	return path.Join(s.cfg.StagingDir, uploadID+".part")
}

func (s *Service) trashPath(b *store.Batch, target string) string {
	return path.Join(s.cfg.TrashDir, time.Now().UTC().Format("20060102-150405")+"-"+b.ID[:8], target)
}

// publish moves a completed upload into the library and records the result.
func (s *Service) publish(ctx context.Context, b *store.Batch, it *store.Item) error {
	staged := s.stagingPath(it.UploadID)
	alt := func(n int) string { return it.AltPrefix + strconv.Itoa(n) + it.AltSuffix }
	pol := mediafs.PolicyNone
	switch it.Policy {
	case PolicyReplace:
		pol = mediafs.PolicyReplace
	case PolicyKeepBoth:
		pol = mediafs.PolicyKeepBoth
	}
	var displaced []string
	if it.Policy == PolicyReplace {
		for _, p := range it.Displace {
			if p == it.Target {
				continue
			}
			if ok, _ := s.fs.Exists(p); !ok {
				continue
			}
			t := s.trashPath(b, p)
			if err := s.fs.MoveToTrash(p, t); err != nil {
				it.Status, it.Error = store.ItemFailed, err.Error()
				s.log.Error("displace failed", "batch", b.ID, "item", it.ID, "err", err)
				if uerr := s.store.UpdateItem(ctx, it); uerr != nil {
					return uerr
				}
				s.updateBatchStatus(ctx, b.ID)
				return nil
			}
			displaced = append(displaced, t)
			s.log.Info("moved to trash", "batch", b.ID, "path", p, "trash", t)
		}
	}
	res, err := s.fs.Publish(staged, it.Target, pol, alt, s.trashPath(b, it.Target))
	if res.TrashedPath != "" {
		displaced = append(displaced, res.TrashedPath)
	}
	res.TrashedPath = strings.Join(displaced, "\n")
	switch {
	case err == nil:
		it.Status, it.FinalPath, it.TrashedPath, it.PermWarning, it.Error = store.ItemPublished, res.Path, res.TrashedPath, res.PermWarning, ""
		it.PublishedAt = time.Now()
		_ = s.store.CompleteUpload(ctx, it.UploadID)
		s.log.Info("published", "batch", b.ID, "item", it.ID, "path", res.Path, "replaced_to", res.TrashedPath)
		if res.PermWarning != "" {
			s.log.Warn("permission warning", "batch", b.ID, "warning", res.PermWarning)
		}
	case errors.Is(err, mediafs.ErrConflict):
		it.Status, it.Error = store.ItemConflict, "destination_exists"
		s.log.Info("publish conflict", "batch", b.ID, "item", it.ID, "path", it.Target)
	default:
		it.Status, it.Error = store.ItemFailed, err.Error()
		s.log.Error("publish failed", "batch", b.ID, "item", it.ID, "err", err)
	}
	if uerr := s.store.UpdateItem(ctx, it); uerr != nil {
		return uerr
	}
	s.updateBatchStatus(ctx, b.ID)
	return nil
}

func (s *Service) refreshBatchStatus(b *store.Batch) {
	if b.Status == store.BatchCancelled {
		return
	}
	for _, it := range b.Items {
		switch it.Status {
		case store.ItemPublished, store.ItemSkipped, store.ItemCancelled:
		default:
			b.Status = store.BatchActive
			return
		}
	}
	b.Status = store.BatchDone
}

func (s *Service) updateBatchStatus(ctx context.Context, id string) {
	b, err := s.store.GetBatch(ctx, id)
	if err != nil {
		return
	}
	old := b.Status
	s.refreshBatchStatus(b)
	if b.Status != old {
		_ = s.store.SetBatchStatus(ctx, id, b.Status)
		if b.Status == store.BatchDone {
			s.log.Info("batch done", "batch", id)
			s.notify(id, s.publishUpdates(b))
		}
	}
}

// Resolve applies a conflict decision to an item whose destination appeared
// after planning.
func (s *Service) Resolve(ctx context.Context, user, batchID, itemID, policy string) (*BatchView, error) {
	b, err := s.ownedBatch(ctx, user, batchID)
	if err != nil {
		return nil, err
	}
	if !validPolicy(policy, false) {
		return nil, invalid("invalid_policy", "")
	}
	var it *store.Item
	for _, x := range b.Items {
		if x.ID == itemID {
			it = x
		}
	}
	if it == nil {
		return nil, store.ErrNotFound
	}
	if it.Status != store.ItemConflict {
		return nil, invalid("not_in_conflict", "")
	}
	it.Policy = policy
	if policy == PolicySkip {
		s.dropStaged(ctx, it.UploadID)
		it.Status, it.Error = store.ItemSkipped, ""
		if err := s.store.UpdateItem(ctx, it); err != nil {
			return nil, err
		}
		s.updateBatchStatus(ctx, b.ID)
	} else if err := s.publish(ctx, b, it); err != nil {
		return nil, err
	}
	return s.GetBatch(ctx, user, batchID)
}

// Cancel stops a batch: staged data is removed, published files stay.
func (s *Service) Cancel(ctx context.Context, user, batchID string) (*BatchView, error) {
	b, err := s.ownedBatch(ctx, user, batchID)
	if err != nil {
		return nil, err
	}
	for _, it := range b.Items {
		switch it.Status {
		case store.ItemPublished, store.ItemSkipped, store.ItemCancelled:
			continue
		}
		if it.UploadID != "" {
			s.dropStaged(ctx, it.UploadID)
		}
		it.Status, it.UploadID = store.ItemCancelled, ""
		if err := s.store.UpdateItem(ctx, it); err != nil {
			return nil, err
		}
	}
	if err := s.store.SetBatchStatus(ctx, b.ID, store.BatchCancelled); err != nil {
		return nil, err
	}
	s.log.Info("batch cancelled", "batch", b.ID)
	return s.GetBatch(ctx, user, batchID)
}

// dropStaged removes a staged upload (our own temporary data, never library files).
func (s *Service) dropStaged(ctx context.Context, uploadID string) {
	if uploadID == "" {
		return
	}
	if err := s.fs.Remove(s.stagingPath(uploadID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("could not remove staged file", "upload", uploadID, "err", err)
	}
	_ = s.store.DeleteUpload(ctx, uploadID)
}

var stagedNameRe = regexp.MustCompile(`^([0-9a-f]{32})\.part$`)

// Janitor expires abandoned uploads and orphaned staging files.
func (s *Service) Janitor(ctx context.Context) {
	stale, err := s.store.StaleUploads(ctx, time.Now().Add(-s.cfg.UploadExpiry))
	if err != nil {
		s.log.Error("janitor", "err", err)
		return
	}
	for _, u := range stale {
		s.dropStaged(ctx, u.ID)
		if it, err := s.store.GetItem(ctx, u.ItemID); err == nil && it.UploadID == u.ID && it.Status == store.ItemUploading {
			it.Status, it.UploadID, it.Error = store.ItemFailed, "", "upload_expired"
			_ = s.store.UpdateItem(ctx, it)
		}
		s.log.Info("expired stale upload", "upload", u.ID)
	}
	entries, err := s.fs.ReadDir(s.cfg.StagingDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		m := stagedNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue // never touch files we did not create
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		if ok, err := s.store.UploadExists(ctx, m[1]); err == nil && !ok {
			_ = s.fs.Remove(path.Join(s.cfg.StagingDir, e.Name()))
			s.log.Info("removed orphaned staging file", "file", e.Name())
		}
	}
	s.purgeTrash()
	_ = s.store.PurgeSessions(ctx)
}
