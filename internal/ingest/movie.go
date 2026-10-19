package ingest

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/configtheworld/media-digest/internal/config"
	"github.com/configtheworld/media-digest/internal/mediafs"
	"github.com/configtheworld/media-digest/internal/naming"
)

// Item roles.
const (
	RoleMovie    = "movie"
	RoleExtra    = "extra"
	RoleSubtitle = "subtitle"
)

const maxItems = 500

// MovieRequest is the structured description of a movie ingest. It never
// contains paths: the server derives every name from provider metadata.
type MovieRequest struct {
	Provider string       `json:"provider"`
	ID       string       `json:"id"`
	Language string       `json:"language"`
	Title    string       `json:"title"` // optional display title override
	Folder   FolderChoice `json:"folder"`
	Items    []MovieItem  `json:"items"`
}

type FolderChoice struct {
	Mode string `json:"mode"` // auto | new | existing
	Name string `json:"name"` // existing folder name (must be listed by the server)
}

type MovieItem struct {
	Key       string `json:"key"`
	Name      string `json:"name"` // original file name, used for the extension and display only
	Size      int64  `json:"size"`
	Role      string `json:"role"`
	Version   string `json:"version"`   // movie: version label; subtitle: label of the movie it belongs to
	ExtraType string `json:"extraType"` // extra: one of naming.ExtraTypes
	Label     string `json:"label"`     // extra: title
	Lang      string `json:"lang"`      // subtitle: language code
	Forced    bool   `json:"forced"`
	SDH       bool   `json:"sdh"`
	Policy    string `json:"policy"` // conflict policy, only on batch creation
}

// Plan is the dry-run result.
type Plan struct {
	Kind             string        `json:"kind"`
	Provider         string        `json:"provider"`
	ID               string        `json:"id"`
	Title            string        `json:"title"`
	ProviderTitle    string        `json:"providerTitle"`
	Year             int           `json:"year,omitempty"`
	LibraryDir       string        `json:"libraryDir"`
	Folder           string        `json:"folder"`
	FolderSource     string        `json:"folderSource"` // existing-id | existing-chosen | new
	FolderExists     bool          `json:"folderExists"`
	FolderCandidates []string      `json:"folderCandidates"`
	Items            []PlannedItem `json:"items"`
	Summary          Summary       `json:"summary"`
	Warnings         []Warning     `json:"warnings"`
}

type PlannedItem struct {
	Key    string `json:"key"`
	Role   string `json:"role"`
	Name   string `json:"name"`
	Ext    string `json:"ext"`
	Size   int64  `json:"size"`
	Path   string `json:"path"`   // relative to MEDIA_ROOT
	Exists bool   `json:"exists"` // the exact destination exists
	// Existing lists other library files of the same episode (TV only).
	Existing []string `json:"existing,omitempty"`
	// Conflict means a skip/replace/keep_both decision is required.
	Conflict bool   `json:"conflict"`
	Episode  string `json:"episode,omitempty"` // e.g. "S01E03-E04" (TV only)
	Policy   string `json:"policy,omitempty"`

	altPrefix, altSuffix        string
	season, episode, episodeEnd int
}

type Summary struct {
	Files       int   `json:"files"`
	Bytes       int64 `json:"bytes"`
	FreeBytes   int64 `json:"freeBytes"` // -1 if unknown
	Conflicts   int   `json:"conflicts"`
	EnoughSpace bool  `json:"enoughSpace"`
}

type Warning struct {
	Code   string         `json:"code"`
	Key    string         `json:"key,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

var (
	keyRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	langRe = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	subRe  = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2,4})?$`)
)

// ValidLanguage reports whether lang is an accepted provider language tag.
func ValidLanguage(lang string) bool { return langRe.MatchString(lang) }

// checkFile validates the common per-file fields and returns the extension.
func (s *Service) checkFile(key, name string, size int64) (string, error) {
	if !keyRe.MatchString(key) {
		return "", invalid("invalid_key", key)
	}
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", invalid("invalid_name", key)
	}
	if naming.IsJunkName(name) {
		return "", &ValidationError{Code: "junk_file", Params: map[string]any{"key": key}}
	}
	ext := naming.Ext(name)
	if !s.cfg.AllowedExts[ext] {
		return "", &ValidationError{Code: "extension_not_allowed", Params: map[string]any{"key": key, "ext": ext}}
	}
	if size <= 0 || size > s.cfg.MaxUploadSize {
		return "", &ValidationError{Code: "invalid_size", Params: map[string]any{"key": key, "max": s.cfg.MaxUploadSize}}
	}
	return ext, nil
}

// PlanMovie validates a movie request and computes final paths without
// touching the library.
func (s *Service) PlanMovie(ctx context.Context, req *MovieRequest) (*Plan, error) {
	p, ok := s.providers[req.Provider]
	if !ok {
		return nil, invalid("unknown_provider", req.Provider)
	}
	if !p.ValidID(req.ID) {
		return nil, invalid("invalid_id", "")
	}
	if !ValidLanguage(req.Language) {
		return nil, invalid("invalid_language", "")
	}
	if len(req.Items) == 0 || len(req.Items) > maxItems {
		return nil, invalid("invalid_item_count", "")
	}
	if len(req.Title) > 512 {
		return nil, invalid("invalid_title", "")
	}

	movie, err := p.Movie(ctx, req.ID, req.Language)
	if err != nil {
		return nil, err
	}
	nm, err := s.GetNaming(ctx)
	if err != nil {
		return nil, err
	}

	title := movie.Title
	if t := strings.TrimSpace(req.Title); t != "" {
		title = t
	}
	if naming.SanitizeComponent(title) == "" {
		return nil, invalid("invalid_title", "")
	}
	plan := &Plan{Kind: "movie", Provider: p.Name(), ID: movie.ID, Title: title, ProviderTitle: movie.Title,
		Year: movie.Year, LibraryDir: s.cfg.MoviesDir, FolderCandidates: []string{}, Warnings: []Warning{}}

	vars := naming.Vars{"title": title, "original_title": movie.Original, "id": movie.ID, "provider": p.Name()}
	if movie.Year > 0 {
		vars["year"] = movie.Year
	}
	// Reserve room in the file name for " - <version>", ".<lang>.forced.sdh" and the extension.
	segs, err := nm.Movie.Render(vars, 100)
	if err != nil {
		return nil, invalid("name_too_long", err.Error())
	}
	dirSegs, stem := segs[:len(segs)-1], segs[len(segs)-1]

	folder := ""
	if len(dirSegs) > 0 {
		folder = dirSegs[0]
	}
	if err := s.resolveFolder(plan, s.cfg.MoviesDir, req.Folder, folder, len(dirSegs) == 1, movie.ID,
		[]string{title, movie.Title, movie.Original}, movie.Year); err != nil {
		return nil, err
	}
	if plan.Folder != "" {
		dirSegs = append([]string{plan.Folder}, dirSegs[min(1, len(dirSegs)):]...)
	}
	dir := path.Join(append([]string{s.cfg.MoviesDir}, dirSegs...)...)
	if exists, err := s.fs.Exists(dir); err == nil {
		plan.FolderExists = exists
	}

	if err := s.planMovieItems(plan, req.Items, nm, dir, stem); err != nil {
		return nil, err
	}
	return plan, s.finishPlan(plan)
}

// resolveFolder decides which top-level folder to use in libDir: one the
// user explicitly chose, the templated name if it exists, an existing one
// carrying the same provider id, or a new one. Folders whose title matches
// are only offered as candidates. reusable is false when the template's
// first segment is not a stable per-title folder.
func (s *Service) resolveFolder(plan *Plan, libDir string, choice FolderChoice, folder string, reusable bool, id string, titles []string, year int) error {
	if !reusable {
		if choice.Mode == "existing" {
			return invalid("folder_reuse_unsupported", "")
		}
		plan.FolderSource = "new"
		return nil
	}
	dirSegs := []string{folder}
	folders, err := s.listLibrary(libDir)
	if err != nil {
		return err
	}
	switch choice.Mode {
	case "existing":
		for _, f := range folders {
			if f.Name == choice.Name {
				plan.Folder, plan.FolderSource = f.Name, "existing-chosen"
				return nil
			}
		}
		return invalid("unknown_folder", "")
	case "new":
		plan.Folder, plan.FolderSource = dirSegs[0], "new"
		return nil
	case "auto", "":
	default:
		return invalid("invalid_folder_mode", "")
	}
	plan.Folder, plan.FolderSource = dirSegs[0], "new"
	for _, f := range folders {
		if f.Name == dirSegs[0] {
			return nil // the templated folder itself already exists
		}
	}
	for _, f := range folders {
		if f.ProviderID == id {
			plan.Folder, plan.FolderSource = f.Name, "existing-id"
			return nil
		}
	}
	norm := make([]string, 0, len(titles))
	for _, t := range titles {
		if n := normTitle(t); n != "" {
			norm = append(norm, n)
		}
	}
	for _, f := range folders {
		if f.ProviderID != "" || f.normTitle == "" || !slices.Contains(norm, f.normTitle) {
			continue
		}
		if f.year != 0 && year != 0 && f.year != year {
			continue
		}
		plan.FolderCandidates = append(plan.FolderCandidates, f.Name)
	}
	if len(plan.FolderCandidates) > 0 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "folder_candidates", Params: map[string]any{"count": len(plan.FolderCandidates)}})
	}
	return nil
}

func (s *Service) planMovieItems(plan *Plan, items []MovieItem, nm *Naming, dir, stem string) error {
	seen := map[string]bool{}
	versions := map[string]bool{} // sanitized movie version labels
	movies := 0
	for _, it := range items {
		if seen[it.Key] {
			return invalid("duplicate_key", it.Key)
		}
		seen[it.Key] = true
		if it.Role == RoleMovie {
			movies++
			versions[naming.SanitizeComponent(it.Version)] = true
		}
	}
	if movies == 0 {
		return invalid("no_movie_file", "")
	}
	if len(versions) != movies {
		return invalid("version_labels_required", "")
	}
	if movies > 1 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "multiple_versions", Params: map[string]any{"count": movies}})
	}

	extraN := 0
	for _, it := range items {
		ext, err := s.checkFile(it.Key, it.Name, it.Size)
		if err != nil {
			return err
		}
		pi := PlannedItem{Key: it.Key, Role: it.Role, Name: it.Name, Ext: ext, Size: it.Size, Policy: it.Policy, season: -1}
		switch it.Role {
		case RoleMovie:
			if !config.IsVideo(ext) {
				return &ValidationError{Code: "role_extension_mismatch", Params: map[string]any{"key": it.Key, "ext": ext}}
			}
			base := path.Join(dir, stem)
			label := naming.SanitizeComponent(it.Version)
			if label != "" {
				base += " - " + label
				pi.altPrefix = base + " "
			} else {
				pi.altPrefix = base + " - Version "
			}
			pi.Path = base + "." + ext
			pi.altSuffix = "." + ext
		case RoleExtra:
			if !config.IsVideo(ext) {
				return &ValidationError{Code: "role_extension_mismatch", Params: map[string]any{"key": it.Key, "ext": ext}}
			}
			sub, ok := nm.ExtraDirs[it.ExtraType]
			if !ok {
				return invalid("invalid_extra_type", it.Key)
			}
			extraN++
			label := naming.SanitizeComponent(it.Label)
			if label == "" {
				label = naming.SanitizeComponent(naming.Stem(it.Name))
			}
			if label == "" {
				label = "Extra " + strconv.Itoa(extraN)
			}
			label = naming.TruncateBytes(label, naming.MaxComponentBytes-20)
			base := path.Join(dir, sub, label)
			pi.Path = base + "." + ext
			pi.altPrefix, pi.altSuffix = base+" (", ")."+ext
		case RoleSubtitle:
			if !config.IsSubtitle(ext) {
				return &ValidationError{Code: "role_extension_mismatch", Params: map[string]any{"key": it.Key, "ext": ext}}
			}
			label := naming.SanitizeComponent(it.Version)
			if !versions[label] {
				return invalid("subtitle_unknown_version", it.Key)
			}
			base := path.Join(dir, stem)
			if label != "" {
				base += " - " + label
			}
			if it.Lang != "" {
				if !subRe.MatchString(it.Lang) {
					return invalid("invalid_subtitle_language", it.Key)
				}
				base += "." + it.Lang
			}
			if it.Forced {
				base += ".forced"
			}
			if it.SDH {
				base += ".sdh"
			}
			pi.Path = base + "." + ext
			pi.altPrefix, pi.altSuffix = base+" (", ")."+ext
		default:
			return invalid("invalid_role", it.Key)
		}
		plan.Items = append(plan.Items, pi)
	}
	return nil
}

// finishPlan checks every destination, detects duplicates and computes the summary.
func (s *Service) finishPlan(plan *Plan) error {
	targets := map[string]string{}
	plan.Summary.FreeBytes = s.fs.FreeBytes(s.cfg.StagingDir)
	for i := range plan.Items {
		pi := &plan.Items[i]
		if err := mediafs.CheckRel(pi.Path); err != nil {
			return invalid("invalid_path", pi.Key)
		}
		lower := strings.ToLower(pi.Path) // case-insensitive clients (SMB, macOS) would collide too
		if other, dup := targets[lower]; dup {
			return &ValidationError{Code: "duplicate_target", Params: map[string]any{"key": pi.Key, "other": other, "path": pi.Path}}
		}
		targets[lower] = pi.Key
		exists, err := s.fs.Exists(pi.Path)
		if err != nil {
			return fmt.Errorf("check %s: %w", pi.Path, err)
		}
		pi.Exists = exists
		pi.Conflict = exists || len(pi.Existing) > 0
		plan.Summary.Files++
		plan.Summary.Bytes += pi.Size
		if pi.Conflict {
			plan.Summary.Conflicts++
		}
	}
	plan.Summary.EnoughSpace = plan.Summary.FreeBytes < 0 || plan.Summary.Bytes < plan.Summary.FreeBytes
	if !plan.Summary.EnoughSpace {
		plan.Warnings = append(plan.Warnings, Warning{Code: "not_enough_space"})
	}
	if plan.Summary.Conflicts > 0 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "conflicts", Params: map[string]any{"count": plan.Summary.Conflicts}})
	}
	return nil
}
