// Package provider defines the metadata provider interface. TMDB is the only
// implementation today; TVDB or others can be added behind the same interface.
package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Kind of media.
type Kind string

const (
	Movie Kind = "movie"
	TV    Kind = "tv"
)

// Summary is one search result.
type Summary struct {
	ID       string `json:"id"`
	Kind     Kind   `json:"kind"`
	Title    string `json:"title"`
	Original string `json:"originalTitle,omitempty"`
	Year     int    `json:"year,omitempty"`
	Overview string `json:"overview,omitempty"`
	Poster   string `json:"poster,omitempty"` // provider image reference, e.g. "/abc.jpg"
}

type SearchResult struct {
	Results    []Summary `json:"results"`
	Page       int       `json:"page"`
	TotalPages int       `json:"totalPages"`
}

type MovieDetails struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Original string `json:"originalTitle,omitempty"`
	Year     int    `json:"year,omitempty"`
	Overview string `json:"overview,omitempty"`
	Poster   string `json:"poster,omitempty"`
	Runtime  int    `json:"runtime,omitempty"`
}

type SeasonSummary struct {
	Number       int    `json:"number"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episodeCount"`
	AirDate      string `json:"airDate,omitempty"`
	Poster       string `json:"poster,omitempty"`
}

type ShowDetails struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Original string          `json:"originalTitle,omitempty"`
	Year     int             `json:"year,omitempty"`
	Overview string          `json:"overview,omitempty"`
	Poster   string          `json:"poster,omitempty"`
	Seasons  []SeasonSummary `json:"seasons"`
}

type Episode struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	AirDate string `json:"airDate,omitempty"`
}

type SeasonDetails struct {
	Number   int       `json:"number"`
	Name     string    `json:"name"`
	Episodes []Episode `json:"episodes"`
}

// Provider is a metadata source.
type Provider interface {
	Name() string
	// Attribution is the notice the provider's terms require in the UI.
	Attribution() Attribution
	Search(ctx context.Context, kind Kind, query, lang string, page int) (*SearchResult, error)
	Movie(ctx context.Context, id, lang string) (*MovieDetails, error)
	Show(ctx context.Context, id, lang string) (*ShowDetails, error)
	Season(ctx context.Context, id string, season int, lang string) (*SeasonDetails, error)
	// ValidID reports whether id is syntactically valid for this provider.
	ValidID(id string) bool
	// Image fetches an image by reference and size.
	Image(ctx context.Context, size, ref string) (io.ReadCloser, string, error)
	// ValidImage reports whether size and ref are acceptable.
	ValidImage(size, ref string) bool
	// Check verifies that credentials work.
	Check(ctx context.Context) error
}

type Attribution struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// Error kinds.
var (
	ErrNotFound      = errors.New("not found")
	ErrUnauthorized  = errors.New("provider rejected the API key")
	ErrUnavailable   = errors.New("provider unavailable")
	ErrNotConfigured = errors.New("provider API key not configured")
)

// RateLimitError is returned when the provider answered 429.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("provider rate limit reached, retry after %s", e.RetryAfter)
}
