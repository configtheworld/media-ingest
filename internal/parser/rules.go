// Package parser holds the episode-number rules used by the browser to map
// file names to episodes. Patterns are written in the common subset of RE2
// (Go) and ECMAScript: named groups as (?<name>...), no lookarounds, no
// backreferences. The server validates every pattern with Go's regexp, which
// guarantees linear-time matching, before handing rules to browsers.
// All patterns are matched case-insensitively.
package parser

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// Rule is one named pattern.
type Rule struct {
	Name    string `json:"name" yaml:"name"`
	Pattern string `json:"pattern" yaml:"pattern"`
}

// Rules is an ordered rule set. The first matching episode rule wins.
type Rules struct {
	// Episode rules must capture "episode"; they may capture "season" and "episodeEnd".
	Episode []Rule `json:"episode" yaml:"episode"`
	// Season rules find a season in a folder name or file name. They capture
	// "season", or "specials" for Season 0.
	Season []Rule `json:"season" yaml:"season"`
	// Junk patterns are removed from the name before episode rules run
	// (resolutions, codecs, years, release tags).
	Junk []string `json:"junk" yaml:"junk"`
}

//go:embed default.json
var defaultJSON []byte

// Default returns the built-in rules (English and language-neutral forms).
// default.json is also read by the browser tests, so both sides test the
// exact same patterns.
func Default() Rules {
	var r Rules
	if err := json.Unmarshal(defaultJSON, &r); err != nil {
		panic("parser: invalid default.json: " + err.Error())
	}
	return r
}

var groupsAllowed = map[string]bool{"season": true, "episode": true, "episodeEnd": true, "specials": true}

// Validate compiles every pattern with RE2 and checks named groups.
func (r Rules) Validate() error {
	var errs []error
	if len(r.Episode) == 0 {
		errs = append(errs, errors.New("at least one episode rule is required"))
	}
	if len(r.Episode)+len(r.Season)+len(r.Junk) > 200 {
		errs = append(errs, errors.New("too many rules"))
	}
	check := func(kind, name, pattern string, need ...string) {
		if len(pattern) == 0 || len(pattern) > 500 {
			errs = append(errs, fmt.Errorf("%s rule %q: pattern must be 1-500 characters", kind, name))
			return
		}
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s rule %q: %v", kind, name, err))
			return
		}
		have := map[string]bool{}
		for _, g := range re.SubexpNames() {
			if g == "" {
				continue
			}
			if !groupsAllowed[g] {
				errs = append(errs, fmt.Errorf("%s rule %q: unknown group %q", kind, name, g))
			}
			have[g] = true
		}
		ok := len(need) == 0
		for _, n := range need {
			ok = ok || have[n]
		}
		if !ok {
			errs = append(errs, fmt.Errorf("%s rule %q: must capture one of %v", kind, name, need))
		}
	}
	for _, x := range r.Episode {
		check("episode", x.Name, x.Pattern, "episode")
	}
	for _, x := range r.Season {
		check("season", x.Name, x.Pattern, "season", "specials")
	}
	for i, p := range r.Junk {
		check("junk", fmt.Sprintf("#%d", i+1), p)
	}
	return errors.Join(errs...)
}
