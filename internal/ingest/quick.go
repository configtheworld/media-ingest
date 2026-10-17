package ingest

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/media-ingest/media-ingest/internal/mediafs"
	"github.com/media-ingest/media-ingest/internal/naming"
)

// Quick upload: no metadata. Files keep their names (sanitized) and go into
// an existing folder under MEDIA_ROOT, optionally into a new sub-folder.

const maxQuickDepth = 5

// BrowseView lists the sub-folders of a folder under MEDIA_ROOT.
type BrowseView struct {
	Path   string   `json:"path"` // "" = MEDIA_ROOT
	Parent string   `json:"parent"`
	Dirs   []string `json:"dirs"`
}

// browsable checks that rel is "" or an existing, visible directory outside
// the staging and trash areas.
func (s *Service) browsable(rel string) error {
	if rel == "" {
		return nil
	}
	if err := mediafs.CheckRel(rel); err != nil {
		return invalid("invalid_folder", "")
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return invalid("invalid_folder", "")
		}
	}
	for _, private := range []string{s.cfg.StagingDir, s.cfg.TrashDir} {
		if rel == private || strings.HasPrefix(rel, private+"/") {
			return invalid("invalid_folder", "")
		}
	}
	fi, err := s.fs.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return invalid("unknown_folder", "")
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return invalid("invalid_folder", "")
	}
	return nil
}

// Browse lists sub-folders (no files) of rel.
func (s *Service) Browse(rel string) (*BrowseView, error) {
	if err := s.browsable(rel); err != nil {
		return nil, err
	}
	dir := rel
	if dir == "" {
		dir = "."
	}
	var entries []fs.DirEntry
	var err error
	if dir == "." {
		entries, err = s.fs.ReadRoot()
	} else {
		entries, err = s.fs.ReadDir(dir)
	}
	if err != nil {
		return nil, err
	}
	v := &BrowseView{Path: rel, Dirs: []string{}}
	if rel != "" {
		if v.Parent = path.Dir(rel); v.Parent == "." {
			v.Parent = ""
		}
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { // IsDir is false for symlinks
			continue
		}
		child := e.Name()
		if rel != "" {
			child = rel + "/" + e.Name()
		}
		if child == s.cfg.StagingDir || child == s.cfg.TrashDir {
			continue
		}
		v.Dirs = append(v.Dirs, e.Name())
	}
	sort.Slice(v.Dirs, func(i, j int) bool { return strings.ToLower(v.Dirs[i]) < strings.ToLower(v.Dirs[j]) })
	return v, nil
}

type QuickRequest struct {
	Folder        string      `json:"folder"`        // existing folder under MEDIA_ROOT
	NewFolder     string      `json:"newFolder"`     // optional new sub-folder name
	KeepStructure bool        `json:"keepStructure"` // keep the relative folders of dropped directories
	Items         []QuickItem `json:"items"`
}

type QuickItem struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Path   string `json:"path"` // relative path as dropped, e.g. "Show/Season 1/a.mkv"
	Size   int64  `json:"size"`
	Policy string `json:"policy"`
}

// PlanQuick validates a quick upload and computes destinations.
func (s *Service) PlanQuick(ctx context.Context, req *QuickRequest) (*Plan, error) {
	if req.Folder == "" {
		return nil, invalid("folder_required", "")
	}
	if err := s.browsable(req.Folder); err != nil {
		return nil, err
	}
	if len(req.Items) == 0 || len(req.Items) > maxItems {
		return nil, invalid("invalid_item_count", "")
	}
	base := req.Folder
	newFolder := ""
	if strings.TrimSpace(req.NewFolder) != "" {
		if len(req.NewFolder) > 255 {
			return nil, invalid("invalid_new_folder", "")
		}
		newFolder = naming.SanitizeComponent(req.NewFolder)
		if newFolder == "" {
			return nil, invalid("invalid_new_folder", "")
		}
		base = path.Join(base, newFolder)
	}
	plan := &Plan{Kind: "quick", Title: base, LibraryDir: req.Folder, Folder: newFolder, FolderSource: "quick",
		FolderCandidates: []string{}, Warnings: []Warning{}}
	if ok, _ := s.fs.Exists(base); ok {
		plan.FolderExists = true
	}
	seen := map[string]bool{}
	renamed := 0
	for _, it := range req.Items {
		if seen[it.Key] {
			return nil, invalid("duplicate_key", it.Key)
		}
		seen[it.Key] = true
		ext, err := s.checkFile(it.Key, it.Name, it.Size)
		if err != nil {
			return nil, err
		}
		name := naming.SanitizeComponent(it.Name)
		if name == "" || naming.Ext(name) != ext {
			return nil, invalid("invalid_name", it.Key)
		}
		if name != it.Name {
			renamed++
		}
		dirs := []string{base}
		if req.KeepStructure && it.Path != "" {
			if len(it.Path) > 1024 {
				return nil, invalid("invalid_name", it.Key)
			}
			parts := strings.Split(strings.ReplaceAll(it.Path, "\\", "/"), "/")
			parts = parts[:len(parts)-1] // the last part is the file name
			if len(parts) > maxQuickDepth {
				return nil, invalid("too_deep", it.Key)
			}
			for _, p := range parts {
				if naming.IsJunkName(p) {
					return nil, &ValidationError{Code: "junk_file", Params: map[string]any{"key": it.Key}}
				}
				if c := naming.SanitizeComponent(p); c != "" {
					dirs = append(dirs, c)
				}
			}
		}
		target := path.Join(append(dirs, name)...)
		stem := strings.TrimSuffix(target, "."+ext)
		plan.Items = append(plan.Items, PlannedItem{Key: it.Key, Role: "file", Name: it.Name, Ext: ext, Size: it.Size,
			Policy: it.Policy, Path: target, altPrefix: stem + " (", altSuffix: ")." + ext, season: -1})
	}
	if renamed > 0 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "names_sanitized", Params: map[string]any{"count": renamed}})
	}
	return plan, s.finishPlan(plan)
}

// CreateQuickBatch is CreateMovieBatch for quick uploads.
func (s *Service) CreateQuickBatch(ctx context.Context, user string, req *QuickRequest) (*BatchView, error) {
	plan, err := s.PlanQuick(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.createBatch(ctx, user, "", plan)
}
