package server

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying connection.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests writes one JSON log line per request. Headers, cookies and
// query strings are never logged.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "err", rec, "stack", string(debug.Stack()))
				if sw.status == 0 {
					http.Error(sw, "internal error", http.StatusInternalServerError)
				}
			}
			level := slog.LevelInfo
			if r.URL.Path == "/healthz" || r.Method == http.MethodPatch || r.Method == http.MethodHead ||
				strings.HasPrefix(r.URL.Path, "/api/img/") {
				level = slog.LevelDebug
			}
			s.log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"bytes", sw.bytes, "dur_ms", time.Since(start).Milliseconds(), "ip", s.auth.ClientIP(r))
		}()
		next.ServeHTTP(sw, r)
	})
}

const csp = "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; " +
	"font-src 'self'; manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// ipLimiter is a per-client-IP token bucket.
type ipLimiter struct {
	mu    sync.Mutex
	m     map[string]*limEntry
	limit rate.Limit
	burst int
}

type limEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(limit rate.Limit, burst int) *ipLimiter {
	return &ipLimiter{m: map[string]*limEntry{}, limit: limit, burst: burst}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip]
	if !ok {
		if len(l.m) > 10000 {
			l.gc(time.Now().Add(-10 * time.Minute))
		}
		e = &limEntry{lim: rate.NewLimiter(l.limit, l.burst)}
		l.m[ip] = e
	}
	e.seen = time.Now()
	return e.lim.Allow()
}

func (l *ipLimiter) gc(before time.Time) {
	for k, e := range l.m {
		if e.seen.Before(before) {
			delete(l.m, k)
		}
	}
}

func (s *Server) rateLimit(l *ipLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(s.auth.ClientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, apiError{Error: "rate_limited"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
