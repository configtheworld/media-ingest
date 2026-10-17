// Package tmdb implements provider.Provider for The Movie Database (TMDB) API v3.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/media-ingest/media-ingest/internal/cache"
	"github.com/media-ingest/media-ingest/internal/provider"
)

const (
	searchTTL  = time.Hour
	detailsTTL = 12 * time.Hour
	maxBody    = 4 << 20
)

// Client talks to TMDB. The API key is read through key() on every request
// so a key saved via the setup page takes effect immediately. The key is
// never logged and never included in returned errors.
type Client struct {
	base, imgBase string
	key           func() string
	http          *http.Client
	cache         *cache.TTL
	sleep         func(context.Context, time.Duration) error
}

func New(base, imgBase string, key func() string) *Client {
	return &Client{
		base: base, imgBase: imgBase, key: key,
		http:  &http.Client{Timeout: 15 * time.Second},
		cache: cache.New(2000),
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

func (c *Client) Name() string { return "tmdb" }

func (c *Client) Attribution() provider.Attribution {
	return provider.Attribution{
		Text: "This product uses the TMDB API but is not endorsed or certified by TMDB.",
		URL:  "https://www.themoviedb.org/",
	}
}

var idRe = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

func (c *Client) ValidID(id string) bool { return idRe.MatchString(id) }

// get performs a GET, decoding JSON into out. It retries once on short 429
// waits and once on 5xx. Errors are classified and contain no URL.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	key := c.key()
	if key == "" {
		return provider.ErrNotConfigured
	}
	if q == nil {
		q = url.Values{}
	}
	bearer := strings.HasPrefix(key, "eyJ") // v4 read access token (JWT)
	if !bearer {
		q.Set("api_key", key)
	}
	u := c.base + path + "?" + q.Encode()
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("%w: bad request", provider.ErrUnavailable)
		}
		req.Header.Set("Accept", "application/json")
		if bearer {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %s", provider.ErrUnavailable, redact(err))
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			if rerr != nil {
				return fmt.Errorf("%w: %s", provider.ErrUnavailable, redact(rerr))
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("%w: invalid response", provider.ErrUnavailable)
			}
			return nil
		case resp.StatusCode == http.StatusNotFound:
			return provider.ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized:
			return provider.ErrUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := retryAfter(resp.Header.Get("Retry-After"))
			if attempt < 2 && wait <= 3*time.Second {
				if err := c.sleep(ctx, wait); err != nil {
					return err
				}
				continue
			}
			return &provider.RateLimitError{RetryAfter: wait}
		case resp.StatusCode >= 500 && attempt == 0:
			if err := c.sleep(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		default:
			return fmt.Errorf("%w: HTTP %d", provider.ErrUnavailable, resp.StatusCode)
		}
	}
}

// redact strips the request URL (which may contain api_key) from an error.
func redact(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	s := err.Error()
	if i := strings.Index(s, "api_key="); i >= 0 {
		s = s[:i] + "api_key=REDACTED"
	}
	return s
}

func retryAfter(h string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && n >= 0 && n < 3600 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 && d < time.Hour {
			return d
		}
	}
	return time.Second
}

func (c *Client) cached(key string, ttl time.Duration, fetch func() (any, error)) (any, error) {
	if v, ok := c.cache.Get(key); ok {
		return v, nil
	}
	v, err := fetch()
	if err != nil {
		return nil, err
	}
	c.cache.Set(key, v, ttl)
	return v, nil
}

func year(date string) int {
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil && y > 1800 && y < 3000 {
			return y
		}
	}
	return 0
}

type searchResp struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	Results    []struct {
		ID            int64  `json:"id"`
		Title         string `json:"title"`
		Name          string `json:"name"`
		OriginalTitle string `json:"original_title"`
		OriginalName  string `json:"original_name"`
		ReleaseDate   string `json:"release_date"`
		FirstAirDate  string `json:"first_air_date"`
		Overview      string `json:"overview"`
		PosterPath    string `json:"poster_path"`
	} `json:"results"`
}

func (c *Client) Search(ctx context.Context, kind provider.Kind, query, lang string, page int) (*provider.SearchResult, error) {
	q := url.Values{"query": {query}, "language": {lang}, "page": {strconv.Itoa(page)}, "include_adult": {"false"}}
	path := "/search/movie"
	if kind == provider.TV {
		path = "/search/tv"
	}
	v, err := c.cached("s|"+path+"|"+q.Encode(), searchTTL, func() (any, error) {
		var r searchResp
		if err := c.get(ctx, path, q, &r); err != nil {
			return nil, err
		}
		out := &provider.SearchResult{Page: r.Page, TotalPages: r.TotalPages, Results: []provider.Summary{}}
		for _, x := range r.Results {
			s := provider.Summary{ID: strconv.FormatInt(x.ID, 10), Kind: kind, Overview: x.Overview, Poster: x.PosterPath}
			if kind == provider.TV {
				s.Title, s.Original, s.Year = x.Name, x.OriginalName, year(x.FirstAirDate)
			} else {
				s.Title, s.Original, s.Year = x.Title, x.OriginalTitle, year(x.ReleaseDate)
			}
			if s.Original == s.Title {
				s.Original = ""
			}
			out.Results = append(out.Results, s)
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*provider.SearchResult), nil
}

func (c *Client) Movie(ctx context.Context, id, lang string) (*provider.MovieDetails, error) {
	if !c.ValidID(id) {
		return nil, provider.ErrNotFound
	}
	v, err := c.cached("m|"+id+"|"+lang, detailsTTL, func() (any, error) {
		var r struct {
			ID            int64  `json:"id"`
			Title         string `json:"title"`
			OriginalTitle string `json:"original_title"`
			ReleaseDate   string `json:"release_date"`
			Overview      string `json:"overview"`
			PosterPath    string `json:"poster_path"`
			Runtime       int    `json:"runtime"`
		}
		if err := c.get(ctx, "/movie/"+id, url.Values{"language": {lang}}, &r); err != nil {
			return nil, err
		}
		m := &provider.MovieDetails{ID: strconv.FormatInt(r.ID, 10), Title: r.Title, Original: r.OriginalTitle,
			Year: year(r.ReleaseDate), Overview: r.Overview, Poster: r.PosterPath, Runtime: r.Runtime}
		if m.Title == "" {
			m.Title = m.Original
		}
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*provider.MovieDetails), nil
}

func (c *Client) Show(ctx context.Context, id, lang string) (*provider.ShowDetails, error) {
	if !c.ValidID(id) {
		return nil, provider.ErrNotFound
	}
	v, err := c.cached("t|"+id+"|"+lang, detailsTTL, func() (any, error) {
		var r struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			OriginalName string `json:"original_name"`
			FirstAirDate string `json:"first_air_date"`
			Overview     string `json:"overview"`
			PosterPath   string `json:"poster_path"`
			Seasons      []struct {
				SeasonNumber int    `json:"season_number"`
				Name         string `json:"name"`
				EpisodeCount int    `json:"episode_count"`
				AirDate      string `json:"air_date"`
				PosterPath   string `json:"poster_path"`
			} `json:"seasons"`
		}
		if err := c.get(ctx, "/tv/"+id, url.Values{"language": {lang}}, &r); err != nil {
			return nil, err
		}
		s := &provider.ShowDetails{ID: strconv.FormatInt(r.ID, 10), Title: r.Name, Original: r.OriginalName,
			Year: year(r.FirstAirDate), Overview: r.Overview, Poster: r.PosterPath, Seasons: []provider.SeasonSummary{}}
		if s.Title == "" {
			s.Title = s.Original
		}
		for _, x := range r.Seasons {
			s.Seasons = append(s.Seasons, provider.SeasonSummary{Number: x.SeasonNumber, Name: x.Name,
				EpisodeCount: x.EpisodeCount, AirDate: x.AirDate, Poster: x.PosterPath})
		}
		return s, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*provider.ShowDetails), nil
}

func (c *Client) Season(ctx context.Context, id string, season int, lang string) (*provider.SeasonDetails, error) {
	if !c.ValidID(id) || season < 0 || season > 1000 {
		return nil, provider.ErrNotFound
	}
	key := fmt.Sprintf("se|%s|%d|%s", id, season, lang)
	v, err := c.cached(key, detailsTTL, func() (any, error) {
		var r struct {
			SeasonNumber int    `json:"season_number"`
			Name         string `json:"name"`
			Episodes     []struct {
				EpisodeNumber int    `json:"episode_number"`
				Name          string `json:"name"`
				AirDate       string `json:"air_date"`
			} `json:"episodes"`
		}
		if err := c.get(ctx, fmt.Sprintf("/tv/%s/season/%d", id, season), url.Values{"language": {lang}}, &r); err != nil {
			return nil, err
		}
		s := &provider.SeasonDetails{Number: r.SeasonNumber, Name: r.Name, Episodes: []provider.Episode{}}
		for _, e := range r.Episodes {
			s.Episodes = append(s.Episodes, provider.Episode{Number: e.EpisodeNumber, Title: e.Name, AirDate: e.AirDate})
		}
		return s, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*provider.SeasonDetails), nil
}

func (c *Client) Check(ctx context.Context) error {
	var r map[string]any
	return c.get(ctx, "/configuration", nil, &r)
}

var (
	imgRefRe   = regexp.MustCompile(`^/[A-Za-z0-9_-]{1,64}\.(jpg|jpeg|png|webp)$`)
	imageSizes = map[string]bool{"w92": true, "w154": true, "w185": true, "w342": true, "w500": true}
)

func (c *Client) ValidImage(size, ref string) bool {
	return imageSizes[size] && imgRefRe.MatchString(ref)
}

// Image fetches a poster from the TMDB image CDN. No API key is sent.
func (c *Client) Image(ctx context.Context, size, ref string) (io.ReadCloser, string, error) {
	if !c.ValidImage(size, ref) {
		return nil, "", provider.ErrNotFound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.imgBase+"/"+size+ref, nil)
	if err != nil {
		return nil, "", provider.ErrUnavailable
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", provider.ErrUnavailable, redact(err))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, "", provider.ErrNotFound
		}
		return nil, "", fmt.Errorf("%w: HTTP %d", provider.ErrUnavailable, resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "image/jpeg"), strings.HasPrefix(ct, "image/png"), strings.HasPrefix(ct, "image/webp"):
	default:
		resp.Body.Close()
		return nil, "", fmt.Errorf("%w: unexpected content type", provider.ErrUnavailable)
	}
	return resp.Body, strings.SplitN(ct, ";", 2)[0], nil
}
