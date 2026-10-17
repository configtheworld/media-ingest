package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/media-ingest/media-ingest/internal/provider"
)

func newTest(t *testing.T, key string, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, srv.URL+"/img", func() string { return key })
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestSearchMovie(t *testing.T) {
	c := newTest(t, "k123", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/movie" || r.URL.Query().Get("api_key") != "k123" ||
			r.URL.Query().Get("language") != "de-DE" || r.URL.Query().Get("query") != "Inception" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(`{"page":1,"total_pages":1,"results":[{"id":27205,"title":"Inception","original_title":"Inception","release_date":"2010-07-15","overview":"x","poster_path":"/p.jpg"}]}`))
	})
	res, err := c.Search(context.Background(), provider.Movie, "Inception", "de-DE", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Results[0].ID != "27205" || res.Results[0].Year != 2010 || res.Results[0].Original != "" {
		t.Fatalf("%+v", res)
	}
}

func TestBearerToken(t *testing.T) {
	c := newTest(t, "eyJhbGciOi.token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer eyJhbGciOi.token" || r.URL.Query().Has("api_key") {
			t.Errorf("v4 token must go in the Authorization header")
		}
		w.Write([]byte(`{"id":1,"title":"A","release_date":"1999-01-01"}`))
	})
	if _, err := c.Movie(context.Background(), "1", "en-US"); err != nil {
		t.Fatal(err)
	}
}

func TestCaching(t *testing.T) {
	var calls atomic.Int32
	c := newTest(t, "k", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"id":1,"title":"A"}`))
	})
	for range 3 {
		c.Movie(context.Background(), "1", "en-US")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	c.Movie(context.Background(), "1", "tr-TR") // different language, different entry
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestRateLimitRetry(t *testing.T) {
	var calls atomic.Int32
	c := newTest(t, "k", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"id":1,"title":"A"}`))
	})
	if _, err := c.Movie(context.Background(), "1", "en-US"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestRateLimitLongWait(t *testing.T) {
	c := newTest(t, "k", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Movie(context.Background(), "1", "en-US")
	var rl *provider.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
		t.Fatalf("got %v", err)
	}
}

func TestErrorsClassifiedAndKeyRedacted(t *testing.T) {
	c := newTest(t, "supersecret", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movie/2":
			w.WriteHeader(http.StatusNotFound)
		case "/movie/3":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	})
	if _, err := c.Movie(context.Background(), "2", "en-US"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("404: %v", err)
	}
	if _, err := c.Movie(context.Background(), "3", "en-US"); !errors.Is(err, provider.ErrUnauthorized) {
		t.Errorf("401: %v", err)
	}
	_, err := c.Movie(context.Background(), "4", "en-US")
	if !errors.Is(err, provider.ErrUnavailable) || strings.Contains(err.Error(), "supersecret") {
		t.Errorf("5xx: %v", err)
	}
}

func TestOfflineErrorRedacted(t *testing.T) {
	c := New("http://127.0.0.1:1", "http://127.0.0.1:1", func() string { return "supersecret" })
	_, err := c.Search(context.Background(), provider.Movie, "x", "en-US", 1)
	if !errors.Is(err, provider.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "supersecret") || strings.Contains(err.Error(), "api_key") {
		t.Fatalf("key leaked in error: %v", err)
	}
}

func TestNotConfigured(t *testing.T) {
	c := New("http://x", "http://x", func() string { return "" })
	if _, err := c.Movie(context.Background(), "1", "en-US"); !errors.Is(err, provider.ErrNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

func TestValidation(t *testing.T) {
	c := New("http://x", "http://x", func() string { return "k" })
	for _, id := range []string{"", "0", "01", "1a", "../1", "12345678901"} {
		if c.ValidID(id) {
			t.Errorf("ValidID(%q)", id)
		}
	}
	for _, ref := range []string{"/../../x.jpg", "x.jpg", "/a.svg", "/a.jpg?x", "//evil.com/a.jpg"} {
		if c.ValidImage("w185", ref) {
			t.Errorf("ValidImage(%q)", ref)
		}
	}
	if c.ValidImage("w9999", "/a.jpg") || !c.ValidImage("w185", "/aBc_-1.jpg") {
		t.Error("size validation")
	}
}
