package parser

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed packs/*.yaml
var builtinPacks embed.FS

// Pack is a named, optional rule set (usually one language).
type Pack struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // builtin | file
	Rules       Rules  `json:"-"`
}

type packFile struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Rules       `yaml:",inline"`
}

const maxYAML = 64 << 10

// ParseYAML reads a rule set in the pack format (name and description are
// optional) and validates it. Unknown keys are rejected.
func ParseYAML(b []byte) (name, description string, r Rules, err error) {
	if len(b) > maxYAML {
		return "", "", r, errors.New("rules file too large")
	}
	var pf packFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&pf); err != nil {
		return "", "", r, fmt.Errorf("YAML: %w", err)
	}
	if len(pf.Episode) == 0 && len(pf.Season) == 0 && len(pf.Junk) == 0 {
		return "", "", r, errors.New("no rules")
	}
	if err := pf.Rules.validatePartial(); err != nil {
		return "", "", r, err
	}
	return pf.Name, pf.Description, pf.Rules, nil
}

// validatePartial validates a fragment that may lack episode rules.
func (r Rules) validatePartial() error {
	probe := r
	if len(probe.Episode) == 0 {
		probe.Episode = []Rule{{Name: "probe", Pattern: `(?<episode>\d+)`}}
	}
	return probe.Validate()
}

var packIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// LoadPacks returns the built-in packs plus *.yaml files in userDir. A file
// whose name matches a built-in pack replaces it. Broken files are skipped and
// reported in the returned error (packs are still returned).
func LoadPacks(userDir string) ([]Pack, error) {
	byID := map[string]Pack{}
	entries, _ := builtinPacks.ReadDir("packs")
	for _, e := range entries {
		b, _ := builtinPacks.ReadFile("packs/" + e.Name())
		name, desc, r, err := ParseYAML(b)
		if err != nil {
			panic(fmt.Sprintf("parser: built-in pack %s: %v", e.Name(), err))
		}
		id := strings.TrimSuffix(e.Name(), ".yaml")
		byID[id] = Pack{ID: id, Name: name, Description: desc, Source: "builtin", Rules: r}
	}
	var errs []error
	if userDir != "" {
		files, err := os.ReadDir(userDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for _, f := range files {
			id, ok := strings.CutSuffix(f.Name(), ".yaml")
			if !ok || !f.Type().IsRegular() {
				continue
			}
			if !packIDRe.MatchString(id) {
				errs = append(errs, fmt.Errorf("%s: invalid pack name (use a-z, 0-9, - and _)", f.Name()))
				continue
			}
			b, err := os.ReadFile(filepath.Join(userDir, f.Name()))
			if err == nil {
				var name, desc string
				var r Rules
				if name, desc, r, err = ParseYAML(b); err == nil {
					if name == "" {
						name = id
					}
					byID[id] = Pack{ID: id, Name: name, Description: desc, Source: "file", Rules: r}
					continue
				}
			}
			errs = append(errs, fmt.Errorf("%s: %w", f.Name(), err))
		}
	}
	out := make([]Pack, 0, len(byID))
	for _, p := range byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, errors.Join(errs...)
}

// Compose builds the effective rule set: custom rules first, then enabled
// packs in the given order, then the defaults. Specific rules must come
// before the generic catch-alls at the end of the defaults.
func Compose(custom *Rules, packs []Pack, enabled []string) Rules {
	out := Rules{}
	add := func(r Rules) {
		out.Episode = append(out.Episode, r.Episode...)
		out.Season = append(out.Season, r.Season...)
		out.Junk = append(out.Junk, r.Junk...)
	}
	if custom != nil {
		add(*custom)
	}
	for _, id := range enabled {
		for _, p := range packs {
			if p.ID == id {
				add(p.Rules)
			}
		}
	}
	add(Default())
	return out
}

// BuiltinIDs lists the ids of the built-in packs.
func BuiltinIDs() []string {
	packs, _ := LoadPacks("")
	ids := make([]string, len(packs))
	for i, p := range packs {
		ids[i] = p.ID
	}
	return ids
}
