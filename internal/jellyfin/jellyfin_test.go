package jellyfin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fake struct {
	mu      sync.Mutex
	calls   []string
	bodies  []string
	auth    []string
	updated int // status for /Library/Media/Updated
	refresh int
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.bodies = append(f.bodies, string(b))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	switch r.URL.Path {
	case "/Library/Media/Updated":
		w.WriteHeader(f.updated)
	case "/Library/Refresh":
		w.WriteHeader(f.refresh)
	default:
		w.WriteHeader(404)
	}
}

func TestMediaUpdated(t *testing.T) {
	f := &fake{updated: 204, refresh: 204}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(srv.URL+"/", "secret-key", "/data/media")
	res := c.Notify(context.Background(), []Update{{Path: "tv/Show/Season 01/a.mkv", Type: Created}, {Path: "tv/Show/old.mkv", Type: Deleted}})
	if res.Method != "media_updated" || res.Error != "" {
		t.Fatalf("%+v", res)
	}
	if len(f.calls) != 1 || f.calls[0] != "POST /Library/Media/Updated" {
		t.Fatalf("%v", f.calls)
	}
	if f.auth[0] != `MediaBrowser Token="secret-key"` {
		t.Fatalf("auth %q", f.auth[0])
	}
	var body struct {
		Updates []struct{ Path, UpdateType string }
	}
	if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.Updates[0].Path != "/data/media/tv/Show/Season 01/a.mkv" || body.Updates[0].UpdateType != "Created" || body.Updates[1].UpdateType != "Deleted" {
		t.Fatalf("%+v", body)
	}
}

func TestFallbackToRefresh(t *testing.T) {
	f := &fake{updated: 500, refresh: 204}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res := New(srv.URL, "k", "/m").Notify(context.Background(), []Update{{Path: "a.mkv", Type: Created}})
	if res.Method != "refresh" || !strings.Contains(res.Error, "HTTP 500") {
		t.Fatalf("%+v", res)
	}
	if len(f.calls) != 2 || f.calls[1] != "POST /Library/Refresh" {
		t.Fatalf("%v", f.calls)
	}
}

func TestFailsSoft(t *testing.T) {
	f := &fake{updated: 401, refresh: 401}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res := New(srv.URL, "k", "/m").Notify(context.Background(), []Update{{Path: "a.mkv", Type: Created}})
	if res.Method != "failed" {
		t.Fatalf("%+v", res)
	}
	// Unreachable server: still no panic, no key in the message.
	res = New("http://127.0.0.1:1", "supersecret", "/m").Notify(context.Background(), []Update{{Path: "a.mkv", Type: Created}})
	if res.Method != "failed" || strings.Contains(res.Error, "supersecret") || strings.Contains(res.Error, "127.0.0.1:1/Library") {
		t.Fatalf("%+v", res)
	}
}

func TestDisabled(t *testing.T) {
	if New("", "k", "/m") != nil {
		t.Fatal("empty URL must disable the hook")
	}
}

func TestRootPrefix(t *testing.T) {
	f := &fake{updated: 204, refresh: 204}
	srv := httptest.NewServer(f)
	defer srv.Close()
	New(srv.URL, "k", "/").Notify(context.Background(), []Update{{Path: "tv/Show/Season 01/a.mkv", Type: Created}})
	if !strings.Contains(f.bodies[0], `"Path":"/tv/Show/Season 01/a.mkv"`) {
		t.Fatalf("%s", f.bodies[0])
	}
}
