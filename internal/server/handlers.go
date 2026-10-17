package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/media-ingest/media-ingest/internal/auth"
	"github.com/media-ingest/media-ingest/internal/config"
	"github.com/media-ingest/media-ingest/internal/ingest"
	"github.com/media-ingest/media-ingest/internal/naming"
	"github.com/media-ingest/media-ingest/internal/provider"
)

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db_unavailable"})
		return
	}
	if err := s.ingest.MediaRootOK(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "media_root_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type providerInfo struct {
	Name        string               `json:"name"`
	Attribution provider.Attribution `json:"attribution"`
}

type sessionResponse struct {
	User         string            `json:"user"`
	Admin        bool              `json:"admin"`
	AuthMode     string            `json:"authMode"`
	CSRFToken    string            `json:"csrfToken"`
	Version      string            `json:"version"`
	NeedsSetup   bool              `json:"needsSetup"`
	KeyFromEnv   bool              `json:"keyFromEnv"`
	Providers    []providerInfo    `json:"providers"`
	MaxUpload    int64             `json:"maxUploadSize"`
	VideoExts    []string          `json:"videoExtensions"`
	SubtitleExts []string          `json:"subtitleExtensions"`
	ExtraTypes   []string          `json:"extraTypes"`
	Libraries    map[string]string `json:"libraries"`
	Jellyfin     bool              `json:"jellyfin"`
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	tok := s.auth.EnsureCSRFCookie(w, r)
	id, err := s.auth.Authenticate(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated", "authMode": s.cfg.AuthMode, "csrfToken": tok})
		return
	}
	resp := sessionResponse{User: id.User, Admin: id.Admin, AuthMode: s.cfg.AuthMode, CSRFToken: tok, Version: s.version,
		NeedsSetup: s.keys.Get() == "", KeyFromEnv: s.keys.FromEnv(), MaxUpload: s.cfg.MaxUploadSize,
		ExtraTypes: naming.ExtraTypes, Libraries: map[string]string{"movies": s.cfg.MoviesDir, "tv": s.cfg.TVDir},
		Jellyfin: s.cfg.JellyfinURL != ""}
	for _, e := range config.DefaultVideoExts {
		if s.cfg.AllowedExts[e] {
			resp.VideoExts = append(resp.VideoExts, e)
		}
	}
	for _, e := range config.DefaultSubtitleExts {
		if s.cfg.AllowedExts[e] {
			resp.SubtitleExts = append(resp.SubtitleExts, e)
		}
	}
	for name, p := range s.providers {
		resp.Providers = append(resp.Providers, providerInfo{Name: name, Attribution: p.Attribution()})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AuthMode != config.AuthBasic {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found"})
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Username) > 128 || len(req.Password) > 1024 {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid_credentials"})
		return
	}
	sess, ok, err := s.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !ok {
		s.log.Warn("failed login", "ip", s.auth.ClientIP(r)) // never log the attempted username/password
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid_credentials"})
		return
	}
	http.SetCookie(w, s.auth.SessionCookie(sess.ID, sess.ExpiresAt))
	s.log.Info("login", "user", sess.User, "ip", s.auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, s.auth.SessionCookie("", time.Time{}))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

var apiKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]{16,1000}$`)

// setupTMDB stores the TMDB API key once, when none is configured.
func (s *Server) setupTMDB(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	if !id.Admin {
		writeJSON(w, http.StatusForbidden, apiError{Error: "admin_only"})
		return
	}
	if s.keys.Get() != "" {
		writeJSON(w, http.StatusConflict, apiError{Error: "already_configured"})
		return
	}
	var req struct {
		APIKey string `json:"apiKey"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.APIKey)
	if !apiKeyRe.MatchString(key) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_api_key"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.newTMDB(key).Check(ctx); err != nil {
		if errors.Is(err, provider.ErrUnauthorized) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_api_key"})
			return
		}
		s.writeErr(w, r, err)
		return
	}
	if err := s.keys.Set(key); err != nil {
		s.log.Error("store api key", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal"})
		return
	}
	s.log.Info("TMDB API key configured via setup page", "user", id.User)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) providerFor(w http.ResponseWriter, r *http.Request) (provider.Provider, bool) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok {
		writeJSON(w, http.StatusNotFound, apiError{Error: "unknown_provider"})
	}
	return p, ok
}

func langParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "en-US"
	}
	if !ingest.ValidLanguage(lang) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_language"})
		return "", false
	}
	return lang, true
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	p, ok := s.providerFor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	if query == "" || len(query) > 200 || !utf8.ValidString(query) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_query"})
		return
	}
	kind := provider.Kind(q.Get("kind"))
	if kind != provider.Movie && kind != provider.TV {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_kind"})
		return
	}
	page := 1
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_page"})
			return
		}
		page = n
	}
	lang, ok := langParam(w, r)
	if !ok {
		return
	}
	res, err := p.Search(r.Context(), kind, query, lang, page)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) movie(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	p, ok := s.providerFor(w, r)
	if !ok {
		return
	}
	lang, ok := langParam(w, r)
	if !ok {
		return
	}
	m, err := p.Movie(r.Context(), r.PathValue("id"), lang)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) show(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	p, ok := s.providerFor(w, r)
	if !ok {
		return
	}
	lang, ok := langParam(w, r)
	if !ok {
		return
	}
	m, err := p.Show(r.Context(), r.PathValue("id"), lang)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) season(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	p, ok := s.providerFor(w, r)
	if !ok {
		return
	}
	lang, ok := langParam(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n > 1000 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_season"})
		return
	}
	m, err := p.Season(r.Context(), r.PathValue("id"), n, lang)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) image(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	p, ok := s.providerFor(w, r)
	if !ok {
		return
	}
	s.images.serve(w, r, p, r.PathValue("size"), "/"+r.PathValue("file"), s.log)
}

type packInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

type settingsResponse struct {
	Naming  ingest.NamingSettings    `json:"naming"`
	Presets map[string]naming.Preset `json:"presets"`
	Parser  ingest.ParserSettings    `json:"parser"`
	Packs   []packInfo               `json:"packs"`
	// Server-side settings shown read-only in the UI.
	Jellyfin       bool `json:"jellyfin"`
	TrashRetention int  `json:"trashRetentionDays"`
}

func (s *Server) settingsView(r *http.Request) (*settingsResponse, error) {
	n, err := s.ingest.GetNaming(r.Context())
	if err != nil {
		return nil, err
	}
	ps, packs, err := s.ingest.GetParserSettings(r.Context())
	if err != nil {
		return nil, err
	}
	out := &settingsResponse{Naming: n.Settings, Presets: naming.Presets, Parser: ps, Packs: []packInfo{},
		Jellyfin: s.cfg.JellyfinURL != "", TrashRetention: int(s.cfg.TrashRetention.Hours() / 24)}
	for _, p := range packs {
		out.Packs = append(out.Packs, packInfo{ID: p.ID, Name: p.Name, Description: p.Description, Source: p.Source})
	}
	return out, nil
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	v, err := s.settingsView(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	if !id.Admin {
		writeJSON(w, http.StatusForbidden, apiError{Error: "admin_only"})
		return
	}
	var req struct {
		Naming *ingest.NamingSettings `json:"naming"`
		Parser *ingest.ParserSettings `json:"parser"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Naming != nil {
		if _, err := s.ingest.SetNaming(r.Context(), *req.Naming); err != nil {
			s.writeErr(w, r, err)
			return
		}
		s.log.Info("naming settings changed", "user", id.User, "preset", req.Naming.Preset)
	}
	if req.Parser != nil {
		if err := s.ingest.SetParserSettings(r.Context(), *req.Parser); err != nil {
			s.writeErr(w, r, err)
			return
		}
		s.log.Info("parser settings changed", "user", id.User, "packs", strings.Join(req.Parser.Packs, ","))
	}
	s.getSettings(w, r, id)
}

func (s *Server) planMovie(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	var req ingest.MovieRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	plan, err := s.ingest.PlanMovie(r.Context(), &req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) createMovieBatch(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var req ingest.MovieRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := s.ingest.CreateMovieBatch(r.Context(), id.User, &req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) getBatch(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	b, err := s.ingest.GetBatch(r.Context(), id.User, r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) cancelBatch(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	b, err := s.ingest.Cancel(r.Context(), id.User, r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) resolveItem(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var req struct {
		Policy string `json:"policy"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := s.ingest.Resolve(r.Context(), id.User, r.PathValue("id"), r.PathValue("item"), req.Policy)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) parserRules(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	rules, err := s.ingest.EffectiveRules(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) tvLibrary(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	lang, ok := langParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	choice := ingest.FolderChoice{Mode: q.Get("mode"), Name: q.Get("folder")}
	if len(choice.Name) > 1024 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_folder"})
		return
	}
	v, err := s.ingest.TVLibrary(r.Context(), r.PathValue("provider"), r.PathValue("id"), lang, choice)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) planTV(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	var req ingest.TVRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	plan, err := s.ingest.PlanTV(r.Context(), &req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) createTVBatch(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var req ingest.TVRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := s.ingest.CreateTVBatch(r.Context(), id.User, &req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	v, err := s.ingest.Browse(r.URL.Query().Get("path"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) planQuick(w http.ResponseWriter, r *http.Request, _ auth.Identity) {
	var req ingest.QuickRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	plan, err := s.ingest.PlanQuick(r.Context(), &req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) createQuickBatch(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	var req ingest.QuickRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := s.ingest.CreateQuickBatch(r.Context(), id.User, &req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 || offset > 1_000_000 {
		offset = 0
	}
	h, err := s.ingest.History(r.Context(), id.User, 50, offset)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": h, "offset": offset, "limit": 50})
}

func (s *Server) undoBatch(w http.ResponseWriter, r *http.Request, id auth.Identity) {
	b, err := s.ingest.Undo(r.Context(), id.User, r.PathValue("id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("batch undone", "batch", b.ID, "user", id.User)
	writeJSON(w, http.StatusOK, b)
}
