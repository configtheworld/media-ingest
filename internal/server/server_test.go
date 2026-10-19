package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/configtheworld/media-digest/internal/auth"
	"github.com/configtheworld/media-digest/internal/config"
	"github.com/configtheworld/media-digest/internal/ingest"
	"github.com/configtheworld/media-digest/internal/mediafs"
	"github.com/configtheworld/media-digest/internal/provider"
	"github.com/configtheworld/media-digest/internal/provider/fake"
	"github.com/configtheworld/media-digest/internal/store"
)

const proxyIP = "192.0.2.10"

type env struct {
	t     *testing.T
	h     http.Handler
	cfg   *config.Config
	media string
	data  string
	csrf  string
	user  string
	keys  *KeyStore
	fake  *fake.Provider
	svc   *ingest.Service
	opts  Options
}

func newEnv(t *testing.T, mode string, mutate ...func(*config.Config)) *env {
	t.Helper()
	media, data := t.TempDir(), t.TempDir()
	cfg := &config.Config{
		DataDir: data, MediaRoot: media, TVDir: "tv", MoviesDir: "movies", StagingDir: ".incoming", TrashDir: ".trash",
		PUID: -1, PGID: -1, FileMode: 0o644, DirMode: 0o755,
		AuthMode: mode, AuthHeader: "Remote-User", GroupsHeader: "Remote-Groups",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix(proxyIP + "/32")},
		MaxUploadSize:  1 << 30, UploadExpiry: 3600e9,
		AllowedExts: map[string]bool{"mkv": true, "mp4": true, "srt": true},
	}
	for _, m := range mutate {
		m(cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(data, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mfs, err := mediafs.Open(media, mediafs.Perms{UID: -1, GID: -1, FileMode: cfg.FileMode, DirMode: cfg.DirMode}, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mfs.Close() })
	os.Mkdir(filepath.Join(media, ".incoming"), 0o755)
	fp := fake.New()
	providers := map[string]provider.Provider{"tmdb": fp}
	keys, _ := LoadKeyStore("envkey", data, log)
	svc := ingest.New(cfg, mfs, st, providers, log)
	opts := Options{Config: cfg, Log: log, Store: st, Ingest: svc, Providers: providers, Keys: keys,
		Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")},
			"manifest.webmanifest": {Data: []byte("{}")}, "icons/icon.svg": {Data: []byte("<svg/>")}}, Version: "test",
		NewTMDB: func(string) provider.Provider { return fp }}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.WaitHooks)
	return &env{t: t, h: srv.Handler(), cfg: cfg, media: media, data: data, csrf: strings.Repeat("ab", 32), user: "alice", keys: keys, fake: fp, svc: svc, opts: opts}
}

func (e *env) req(method, url string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, url, body)
	r.RemoteAddr = proxyIP + ":40000"
	if e.user != "" {
		r.Header.Set("Remote-User", e.user)
	}
	r.AddCookie(&http.Cookie{Name: auth.CSRFCookie, Value: e.csrf})
	r.Header.Set(auth.CSRFHeader, e.csrf)
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) json(method, url string, v any, out any) int {
	e.t.Helper()
	b, _ := json.Marshal(v)
	w := e.req(method, url, bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if out != nil {
		json.Unmarshal(w.Body.Bytes(), out)
	}
	return w.Code
}

func TestProxyAuthFailsClosed(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	// Trusted proxy, header present.
	if w := e.req("GET", "/api/session", nil, nil); w.Code != 200 {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	// Header missing.
	if w := e.req("GET", "/api/session", nil, map[string]string{"Remote-User": ""}); w.Code != 401 {
		t.Fatalf("missing header: %d", w.Code)
	}
	// Static files are protected too in proxy mode.
	if w := e.req("GET", "/", nil, map[string]string{"Remote-User": ""}); w.Code != 401 {
		t.Fatalf("static without user: %d", w.Code)
	}
	// Untrusted peer with a forged header.
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("Remote-User", "admin")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("untrusted peer: %d", w.Code)
	}
	// The web app manifest and icons are public (browsers fetch them without cookies).
	for _, p := range []string{"/manifest.webmanifest", "/icons/icon.svg"} {
		if w := e.req("GET", p, nil, map[string]string{"Remote-User": ""}); w.Code != 200 {
			t.Fatalf("%s without user: %d", p, w.Code)
		}
	}
	// Healthz is public.
	r = httptest.NewRequest("GET", "/healthz", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("healthz: %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	w := e.req("GET", "/api/session", nil, nil)
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if w.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	body := `{"naming":{"preset":"plex"}}`
	cases := map[string]map[string]string{
		"no header":      {auth.CSRFHeader: ""},
		"wrong header":   {auth.CSRFHeader: strings.Repeat("cd", 32)},
		"cross-site":     {"Sec-Fetch-Site": "cross-site"},
		"foreign origin": {"Origin": "https://evil.example"},
	}
	for name, hdr := range cases {
		hdr["Content-Type"] = "application/json"
		if w := e.req("PUT", "/api/settings", strings.NewReader(body), hdr); w.Code != 403 {
			t.Errorf("%s: got %d", name, w.Code)
		}
	}
	if w := e.req("PUT", "/api/settings", strings.NewReader(body), map[string]string{"Content-Type": "application/json", "Origin": "http://example.com"}); w.Code != 200 {
		t.Errorf("same origin: got %d %s", w.Code, w.Body)
	}
}

func TestStrictJSON(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	for _, body := range []string{`{"naming":{"preset":"plex"},"extra":1}`, `{"naming":{}} {}`, `[1]`} {
		w := e.req("PUT", "/api/settings", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
		if w.Code != 400 {
			t.Errorf("%s: got %d", body, w.Code)
		}
	}
	w := e.req("PUT", "/api/settings", strings.NewReader(`{}`), map[string]string{"Content-Type": "text/plain"})
	if w.Code != 415 {
		t.Errorf("content type: %d", w.Code)
	}
}

func movieReq(items ...map[string]any) map[string]any {
	return map[string]any{"provider": "tmdb", "id": "27205", "language": "en-US", "folder": map[string]any{"mode": "auto"}, "items": items}
}

func movieItem(key, name string, size int) map[string]any {
	return map[string]any{"key": key, "name": name, "size": size, "role": "movie"}
}

// upload runs the tus protocol for one item in two chunks.
func (e *env) upload(batchID, itemID string, data []byte) {
	e.t.Helper()
	meta := "batch " + base64.StdEncoding.EncodeToString([]byte(batchID)) + ",item " + base64.StdEncoding.EncodeToString([]byte(itemID)) +
		",filename " + base64.StdEncoding.EncodeToString([]byte("whatever.mkv"))
	w := e.req("POST", "/api/uploads", nil, map[string]string{"Tus-Resumable": "1.0.0", "Upload-Length": strconv.Itoa(len(data)), "Upload-Metadata": meta})
	if w.Code != 201 {
		e.t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	loc := w.Header().Get("Location")
	half := len(data) / 2
	for _, part := range []struct {
		off  int
		data []byte
	}{{0, data[:half]}, {half, data[half:]}} {
		w = e.req("PATCH", loc, bytes.NewReader(part.data), map[string]string{"Tus-Resumable": "1.0.0", "Upload-Offset": strconv.Itoa(part.off),
			"Content-Type": "application/offset+octet-stream"})
		if w.Code != 204 {
			e.t.Fatalf("patch@%d: %d %s", part.off, w.Code, w.Body)
		}
		if part.off == 0 {
			h := e.req("HEAD", loc, nil, map[string]string{"Tus-Resumable": "1.0.0"})
			if h.Header().Get("Upload-Offset") != strconv.Itoa(half) {
				e.t.Fatalf("HEAD offset %q", h.Header().Get("Upload-Offset"))
			}
		}
	}
}

func TestMovieFlowEndToEnd(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	req := movieReq(movieItem("m1", "Inception.2010.1080p.BluRay.mkv", 10),
		map[string]any{"key": "s1", "name": "inception.en.forced.srt", "size": 4, "role": "subtitle", "lang": "en", "forced": true},
		map[string]any{"key": "x1", "name": "trailer.mp4", "size": 6, "role": "extra", "extraType": "trailer", "label": "Official Trailer"})

	var plan ingest.Plan
	if code := e.json("POST", "/api/plan/movie", req, &plan); code != 200 {
		t.Fatalf("plan: %d", code)
	}
	want := map[string]string{
		"m1": "movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv",
		"s1": "movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].en.forced.srt",
		"x1": "movies/Inception (2010) [tmdbid-27205]/Trailers/Official Trailer.mp4",
	}
	for _, it := range plan.Items {
		if it.Path != want[it.Key] {
			t.Errorf("%s: %q want %q", it.Key, it.Path, want[it.Key])
		}
	}
	// Dry run must not touch the library.
	if _, err := os.Stat(filepath.Join(e.media, "movies")); !os.IsNotExist(err) {
		t.Fatal("plan created directories")
	}

	var batch ingest.BatchView
	if code := e.json("POST", "/api/batches/movie", req, &batch); code != 201 {
		t.Fatalf("batch: %d", code)
	}
	content := map[string][]byte{"m1": []byte("0123456789"), "s1": []byte("subs"), "x1": []byte("traile")}
	for _, it := range batch.Items {
		e.upload(batch.ID, it.ID, content[it.Key])
	}
	var got ingest.BatchView
	e.json("GET", "/api/batches/"+batch.ID, nil, &got)
	if got.Status != "done" {
		t.Fatalf("batch status %q: %+v", got.Status, got.Items)
	}
	for _, it := range got.Items {
		if it.Status != "published" || it.FinalPath != want[it.Key] {
			t.Errorf("%s: %s %s", it.Key, it.Status, it.FinalPath)
		}
		b, err := os.ReadFile(filepath.Join(e.media, it.FinalPath))
		if err != nil || !bytes.Equal(b, content[it.Key]) {
			t.Errorf("%s content %q err %v", it.Key, b, err)
		}
		fi, _ := os.Stat(filepath.Join(e.media, it.FinalPath))
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %v", it.Key, fi.Mode().Perm())
		}
	}
	di, _ := os.Stat(filepath.Join(e.media, "movies/Inception (2010) [tmdbid-27205]"))
	if di.Mode().Perm() != 0o755 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	if left, _ := os.ReadDir(filepath.Join(e.media, ".incoming")); len(left) != 0 {
		t.Errorf("staging not empty: %v", left)
	}

	// Second ingest of the same movie: conflict must be decided explicitly.
	req2 := movieReq(movieItem("m1", "other.mkv", 3))
	e.json("POST", "/api/plan/movie", req2, &plan)
	if plan.Summary.Conflicts != 1 || plan.FolderSource != "new" || !plan.FolderExists {
		t.Fatalf("plan2: %+v", plan)
	}
	var apiErr apiError
	if code := e.json("POST", "/api/batches/movie", req2, &apiErr); code != 400 || apiErr.Error != "conflict_policy_required" {
		t.Fatalf("missing policy: %d %+v", code, apiErr)
	}
	req2["items"].([]map[string]any)[0]["policy"] = "keep_both"
	e.json("POST", "/api/batches/movie", req2, &batch)
	e.upload(batch.ID, batch.Items[0].ID, []byte("new"))
	e.json("GET", "/api/batches/"+batch.ID, nil, &got)
	if p := got.Items[0].FinalPath; p != "movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205] - Version 2.mkv" {
		t.Fatalf("keep both path %q", p)
	}
	if b, _ := os.ReadFile(filepath.Join(e.media, want["m1"])); string(b) != "0123456789" {
		t.Fatal("original overwritten")
	}

	// Replace moves the old file to trash.
	req2["items"].([]map[string]any)[0]["policy"] = "replace"
	e.json("POST", "/api/batches/movie", req2, &batch)
	e.upload(batch.ID, batch.Items[0].ID, []byte("rep"))
	e.json("GET", "/api/batches/"+batch.ID, nil, &got)
	if b, _ := os.ReadFile(filepath.Join(e.media, want["m1"])); string(b) != "rep" {
		t.Fatal("not replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(e.media, got.Items[0].TrashedPath)); string(b) != "0123456789" {
		t.Fatalf("trash %q", got.Items[0].TrashedPath)
	}
}

func TestPublishConflictAppearingLater(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	var batch ingest.BatchView
	e.json("POST", "/api/batches/movie", movieReq(movieItem("m1", "a.mkv", 2)), &batch)
	// Someone else creates the file between planning and publishing.
	dst := filepath.Join(e.media, batch.Items[0].Target)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.WriteFile(dst, []byte("theirs"), 0o644)
	e.upload(batch.ID, batch.Items[0].ID, []byte("ok"))
	var got ingest.BatchView
	e.json("GET", "/api/batches/"+batch.ID, nil, &got)
	if got.Items[0].Status != "conflict" {
		t.Fatalf("status %q", got.Items[0].Status)
	}
	if b, _ := os.ReadFile(dst); string(b) != "theirs" {
		t.Fatal("overwrote a file that appeared after planning")
	}
	if code := e.json("POST", "/api/batches/"+batch.ID+"/items/"+got.Items[0].ID+"/resolve", map[string]string{"policy": "keep_both"}, &got); code != 200 {
		t.Fatalf("resolve: %d", code)
	}
	if got.Items[0].Status != "published" || !strings.HasSuffix(got.Items[0].FinalPath, " - Version 2.mkv") {
		t.Fatalf("%+v", got.Items[0])
	}
}

func TestHostileInputs(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	cases := map[string]struct {
		req  map[string]any
		code string
	}{
		"junk":         {movieReq(movieItem("m1", "._Inception.mkv", 5)), "junk_file"},
		"ds_store":     {movieReq(movieItem("m1", ".DS_Store", 5)), "junk_file"},
		"extension":    {movieReq(movieItem("m1", "evil.exe", 5)), "extension_not_allowed"},
		"no extension": {movieReq(movieItem("m1", "noext", 5)), "extension_not_allowed"},
		"zero size":    {movieReq(movieItem("m1", "a.mkv", 0)), "invalid_size"},
		"too big":      {movieReq(movieItem("m1", "a.mkv", 2<<30)), "invalid_size"},
		"bad key":      {movieReq(movieItem("../x", "a.mkv", 5)), "invalid_key"},
		"null byte":    {movieReq(movieItem("m1", "a\x00.mkv", 5)), "invalid_name"},
		"sub as movie": {movieReq(movieItem("m1", "a.srt", 5)), "role_extension_mismatch"},
		"bad role":     {movieReq(map[string]any{"key": "m1", "name": "a.mkv", "size": 5, "role": "../../"}), "no_movie_file"},
		"bad provider": {map[string]any{"provider": "../", "id": "1", "language": "en-US", "items": []any{}}, "unknown_provider"},
		"bad id":       {map[string]any{"provider": "tmdb", "id": "1/../2", "language": "en-US", "items": []any{}}, "invalid_id"},
		"bad lang":     {map[string]any{"provider": "tmdb", "id": "1", "language": "../x", "items": []any{}}, "invalid_language"},
		"unknown folder": {func() map[string]any {
			r := movieReq(movieItem("m1", "a.mkv", 5))
			r["folder"] = map[string]any{"mode": "existing", "name": "../../etc"}
			return r
		}(), "unknown_folder"},
		"bad sub lang": {movieReq(movieItem("m1", "a.mkv", 5),
			map[string]any{"key": "s", "name": "a.srt", "size": 1, "role": "subtitle", "lang": "../en"}), "invalid_subtitle_language"},
	}
	for name, c := range cases {
		var apiErr apiError
		code := e.json("POST", "/api/plan/movie", c.req, &apiErr)
		if code != 400 || apiErr.Error != c.code {
			t.Errorf("%s: %d %q, want 400 %q", name, code, apiErr.Error, c.code)
		}
	}
	// A title full of path tricks still yields a single safe folder.
	var plan ingest.Plan
	r := movieReq(movieItem("m1", "a.mkv", 5))
	r["id"] = "100"
	if code := e.json("POST", "/api/plan/movie", r, &plan); code != 200 {
		t.Fatalf("plan: %d", code)
	}
	if p := plan.Items[0].Path; p != "movies/Amélie - The Fabulous - Destiny (2001) [tmdbid-100]/Amélie - The Fabulous - Destiny (2001) [tmdbid-100].mkv" {
		t.Fatalf("path %q", p)
	}
	// An edited display title is sanitized the same way.
	r["title"] = "../../../etc/cron.d/x"
	e.json("POST", "/api/plan/movie", r, &plan)
	segs := strings.Split(plan.Items[0].Path, "/")
	if len(segs) != 3 {
		t.Fatalf("path %q", plan.Items[0].Path)
	}
	for _, s := range segs {
		if s == ".." || s == "." || strings.HasPrefix(s, ".") {
			t.Fatalf("unsafe segment %q in %q", s, plan.Items[0].Path)
		}
	}
}

func TestUploadOwnershipAndLength(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	var batch ingest.BatchView
	e.json("POST", "/api/batches/movie", movieReq(movieItem("m1", "a.mkv", 5)), &batch)
	meta := "batch " + base64.StdEncoding.EncodeToString([]byte(batch.ID)) + ",item " + base64.StdEncoding.EncodeToString([]byte(batch.Items[0].ID))
	tus := map[string]string{"Tus-Resumable": "1.0.0", "Upload-Metadata": meta}

	tus["Upload-Length"] = "6"
	if w := e.req("POST", "/api/uploads", nil, tus); w.Code != 400 {
		t.Fatalf("length mismatch: %d", w.Code)
	}
	tus["Upload-Length"] = "5"
	e.user = "mallory"
	if w := e.req("POST", "/api/uploads", nil, tus); w.Code != 404 {
		t.Fatalf("other user's batch: %d", w.Code)
	}
	if code := e.json("GET", "/api/batches/"+batch.ID, nil, nil); code != 404 {
		t.Fatalf("other user's batch view: %d", code)
	}
	e.user = "alice"
	w := e.req("POST", "/api/uploads", nil, tus)
	if w.Code != 201 {
		t.Fatalf("create: %d", w.Code)
	}
	loc := w.Header().Get("Location")
	patch := map[string]string{"Tus-Resumable": "1.0.0", "Content-Type": "application/offset+octet-stream", "Upload-Offset": "3"}
	if w := e.req("PATCH", loc, strings.NewReader("ab"), patch); w.Code != 409 {
		t.Fatalf("wrong offset: %d", w.Code)
	}
	patch["Upload-Offset"] = "0"
	if w := e.req("PATCH", loc, strings.NewReader("abcdefgh"), patch); w.Code != 413 {
		t.Fatalf("oversized chunk: %d", w.Code)
	}
	e.user = "mallory"
	if w := e.req("PATCH", loc, strings.NewReader("ab"), patch); w.Code != 404 {
		t.Fatalf("other user's upload: %d", w.Code)
	}
	e.user = "alice"
	if w := e.req("PATCH", loc, strings.NewReader("ab"), map[string]string{"Tus-Resumable": "0.2.0", "Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"}); w.Code != 412 {
		t.Fatalf("tus version: %d", w.Code)
	}
	if w := e.req("DELETE", loc, nil, map[string]string{"Tus-Resumable": "1.0.0"}); w.Code != 204 {
		t.Fatalf("terminate: %d", w.Code)
	}
	if left, _ := os.ReadDir(filepath.Join(e.media, ".incoming")); len(left) != 0 {
		t.Fatalf("staging not cleaned: %v", left)
	}
}

func TestBasicAuth(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	e := newEnv(t, config.AuthBasic, func(c *config.Config) { c.BasicUser, c.BasicHash = "admin", hash })
	e.user = ""
	if code := e.json("GET", "/api/session", nil, nil); code != 401 {
		t.Fatalf("anon session: %d", code)
	}
	// Static files are public in basic mode (login page).
	if w := e.req("GET", "/", nil, nil); w.Code != 200 {
		t.Fatalf("static: %d", w.Code)
	}
	if code := e.json("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "wrong"}, nil); code != 401 {
		t.Fatalf("bad password: %d", code)
	}
	if code := e.json("POST", "/api/auth/login", map[string]string{"username": "root", "password": "correct horse"}, nil); code != 401 {
		t.Fatalf("bad user: %d", code)
	}
	b, _ := json.Marshal(map[string]string{"username": "admin", "password": "correct horse"})
	w := e.req("POST", "/api/auth/login", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var sess *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie %+v", sess)
	}
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.AddCookie(sess)
	rw := httptest.NewRecorder()
	e.h.ServeHTTP(rw, r)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"user":"admin"`) {
		t.Fatalf("authed session: %d %s", rw.Code, rw.Body)
	}
	// The proxy header means nothing in basic mode.
	r = httptest.NewRequest("GET", "/api/session", nil)
	r.RemoteAddr = proxyIP + ":1"
	r.Header.Set("Remote-User", "admin")
	rw = httptest.NewRecorder()
	e.h.ServeHTTP(rw, r)
	if rw.Code != 401 {
		t.Fatalf("header in basic mode: %d", rw.Code)
	}
}

func TestLoginRateLimited(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	e := newEnv(t, config.AuthBasic, func(c *config.Config) { c.BasicUser, c.BasicHash = "admin", hash })
	limited := false
	for range 8 {
		if code := e.json("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "x"}, nil); code == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("login was never rate limited")
	}
}

func TestSetupPage(t *testing.T) {
	e := newEnv(t, config.AuthProxy, func(c *config.Config) { c.AdminUsers = []string{"root"} })
	e.keys.key, e.keys.fromEnv = "", false
	var sess sessionResponse
	e.json("GET", "/api/session", nil, &sess)
	if !sess.NeedsSetup || sess.Admin {
		t.Fatalf("%+v", sess)
	}
	if code := e.json("POST", "/api/setup/tmdb", map[string]string{"apiKey": "0123456789abcdef0123"}, nil); code != 403 {
		t.Fatalf("non-admin setup: %d", code)
	}
	e.user = "root"
	if code := e.json("POST", "/api/setup/tmdb", map[string]string{"apiKey": "0123456789abcdef0123"}, nil); code != 200 {
		t.Fatalf("setup: %d", code)
	}
	fi, err := os.Stat(filepath.Join(e.data, "tmdb_api_key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", err, fi)
	}
	if code := e.json("POST", "/api/setup/tmdb", map[string]string{"apiKey": "0123456789abcdef0123"}, nil); code != 409 {
		t.Fatalf("second setup: %d", code)
	}
}

func TestFolderReuseByProviderID(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	os.MkdirAll(filepath.Join(e.media, "movies", "Inception {tmdb-27205}"), 0o755)
	os.MkdirAll(filepath.Join(e.media, "movies", "Inception (2010)"), 0o755)
	var plan ingest.Plan
	e.json("POST", "/api/plan/movie", movieReq(movieItem("m1", "a.mkv", 5)), &plan)
	if plan.Folder != "Inception {tmdb-27205}" || plan.FolderSource != "existing-id" {
		t.Fatalf("%+v", plan)
	}
	os.Remove(filepath.Join(e.media, "movies", "Inception {tmdb-27205}"))
	e.json("POST", "/api/plan/movie", movieReq(movieItem("m1", "a.mkv", 5)), &plan)
	if plan.FolderSource != "new" || len(plan.FolderCandidates) != 1 || plan.FolderCandidates[0] != "Inception (2010)" {
		t.Fatalf("%+v", plan)
	}
	r := movieReq(movieItem("m1", "a.mkv", 5))
	r["folder"] = map[string]any{"mode": "existing", "name": "Inception (2010)"}
	e.json("POST", "/api/plan/movie", r, &plan)
	if plan.Items[0].Path != "movies/Inception (2010)/Inception (2010) [tmdbid-27205].mkv" {
		t.Fatalf("%q", plan.Items[0].Path)
	}
}
