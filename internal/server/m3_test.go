package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/media-ingest/media-ingest/internal/config"
	"github.com/media-ingest/media-ingest/internal/ingest"
)

// fakeJellyfin records Library/Media/Updated calls.
type fakeJellyfin struct {
	mu      sync.Mutex
	updates [][]map[string]string
	status  int
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct{ Updates []map[string]string }
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/Library/Media/Updated" {
		f.updates = append(f.updates, body.Updates)
		w.WriteHeader(f.status)
		return
	}
	w.WriteHeader(204)
}

func (f *fakeJellyfin) last() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) == 0 {
		return nil
	}
	return f.updates[len(f.updates)-1]
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestUndoRestoresReplacedFilesAndNotifiesJellyfin(t *testing.T) {
	jf := &fakeJellyfin{status: 204}
	jsrv := httptest.NewServer(jf)
	defer jsrv.Close()
	e := newEnv(t, config.AuthProxy, func(c *config.Config) {
		c.JellyfinURL, c.JellyfinAPIKey, c.JellyfinPathPrefix = jsrv.URL, "k", "/jf"
	})

	// Library: an old copy of S01E02 under another name.
	old := show + "/Season 01/Harbor.Lights.S01E02.720p.mkv"
	os.MkdirAll(filepath.Join(e.media, filepath.Dir(old)), 0o755)
	os.WriteFile(filepath.Join(e.media, old), []byte("old"), 0o644)

	req := tvReq(ep("a", "new.mkv", 1, 2, "policy", "replace"), ep("b", "x.mkv", 2, 1))
	var batch ingest.BatchView
	e.json("POST", "/api/batches/tv", req, &batch)
	for _, it := range batch.Items {
		e.upload(batch.ID, it.ID, []byte("new!"))
	}
	waitFor(t, func() bool { return len(jf.last()) == 3 })
	got := map[string]string{}
	for _, u := range jf.last() {
		got[u["Path"]] = u["UpdateType"]
	}
	if got["/jf/"+old] != "Deleted" || got["/jf/"+show+"/Season 01/Harbor Lights - S01E02 - Fog.mkv"] != "Created" ||
		got["/jf/"+show+"/Season 02/Harbor Lights - S02E01 - Return.mkv"] != "Created" {
		t.Fatalf("jellyfin updates %v", got)
	}
	var view ingest.BatchView
	waitFor(t, func() bool { e.json("GET", "/api/batches/"+batch.ID, nil, &view); return view.Hook == "media_updated" })

	// History lists the batch as undoable.
	var hist struct{ Batches []ingest.HistoryEntry }
	e.json("GET", "/api/history", nil, &hist)
	if len(hist.Batches) != 1 || !hist.Batches[0].CanUndo || hist.Batches[0].Published != 2 {
		t.Fatalf("history %+v", hist)
	}

	// Someone changed one published file afterwards: undo must leave it alone.
	changed := filepath.Join(e.media, show, "Season 02", "Harbor Lights - S02E01 - Return.mkv")
	os.WriteFile(changed, []byte("edited by someone else"), 0o644)

	var undone ingest.BatchView
	if code := e.json("POST", "/api/batches/"+batch.ID+"/undo", nil, &undone); code != 200 {
		t.Fatalf("undo: %d", code)
	}
	if undone.Status != "undone" {
		t.Fatalf("status %q", undone.Status)
	}
	if b, _ := os.ReadFile(filepath.Join(e.media, old)); string(b) != "old" {
		t.Fatal("replaced file was not restored")
	}
	if _, err := os.Stat(filepath.Join(e.media, show, "Season 01", "Harbor Lights - S01E02 - Fog.mkv")); !os.IsNotExist(err) {
		t.Fatal("published file still in the library")
	}
	if b, _ := os.ReadFile(changed); string(b) != "edited by someone else" {
		t.Fatal("undo touched a file changed since publishing")
	}
	for _, it := range undone.Items {
		switch it.Key {
		case "a":
			if it.Status != "undone" || !strings.HasPrefix(it.UndoPath, ".trash/") {
				t.Fatalf("%+v", it)
			}
			if b, _ := os.ReadFile(filepath.Join(e.media, it.UndoPath)); string(b) != "new!" {
				t.Fatal("undone file not kept in trash")
			}
		case "b":
			if it.Status != "published" || it.Error != "changed_since_publish" {
				t.Fatalf("%+v", it)
			}
		}
	}
	waitFor(t, func() bool {
		u := jf.last()
		return len(u) == 2 && u[0]["UpdateType"] == "Deleted" && u[1]["UpdateType"] == "Created"
	})
	if code := e.json("POST", "/api/batches/"+batch.ID+"/undo", nil, nil); code != 400 {
		t.Fatalf("second undo: %d", code)
	}
}

func TestUndoPrunesEmptyFolders(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	var batch ingest.BatchView
	e.json("POST", "/api/batches/movie", movieReq(movieItem("m1", "a.mkv", 3)), &batch)
	e.upload(batch.ID, batch.Items[0].ID, []byte("abc"))
	if code := e.json("POST", "/api/batches/"+batch.ID+"/undo", nil, nil); code != 200 {
		t.Fatalf("undo %d", code)
	}
	if _, err := os.Stat(filepath.Join(e.media, "movies", "Inception (2010) [tmdbid-27205]")); !os.IsNotExist(err) {
		t.Fatal("empty movie folder should be removed")
	}
	if _, err := os.Stat(filepath.Join(e.media, "movies")); err != nil {
		t.Fatal("library root must stay")
	}
	// Other users cannot undo or see it.
	e.user = "mallory"
	if code := e.json("POST", "/api/batches/"+batch.ID+"/undo", nil, nil); code != 404 {
		t.Fatalf("other user: %d", code)
	}
	var hist struct{ Batches []ingest.HistoryEntry }
	e.json("GET", "/api/history", nil, &hist)
	if len(hist.Batches) != 0 {
		t.Fatal("history leaked another user's batches")
	}
}

func TestJellyfinFailureIsSoft(t *testing.T) {
	jf := &fakeJellyfin{status: 500}
	jsrv := httptest.NewServer(jf)
	defer jsrv.Close()
	e := newEnv(t, config.AuthProxy, func(c *config.Config) { c.JellyfinURL, c.JellyfinAPIKey, c.JellyfinPathPrefix = jsrv.URL, "k", "/m" })
	var batch ingest.BatchView
	e.json("POST", "/api/batches/movie", movieReq(movieItem("m1", "a.mkv", 3)), &batch)
	e.upload(batch.ID, batch.Items[0].ID, []byte("abc"))
	var view ingest.BatchView
	waitFor(t, func() bool { e.json("GET", "/api/batches/"+batch.ID, nil, &view); return view.Hook != "" })
	if view.Status != "done" || !strings.HasPrefix(view.Hook, "refresh") {
		t.Fatalf("status %q hook %q", view.Status, view.Hook)
	}
}

func TestQuickUpload(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	os.MkdirAll(filepath.Join(e.media, "home videos", "2024"), 0o755)
	os.MkdirAll(filepath.Join(e.media, ".hidden"), 0o755)

	var root ingest.BrowseView
	e.json("GET", "/api/browse", nil, &root)
	if strings.Join(root.Dirs, ",") != "home videos" { // .incoming, .trash and hidden folders are not offered
		t.Fatalf("%+v", root)
	}
	for _, bad := range []string{"..", "../etc", ".incoming", ".trash/x", "/etc", "home videos/../..", "nope"} {
		if code := e.json("GET", "/api/browse?path="+strings.ReplaceAll(bad, " ", "%20"), nil, nil); code != 400 {
			t.Errorf("browse %q: %d", bad, code)
		}
	}

	req := map[string]any{"folder": "home videos", "newFolder": "Trip: Paris?", "keepStructure": true, "items": []map[string]any{
		{"key": "a", "name": "clip 1.mp4", "path": "Day 1/clip 1.mp4", "size": 3},
		{"key": "b", "name": "notes<1>.srt", "path": "notes<1>.srt", "size": 2},
	}}
	var plan ingest.Plan
	if code := e.json("POST", "/api/plan/quick", req, &plan); code != 200 {
		t.Fatalf("plan %d", code)
	}
	p := paths(plan)
	if p["a"] != "home videos/Trip - Paris/Day 1/clip 1.mp4" || p["b"] != "home videos/Trip - Paris/notes1.srt" {
		t.Fatalf("%v", p)
	}
	if !hasWarning(plan, "names_sanitized") {
		t.Fatal("sanitized names must be reported")
	}
	var batch ingest.BatchView
	e.json("POST", "/api/batches/quick", req, &batch)
	for _, it := range batch.Items {
		e.upload(batch.ID, it.ID, []byte(strings.Repeat("x", int(it.Size))))
	}
	if b, err := os.ReadFile(filepath.Join(e.media, "home videos/Trip - Paris/Day 1/clip 1.mp4")); err != nil || string(b) != "xxx" {
		t.Fatalf("%q %v", b, err)
	}

	hostile := map[string]map[string]any{
		"no folder":     {"folder": "", "items": []map[string]any{{"key": "a", "name": "a.mkv", "size": 1}}},
		"staging":       {"folder": ".incoming", "items": []map[string]any{{"key": "a", "name": "a.mkv", "size": 1}}},
		"traversal":     {"folder": "home videos/../..", "items": []map[string]any{{"key": "a", "name": "a.mkv", "size": 1}}},
		"bad ext":       {"folder": "home videos", "items": []map[string]any{{"key": "a", "name": "a.sh", "size": 1}}},
		"junk dir":      {"folder": "home videos", "keepStructure": true, "items": []map[string]any{{"key": "a", "name": "a.mkv", "path": "._x/a.mkv", "size": 1}}},
		"dot new":       {"folder": "home videos", "newFolder": "..", "items": []map[string]any{{"key": "a", "name": "a.mkv", "size": 1}}},
		"too deep":      {"folder": "home videos", "keepStructure": true, "items": []map[string]any{{"key": "a", "name": "a.mkv", "path": "1/2/3/4/5/6/a.mkv", "size": 1}}},
		"ext by sanitz": {"folder": "home videos", "items": []map[string]any{{"key": "a", "name": "a.mkv.", "size": 1}}},
	}
	for name, r := range hostile {
		if code := e.json("POST", "/api/plan/quick", r, nil); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	// "../" segments in a dropped path cannot climb out of the destination.
	var p2 ingest.Plan
	e.json("POST", "/api/plan/quick", map[string]any{"folder": "home videos", "keepStructure": true,
		"items": []map[string]any{{"key": "a", "name": "a.mkv", "path": "../../../etc/a.mkv", "size": 1}}}, &p2)
	if got := paths(p2)["a"]; !strings.HasPrefix(got, "home videos/") || strings.Contains(got, "/../") {
		t.Fatalf("escaped: %q", got)
	}
}

func TestParserSettings(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	var st settingsResponse
	e.json("GET", "/api/settings", nil, &st)
	if strings.Join(st.Parser.Packs, ",") != "de,es,tr" || len(st.Packs) != 3 {
		t.Fatalf("%+v", st.Parser)
	}
	var rules struct{ Episode []struct{ Name string } }
	e.json("GET", "/api/parser/rules", nil, &rules)
	if rules.Episode[0].Name != "Staffel 1 Folge 3" {
		t.Fatalf("first rule %q", rules.Episode[0].Name)
	}
	custom := "episode:\n  - name: Mine\n    pattern: 'ep(?<episode>\\d+)x'\n"
	if code := e.json("PUT", "/api/settings", map[string]any{"parser": map[string]any{"packs": []string{"tr"}, "custom": custom}}, &st); code != 200 {
		t.Fatalf("put %d", code)
	}
	e.json("GET", "/api/parser/rules", nil, &rules)
	if rules.Episode[0].Name != "Mine" || rules.Episode[1].Name != "1. Sezon 3. Bölüm" {
		t.Fatalf("order %v", rules.Episode[:2])
	}
	for name, ps := range map[string]map[string]any{
		"unknown pack": {"packs": []string{"xx"}},
		"lookahead":    {"packs": []string{}, "custom": "episode:\n  - name: x\n    pattern: '(?=a)(?<episode>\\d)'\n"},
		"bad yaml":     {"packs": []string{}, "custom": "episode: ["},
	} {
		var apiErr apiError
		if code := e.json("PUT", "/api/settings", map[string]any{"parser": ps}, &apiErr); code != 400 {
			t.Errorf("%s: %d %+v", name, code, apiErr)
		}
	}
}

func TestTrashRetention(t *testing.T) {
	e := newEnv(t, config.AuthProxy, func(c *config.Config) { c.TrashRetention = 24 * time.Hour })
	oldEntry := filepath.Join(e.media, ".trash", "20200101-000000-0123abcd", "movies", "x.mkv")
	newEntry := filepath.Join(e.media, ".trash", time.Now().UTC().Format("20060102-150405")+"-0123abcd-undo", "x.mkv")
	foreign := filepath.Join(e.media, ".trash", "my own stuff", "x.mkv")
	for _, p := range []string{oldEntry, newEntry, foreign} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	e.svc.Janitor(t.Context())
	if _, err := os.Stat(oldEntry); !os.IsNotExist(err) {
		t.Fatal("expired trash not purged")
	}
	for _, p := range []string{newEntry, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s must stay: %v", p, err)
		}
	}
}
