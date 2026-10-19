// Package fake is an in-memory metadata provider for tests.
package fake

import (
	"context"
	"io"
	"strings"

	"github.com/configtheworld/media-digest/internal/provider"
)

type Provider struct {
	Movies   map[string]*provider.MovieDetails
	Shows    map[string]*provider.ShowDetails
	Seasons  map[string]map[int]*provider.SeasonDetails
	CheckErr error
}

func New() *Provider {
	return &Provider{Movies: map[string]*provider.MovieDetails{
		"27205": {ID: "27205", Title: "Inception", Year: 2010},
		"100":   {ID: "100", Title: "Amélie: The Fabulous / Destiny?", Original: "Le Fabuleux Destin d'Amélie Poulain", Year: 2001},
		"200":   {ID: "200", Title: "Untitled", Year: 0},
	},
		Shows: map[string]*provider.ShowDetails{
			"9001": {ID: "9001", Title: "Harbor Lights", Year: 2019, Seasons: []provider.SeasonSummary{
				{Number: 0, Name: "Specials", EpisodeCount: 2}, {Number: 1, Name: "Season 1", EpisodeCount: 6}, {Number: 2, Name: "Season 2", EpisodeCount: 4}}},
		},
		Seasons: map[string]map[int]*provider.SeasonDetails{
			"9001": {
				0: {Number: 0, Name: "Specials", Episodes: eps("Behind the Harbor", "Lost Tapes")},
				1: {Number: 1, Name: "Season 1", Episodes: eps("Arrival", "Fog", "Undertow", "Low Tide", "Beacon", "Homecoming")},
				2: {Number: 2, Name: "Season 2", Episodes: eps("Return", "Drift", "Storm", "Dawn")},
			},
		},
	}
}

func eps(titles ...string) []provider.Episode {
	out := make([]provider.Episode, len(titles))
	for i, t := range titles {
		out[i] = provider.Episode{Number: i + 1, Title: t, AirDate: "2019-01-" + string(rune('1'+i/9)) + string(rune('1'+i%9))}
	}
	return out
}

func (p *Provider) Name() string { return "tmdb" }
func (p *Provider) Attribution() provider.Attribution {
	return provider.Attribution{Text: "fake", URL: "https://example.com"}
}
func (p *Provider) ValidID(id string) bool {
	return id != "" && strings.Trim(id, "0123456789") == "" && id[0] != '0'
}
func (p *Provider) ValidImage(size, ref string) bool { return false }
func (p *Provider) Check(context.Context) error      { return p.CheckErr }

func (p *Provider) Search(_ context.Context, kind provider.Kind, q, _ string, _ int) (*provider.SearchResult, error) {
	res := &provider.SearchResult{Page: 1, TotalPages: 1, Results: []provider.Summary{}}
	for _, m := range p.Movies {
		if strings.Contains(strings.ToLower(m.Title), strings.ToLower(q)) {
			res.Results = append(res.Results, provider.Summary{ID: m.ID, Kind: kind, Title: m.Title, Year: m.Year})
		}
	}
	return res, nil
}

func (p *Provider) Movie(_ context.Context, id, _ string) (*provider.MovieDetails, error) {
	if m, ok := p.Movies[id]; ok {
		return m, nil
	}
	return nil, provider.ErrNotFound
}

func (p *Provider) Show(_ context.Context, id, _ string) (*provider.ShowDetails, error) {
	if s, ok := p.Shows[id]; ok {
		return s, nil
	}
	return nil, provider.ErrNotFound
}

func (p *Provider) Season(_ context.Context, id string, n int, _ string) (*provider.SeasonDetails, error) {
	if s, ok := p.Seasons[id][n]; ok {
		return s, nil
	}
	return nil, provider.ErrNotFound
}

func (p *Provider) Image(context.Context, string, string) (io.ReadCloser, string, error) {
	return nil, "", provider.ErrNotFound
}
