// Package ingest plans, uploads and publishes media into the library.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/media-ingest/media-ingest/internal/config"
	"github.com/media-ingest/media-ingest/internal/jellyfin"
	"github.com/media-ingest/media-ingest/internal/mediafs"
	"github.com/media-ingest/media-ingest/internal/naming"
	"github.com/media-ingest/media-ingest/internal/provider"
	"github.com/media-ingest/media-ingest/internal/store"
)

// Service is the ingest engine shared by the HTTP handlers.
type Service struct {
	cfg       *config.Config
	fs        *mediafs.FS
	store     *store.Store
	providers map[string]provider.Provider
	log       *slog.Logger
	locks     uploadLocks
	jf        *jellyfin.Client // nil when the hook is disabled
	hooks     sync.WaitGroup
}

func New(cfg *config.Config, fs *mediafs.FS, st *store.Store, providers map[string]provider.Provider, log *slog.Logger) *Service {
	return &Service{cfg: cfg, fs: fs, store: st, providers: providers, log: log,
		jf: jellyfin.New(cfg.JellyfinURL, cfg.JellyfinAPIKey, cfg.JellyfinPathPrefix)}
}

// MediaRootOK reports whether MEDIA_ROOT is accessible.
func (s *Service) MediaRootOK() error { return s.fs.Healthy() }

// Provider returns a provider by name.
func (s *Service) Provider(name string) (provider.Provider, bool) {
	p, ok := s.providers[name]
	return p, ok
}

// ValidationError is a client error with a stable code for the UI.
type ValidationError struct {
	Code   string         `json:"code"`
	Detail string         `json:"detail,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

func (e *ValidationError) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

func invalid(code, detail string) error { return &ValidationError{Code: code, Detail: detail} }

// --- naming settings ---

const namingKey = "naming"

// NamingSettings are chosen in the UI and stored in the database.
type NamingSettings struct {
	Preset        string `json:"preset"`
	MovieTemplate string `json:"movieTemplate,omitempty"` // empty = preset
	TVTemplate    string `json:"tvTemplate,omitempty"`    // empty = preset
}

// Naming is the resolved naming configuration.
type Naming struct {
	Settings  NamingSettings
	Movie, TV *naming.Template
	ExtraDirs map[string]string
}

// GetNaming loads naming settings, falling back to the default preset.
func (s *Service) GetNaming(ctx context.Context) (*Naming, error) {
	ns := NamingSettings{Preset: naming.DefaultPreset}
	if raw, ok, err := s.store.GetSetting(ctx, namingKey); err != nil {
		return nil, err
	} else if ok {
		if err := json.Unmarshal([]byte(raw), &ns); err != nil {
			s.log.Warn("ignoring invalid naming settings", "err", err)
			ns = NamingSettings{Preset: naming.DefaultPreset}
		}
	}
	n, err := resolveNaming(ns)
	if err != nil {
		s.log.Warn("stored naming templates invalid, using default preset", "err", err)
		return resolveNaming(NamingSettings{Preset: naming.DefaultPreset})
	}
	return n, nil
}

func resolveNaming(ns NamingSettings) (*Naming, error) {
	p, ok := naming.Presets[ns.Preset]
	if !ok {
		return nil, invalid("unknown_preset", ns.Preset)
	}
	movieSrc, tvSrc := p.Movie, p.TV
	if ns.MovieTemplate != "" {
		movieSrc = ns.MovieTemplate
	}
	if ns.TVTemplate != "" {
		tvSrc = ns.TVTemplate
	}
	mt, err := naming.ParseTemplate(movieSrc, naming.MovieVars)
	if err != nil {
		return nil, invalid("invalid_movie_template", err.Error())
	}
	tt, err := naming.ParseTemplate(tvSrc, naming.TVVars)
	if err != nil {
		return nil, invalid("invalid_tv_template", err.Error())
	}
	return &Naming{Settings: ns, Movie: mt, TV: tt, ExtraDirs: p.ExtraDirs}, nil
}

// SetNaming validates and stores naming settings.
func (s *Service) SetNaming(ctx context.Context, ns NamingSettings) (*Naming, error) {
	ns.MovieTemplate = strings.TrimSpace(ns.MovieTemplate)
	ns.TVTemplate = strings.TrimSpace(ns.TVTemplate)
	n, err := resolveNaming(ns)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(ns)
	return n, s.store.SetSetting(ctx, namingKey, string(raw))
}

// --- library folder discovery ---

var providerIDRe = regexp.MustCompile(`(?i)[\[{](tmdbid|tmdb)[-=]([0-9]+)[\]}]`)

// libraryFolder is an existing top-level folder in a library.
type libraryFolder struct {
	Name       string
	ProviderID string // tmdb id parsed from the name, if any
	normTitle  string
	year       int
}

func (s *Service) listLibrary(dir string) ([]libraryFolder, error) {
	entries, err := s.fs.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // library folder not created yet
	}
	if err != nil {
		return nil, err
	}
	var out []libraryFolder
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") { // IsDir is false for symlinks
			continue
		}
		f := libraryFolder{Name: e.Name()}
		if m := providerIDRe.FindStringSubmatch(e.Name()); m != nil {
			f.ProviderID = strings.TrimLeft(m[2], "0")
		}
		f.normTitle, f.year = folderTitle(e.Name())
		out = append(out, f)
	}
	return out, nil
}

var (
	tagRe  = regexp.MustCompile(`[\[{][^\]}]*[\]}]`)
	yearRe = regexp.MustCompile(`\((\d{4})\)`)
)

// folderTitle extracts a normalized title and year from "Title (2010) [tmdbid-1]".
func folderTitle(name string) (string, int) {
	name = tagRe.ReplaceAllString(name, " ")
	year := 0
	if m := yearRe.FindStringSubmatchIndex(name); m != nil {
		fmt.Sscanf(name[m[2]:m[3]], "%d", &year)
		name = name[:m[0]] + name[m[1]:]
	}
	return normTitle(name), year
}

// normTitle lowercases, strips accents and punctuation for fuzzy matching.
func normTitle(s string) string {
	s = norm.NFKD.String(strings.ToLower(s))
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}
