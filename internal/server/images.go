package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/media-ingest/media-ingest/internal/provider"
)

const (
	imageTTL     = 30 * 24 * time.Hour
	maxImageSize = 5 << 20
)

// imageCache proxies provider posters through the server and caches them on
// disk, so browsers never contact the provider and the API key stays private.
type imageCache struct {
	dir string
	sf  singleflight.Group
}

func newImageCache(dir string) *imageCache { return &imageCache{dir: dir} }

func contentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	}
	return "image/jpeg"
}

func (c *imageCache) serve(w http.ResponseWriter, r *http.Request, p provider.Provider, size, ref string, log *slog.Logger) {
	if !p.ValidImage(size, ref) {
		http.NotFound(w, r)
		return
	}
	// size and ref are validated against strict patterns, so the cache path
	// cannot escape the cache directory.
	file := filepath.Join(c.dir, p.Name(), size, filepath.Base(ref))
	fi, err := os.Stat(file)
	if err != nil || time.Since(fi.ModTime()) > imageTTL {
		_, err, _ = c.sf.Do(file, func() (any, error) { return nil, c.fetch(r, p, size, ref, file) })
		if err != nil {
			if fi == nil { // nothing stale to fall back to
				if errors.Is(err, provider.ErrNotFound) {
					http.NotFound(w, r)
				} else {
					log.Debug("poster fetch failed", "err", err)
					http.Error(w, "image unavailable", http.StatusBadGateway)
				}
				return
			}
		}
	}
	f, err := os.Open(file)
	if err != nil {
		http.Error(w, "image unavailable", http.StatusBadGateway)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentTypeFor(file))
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = io.Copy(w, f)
}

func (c *imageCache) fetch(r *http.Request, p provider.Provider, size, ref, file string) error {
	// Shared by concurrent waiters: one client leaving must not abort it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
	defer cancel()
	body, _, err := p.Image(ctx, size, ref)
	if err != nil {
		return err
	}
	defer body.Close()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".img-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(body, maxImageSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxImageSize {
		return errors.New("image too large")
	}
	return os.Rename(tmp.Name(), file)
}
