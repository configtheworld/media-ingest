package server

import (
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/configtheworld/media-digest/internal/auth"
	"github.com/configtheworld/media-digest/internal/config"
)

func TestRepeatedUserHeaderRejected(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.RemoteAddr = proxyIP + ":1"
	r.Header.Add("Remote-User", "alice")
	r.Header.Add("Remote-User", "admin")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("two Remote-User values must be rejected, got %d", w.Code)
	}
}

func TestCSRFInBasicMode(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	e := newEnv(t, config.AuthBasic, func(c *config.Config) { c.BasicUser, c.BasicHash = "admin", hash })
	e.user = ""
	body := `{"username":"admin","password":"correct horse"}`
	w := e.req("POST", "/api/auth/login", strings.NewReader(body), map[string]string{"Content-Type": "application/json", auth.CSRFHeader: ""})
	if w.Code != 403 {
		t.Fatalf("login without CSRF token: %d", w.Code)
	}
}

// Logs must never contain credentials, cookies, header values or query strings.
func TestLogsContainNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	e := newEnv(t, config.AuthProxy)
	opts := e.opts
	opts.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	r := httptest.NewRequest("GET", "/api/meta/tmdb/search?kind=movie&q=secret-title-query", nil)
	r.RemoteAddr = proxyIP + ":1"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("Authorization", "Bearer super-secret-token")
	r.AddCookie(&http.Cookie{Name: "authelia_session", Value: "secret-cookie-value"})
	h.ServeHTTP(httptest.NewRecorder(), r)
	bad := httptest.NewRequest("GET", "/api/session", nil)
	bad.RemoteAddr = "10.9.9.9:1"
	bad.Header.Set("Remote-User", "forged-user-name")
	h.ServeHTTP(httptest.NewRecorder(), bad)
	out := buf.String()
	if !strings.Contains(out, `"path":"/api/meta/tmdb/search"`) {
		t.Fatalf("request not logged: %s", out)
	}
	for _, secret := range []string{"super-secret-token", "secret-cookie-value", "secret-title-query", "forged-user-name"} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q:\n%s", secret, out)
		}
	}
}

// The app never executes external programs.
func TestNoShellExecution(t *testing.T) {
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for { // find the module root
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Skip("source tree not available (test binary run outside the repository)")
		}
		root = parent
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == "os/exec" || path == "syscall/exec" {
				t.Errorf("%s imports %s", p, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
