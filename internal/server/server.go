// Package server wires HTTP routes, middleware and handlers.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/media-ingest/media-ingest/internal/auth"
	"github.com/media-ingest/media-ingest/internal/config"
	"github.com/media-ingest/media-ingest/internal/ingest"
	"github.com/media-ingest/media-ingest/internal/provider"
	"github.com/media-ingest/media-ingest/internal/store"
)

type Server struct {
	cfg       *config.Config
	log       *slog.Logger
	store     *store.Store
	auth      *auth.Auth
	ingest    *ingest.Service
	providers map[string]provider.Provider
	keys      *KeyStore
	images    *imageCache
	static    *staticFiles
	version   string
	newTMDB   func(key string) provider.Provider // builds a client for key validation

	apiLimit  *ipLimiter
	authLimit *ipLimiter
}

type Options struct {
	Config    *config.Config
	Log       *slog.Logger
	Store     *store.Store
	Ingest    *ingest.Service
	Providers map[string]provider.Provider
	Keys      *KeyStore
	Static    fs.FS
	Version   string
	NewTMDB   func(key string) provider.Provider
}

func New(o Options) (*Server, error) {
	st, err := newStatic(o.Static)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: o.Config, log: o.Log, store: o.Store, auth: auth.New(o.Config, o.Store), ingest: o.Ingest,
		providers: o.Providers, keys: o.Keys, static: st, version: o.Version, newTMDB: o.NewTMDB,
		images:    newImageCache(path.Join(o.Config.DataDir, "cache", "img")),
		apiLimit:  newIPLimiter(rate.Limit(30), 300),
		authLimit: newIPLimiter(rate.Every(12*time.Second), 5),
	}, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/session", s.session)
	api.Handle("POST /api/auth/login", s.rateLimit(s.authLimit, http.HandlerFunc(s.login)))
	api.HandleFunc("POST /api/auth/logout", s.logout)
	api.Handle("POST /api/setup/tmdb", s.rateLimit(s.authLimit, s.authed(s.setupTMDB)))
	api.Handle("GET /api/meta/{provider}/search", s.authed(s.search))
	api.Handle("GET /api/meta/{provider}/movie/{id}", s.authed(s.movie))
	api.Handle("GET /api/meta/{provider}/tv/{id}", s.authed(s.show))
	api.Handle("GET /api/meta/{provider}/tv/{id}/season/{n}", s.authed(s.season))
	api.Handle("GET /api/img/{provider}/{size}/{file}", s.authed(s.image))
	api.Handle("GET /api/settings", s.authed(s.getSettings))
	api.Handle("PUT /api/settings", s.authed(s.putSettings))
	api.Handle("GET /api/parser/rules", s.authed(s.parserRules))
	api.Handle("GET /api/library/{provider}/tv/{id}", s.authed(s.tvLibrary))
	api.Handle("POST /api/plan/movie", s.authed(s.planMovie))
	api.Handle("POST /api/batches/movie", s.authed(s.createMovieBatch))
	api.Handle("POST /api/plan/tv", s.authed(s.planTV))
	api.Handle("POST /api/batches/tv", s.authed(s.createTVBatch))
	api.Handle("GET /api/browse", s.authed(s.browse))
	api.Handle("POST /api/plan/quick", s.authed(s.planQuick))
	api.Handle("POST /api/batches/quick", s.authed(s.createQuickBatch))
	api.Handle("GET /api/history", s.authed(s.history))
	api.Handle("POST /api/batches/{id}/undo", s.authed(s.undoBatch))
	api.Handle("GET /api/batches/{id}", s.authed(s.getBatch))
	api.Handle("POST /api/batches/{id}/cancel", s.authed(s.cancelBatch))
	api.Handle("POST /api/batches/{id}/items/{item}/resolve", s.authed(s.resolveItem))
	api.Handle("OPTIONS /api/uploads", s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Identity) { s.ingest.TusOptions(w, r) }))
	api.Handle("POST /api/uploads", s.authed(func(w http.ResponseWriter, r *http.Request, id auth.Identity) { s.ingest.TusCreate(w, r, id.User) }))
	api.Handle("HEAD /api/uploads/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		s.ingest.TusHead(w, r, id.User, r.PathValue("id"))
	}))
	api.Handle("PATCH /api/uploads/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		s.ingest.TusPatch(w, r, id.User, r.PathValue("id"))
	}))
	api.Handle("DELETE /api/uploads/{id}", s.authed(func(w http.ResponseWriter, r *http.Request, id auth.Identity) {
		s.ingest.TusDelete(w, r, id.User, r.PathValue("id"))
	}))
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found"})
	})
	mux.Handle("/api/", s.rateLimit(s.apiLimit, s.csrf(api)))

	// Static assets: in proxy mode they require authentication like
	// everything else (fail closed); in basic mode the login page needs them.
	var static http.Handler = s.static
	if s.cfg.AuthMode == config.AuthProxy {
		static = s.authed(func(w http.ResponseWriter, r *http.Request, _ auth.Identity) { s.static.ServeHTTP(w, r) })
	}
	mux.Handle("/", s.rateLimit(s.apiLimit, static))

	return s.logRequests(securityHeaders(mux))
}

type authedHandler func(http.ResponseWriter, *http.Request, auth.Identity)

// authed requires an authenticated identity.
func (s *Server) authed(h authedHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.auth.Authenticate(r)
		if err != nil {
			s.log.Debug("unauthenticated request", "path", r.URL.Path, "reason", err.Error(), "ip", s.auth.ClientIP(r))
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthenticated"})
			return
		}
		h(w, r.WithContext(auth.WithIdentity(r.Context(), id)), id)
	})
}

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.CheckCSRF(r) {
			writeJSON(w, http.StatusForbidden, apiError{Error: "csrf"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- JSON helpers ---

type apiError struct {
	Error  string         `json:"error"`
	Detail string         `json:"detail,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON strictly decodes a single JSON object into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, apiError{Error: "content_type"})
		return false
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_json", Detail: jsonErrDetail(err)})
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid_json", Detail: "trailing data"})
		return false
	}
	return true
}

func jsonErrDetail(err error) string {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return "body too large"
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// writeErr maps service errors to HTTP responses.
func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *ingest.ValidationError
	var rl *provider.RateLimitError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, apiError{Error: ve.Code, Detail: ve.Detail, Params: ve.Params})
	case errors.Is(err, provider.ErrNotFound), errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Error: "not_found"})
	case errors.Is(err, provider.ErrNotConfigured):
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "provider_not_configured"})
	case errors.Is(err, provider.ErrUnauthorized):
		writeJSON(w, http.StatusBadGateway, apiError{Error: "provider_unauthorized"})
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "provider_rate_limited", Params: map[string]any{"retryAfter": int(rl.RetryAfter.Seconds()) + 1}})
	case errors.Is(err, provider.ErrUnavailable):
		s.log.Warn("provider unavailable", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "provider_unavailable"})
	case errors.Is(err, context.Canceled):
		// client went away
	default:
		s.log.Error("internal error", "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal"})
	}
}

// --- static files ---

type staticFiles struct {
	fsys  fs.FS
	etags map[string]string
}

func newStatic(fsys fs.FS) (*staticFiles, error) {
	st := &staticFiles{fsys: fsys, etags: map[string]string{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		st.etags[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return st, err
}

func (st *staticFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	etag, ok := st.etags[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if ct := mime.TypeByExtension(path.Ext(p)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if path.Ext(p) == ".webmanifest" {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	b, _ := fs.ReadFile(st.fsys, p)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b)
}
