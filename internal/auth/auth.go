// Package auth authenticates requests (proxy header, basic login or none),
// enforces CSRF protection and derives the client IP.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/media-ingest/media-ingest/internal/config"
	"github.com/media-ingest/media-ingest/internal/store"
)

const (
	SessionCookie = "mi_session"
	CSRFCookie    = "mi_csrf"
	CSRFHeader    = "X-CSRF-Token"
	sessionTTL    = 7 * 24 * time.Hour
)

// Identity is the authenticated user.
type Identity struct {
	User  string `json:"user"`
	Admin bool   `json:"admin"`
}

type ctxKey struct{}

// FromContext returns the identity set by Middleware.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// WithIdentity returns a context carrying id (used by tests).
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// Auth implements authentication for the configured mode.
type Auth struct {
	cfg   *config.Config
	store *store.Store
}

func New(cfg *config.Config, st *store.Store) *Auth { return &Auth{cfg: cfg, store: st} }

// Errors returned by Authenticate.
var (
	ErrUntrustedProxy = errors.New("request did not come from a trusted proxy")
	ErrNoUser         = errors.New("no authenticated user")
)

// Authenticate resolves the identity of r. It fails closed.
func (a *Auth) Authenticate(r *http.Request) (Identity, error) {
	switch a.cfg.AuthMode {
	case config.AuthProxy:
		ip, ok := remoteIP(r)
		if !ok || !a.trusted(ip) {
			return Identity{}, ErrUntrustedProxy
		}
		vals := r.Header.Values(a.cfg.AuthHeader)
		if len(vals) != 1 {
			return Identity{}, ErrNoUser
		}
		user := strings.TrimSpace(vals[0])
		if !validUser(user) {
			return Identity{}, ErrNoUser
		}
		return Identity{User: user, Admin: a.proxyAdmin(user, r.Header.Get(a.cfg.GroupsHeader))}, nil
	case config.AuthBasic:
		c, err := r.Cookie(SessionCookie)
		if err != nil || len(c.Value) != 64 {
			return Identity{}, ErrNoUser
		}
		sess, err := a.store.GetSession(r.Context(), c.Value)
		if err != nil {
			return Identity{}, ErrNoUser
		}
		return Identity{User: sess.User, Admin: true}, nil
	case config.AuthNone:
		return Identity{User: "anonymous", Admin: true}, nil
	}
	return Identity{}, ErrNoUser
}

func (a *Auth) proxyAdmin(user, groups string) bool {
	if len(a.cfg.AdminUsers) > 0 {
		return slices.Contains(a.cfg.AdminUsers, user)
	}
	if a.cfg.AdminGroup != "" {
		for _, g := range strings.Split(groups, ",") {
			if strings.TrimSpace(g) == a.cfg.AdminGroup {
				return true
			}
		}
		return false
	}
	return true // single-user default: every authenticated user is admin
}

func (a *Auth) trusted(ip netip.Addr) bool {
	for _, p := range a.cfg.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func validUser(u string) bool {
	if u == "" || len(u) > 128 {
		return false
	}
	for _, r := range u {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func remoteIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// ClientIP returns the originating client IP. X-Forwarded-For is honoured
// only when the direct peer is a trusted proxy; the right-most untrusted
// address is used, so clients cannot spoof it.
func (a *Auth) ClientIP(r *http.Request) string {
	ip, ok := remoteIP(r)
	if !ok {
		return r.RemoteAddr
	}
	if !a.trusted(ip) {
		return ip.String()
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		hop = hop.Unmap()
		if !a.trusted(hop) {
			return hop.String()
		}
	}
	return ip.String()
}

// Login verifies basic credentials and creates a session.
func (a *Auth) Login(ctx context.Context, user, password string) (*store.Session, bool, error) {
	// Always run argon2 so timing does not reveal whether the user exists.
	okPass := VerifyPassword(a.cfg.BasicHash, password)
	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(a.cfg.BasicUser)) == 1
	if !okPass || !okUser {
		return nil, false, nil
	}
	sess, err := a.store.CreateSession(ctx, a.cfg.BasicUser, sessionTTL)
	return sess, err == nil, err
}

// SessionCookie builds the session cookie.
func (a *Auth) SessionCookie(value string, expires time.Time) *http.Cookie {
	maxAge := int(time.Until(expires).Seconds())
	if value == "" {
		maxAge = -1
	}
	return &http.Cookie{Name: SessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: a.cfg.CookieSecure, SameSite: http.SameSiteLaxMode}
}

// EnsureCSRFCookie returns the request's CSRF token, setting a new cookie if needed.
func (a *Auth) EnsureCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(CSRFCookie); err == nil && validToken(c.Value) {
		return c.Value
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	tok := hex.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: tok, Path: "/", HttpOnly: false,
		Secure: a.cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
	return tok
}

func validToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// CheckCSRF validates state-changing requests: the X-CSRF-Token header must
// match the CSRF cookie (double submit; a custom header also forces a CORS
// preflight we never answer), and the browser's Fetch Metadata / Origin must
// not indicate a cross-site request.
func CheckCSRF(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	c, err := r.Cookie(CSRFCookie)
	if err != nil || !validToken(c.Value) {
		return false
	}
	h := r.Header.Get(CSRFHeader)
	return subtle.ConstantTimeCompare([]byte(h), []byte(c.Value)) == 1
}
