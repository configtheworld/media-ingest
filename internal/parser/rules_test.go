package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultRulesValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]Rules{
		"no episode rules": {},
		"lookahead":        {Episode: []Rule{{"x", `e(?<episode>\d+)(?!p)`}}},
		"backreference":    {Episode: []Rule{{"x", `(?<episode>\d)\1`}}},
		"missing group":    {Episode: []Rule{{"x", `e(\d+)`}}},
		"unknown group":    {Episode: []Rule{{"x", `(?<episode>\d+)(?<evil>.)`}}},
		"season no group":  {Episode: []Rule{{"x", `(?<episode>\d+)`}}, Season: []Rule{{"s", `season`}}},
		"too long":         {Episode: []Rule{{"x", `(?<episode>\d+)` + strings.Repeat("a", 600)}}},
		"bad junk":         {Episode: []Rule{{"x", `(?<episode>\d+)`}}, Junk: []string{`(`}},
	}
	for name, r := range cases {
		if r.Validate() == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestBuiltinPacks(t *testing.T) {
	packs, err := LoadPacks("")
	if err != nil {
		t.Fatal(err)
	}
	if ids := BuiltinIDs(); strings.Join(ids, ",") != "de,es,tr" {
		t.Fatalf("ids %v", ids)
	}
	all := Compose(nil, packs, BuiltinIDs())
	if err := all.Validate(); err != nil {
		t.Fatal(err)
	}
	if all.Episode[len(all.Episode)-1].Name != "Title 01" {
		t.Fatal("defaults (with the catch-all rules) must come last")
	}
}

func TestUserPacks(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "de.yaml"), []byte("name: Mein Deutsch\nepisode:\n  - name: x\n    pattern: 'folge(?<episode>\\d+)'\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "it.yaml"), []byte("episode:\n  - name: Puntata\n    pattern: 'puntata[ ._-]*(?<episode>\\d+)'\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("episode:\n  - name: x\n    pattern: '(?!x)(?<episode>\\d)'\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "typo.yaml"), []byte("episodes: []\n"), 0o644)
	packs, err := LoadPacks(dir)
	if err == nil || !strings.Contains(err.Error(), "bad.yaml") || !strings.Contains(err.Error(), "typo.yaml") {
		t.Fatalf("expected errors for broken files, got %v", err)
	}
	byID := map[string]Pack{}
	for _, p := range packs {
		byID[p.ID] = p
	}
	if byID["de"].Source != "file" || byID["de"].Name != "Mein Deutsch" || byID["it"].Name != "it" || byID["tr"].Source != "builtin" {
		t.Fatalf("%+v", byID)
	}
	if _, ok := byID["bad"]; ok {
		t.Fatal("broken pack must be skipped")
	}
}

func TestParseYAMLRejects(t *testing.T) {
	for _, src := range []string{"", "episode: [", "unknown: 1", "episode:\n  - name: x\n    pattern: '(\\d+)'\n", strings.Repeat("#", 70000)} {
		if _, _, _, err := ParseYAML([]byte(src)); err == nil {
			t.Errorf("accepted %q", src[:min(len(src), 30)])
		}
	}
}
