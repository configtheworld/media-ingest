package naming

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeComponent(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Mission: Impossible", "Mission - Impossible"},
		{"AC/DC", "AC-DC"},
		{`What? Why* "Now"`, "What Why 'Now'"},
		{"trailing dots...", "trailing dots"},
		{"  spaced   out  ", "spaced out"},
		{"..", ""},
		{".", ""},
		{".hidden", "hidden"},
		{"CON", "CON_"},
		{"con.txt", "con.txt_"},
		{"LPT1", "LPT1_"},
		{"COM¹", "COM¹_"},
		{"Console", "Console"},
		{"a\x00b", "ab"},
		{"tab\there", "tab here"},
		{"evil\u202Egnp.exe", "evilgnp.exe"}, // RTL override removed
		{"zero\u200Bwidth", "zerowidth"},     // zero-width space removed
		{"fake\u2215slash", "fake-slash"},    // division slash
		{"full\uFF0Fwidth", "full-width"},    // fullwidth solidus
		{"E\u0301mile", "Émile"},             // NFD -> NFC
		{"Şahsiyet", "Şahsiyet"},             // non-ASCII kept
		{"a|b", "a-b"},
		{"<script>", "script"},
	}
	for _, tt := range tests {
		if got := SanitizeComponent(tt.in); got != tt.want {
			t.Errorf("SanitizeComponent(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeComponentLength(t *testing.T) {
	long := strings.Repeat("ğ", 300) // 2 bytes each
	got := SanitizeComponent(long)
	if len(got) > MaxComponentBytes || !utf8.ValidString(got) {
		t.Fatalf("len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestIsJunkName(t *testing.T) {
	for _, n := range []string{"._movie.mkv", ".DS_Store", "dir/._x.srt", "Thumbs.db", "desktop.ini"} {
		if !IsJunkName(n) {
			t.Errorf("%q should be junk", n)
		}
	}
	for _, n := range []string{"movie.mkv", "_x.mkv", ".hidden.mkv"} {
		if IsJunkName(n) {
			t.Errorf("%q should not be junk", n)
		}
	}
}

func TestExt(t *testing.T) {
	tests := map[string]string{"a.MKV": "mkv", "dir.x/file": "", ".mkv": "", "a.": "", "x.tar.gz": "gz"}
	for in, want := range tests {
		if got := Ext(in); got != want {
			t.Errorf("Ext(%q)=%q want %q", in, got, want)
		}
	}
}

func TestPresetsRender(t *testing.T) {
	movie := Vars{"title": "The Clockwork Garden", "year": 2015, "id": "900001"}
	tv := Vars{"show": "Northwind", "year": 2017, "id": "900200", "season": 1, "episode": 3, "episode_title": "Old Tides"}
	tests := []struct {
		preset, kind string
		vars         Vars
		want         string
	}{
		{"jellyfin", "movie", movie, "The Clockwork Garden (2015) [tmdbid-900001]/The Clockwork Garden (2015) [tmdbid-900001]"},
		{"plex", "movie", movie, "The Clockwork Garden (2015) {tmdb-900001}/The Clockwork Garden (2015) {tmdb-900001}"},
		{"emby", "movie", movie, "The Clockwork Garden (2015) [tmdbid=900001]/The Clockwork Garden (2015)"},
		{"jellyfin", "tv", tv, "Northwind (2017) [tmdbid-900200]/Season 01/Northwind - S01E03 - Old Tides"},
		{"plex", "tv", tv, "Northwind (2017) {tmdb-900200}/Season 01/Northwind (2017) - s01e03 - Old Tides"},
	}
	for _, tt := range tests {
		p := Presets[tt.preset]
		src, allowed := p.Movie, MovieVars
		if tt.kind == "tv" {
			src, allowed = p.TV, TVVars
		}
		tpl, err := ParseTemplate(src, allowed)
		if err != nil {
			t.Fatalf("%s/%s: %v", tt.preset, tt.kind, err)
		}
		segs, err := tpl.Render(tt.vars, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(segs, "/"); got != tt.want {
			t.Errorf("%s/%s:\n got %q\nwant %q", tt.preset, tt.kind, got, tt.want)
		}
	}
}

func TestTemplateOptionalGroups(t *testing.T) {
	tpl, err := ParseTemplate(Presets["jellyfin"].TV, TVVars)
	if err != nil {
		t.Fatal(err)
	}
	segs, _ := tpl.Render(Vars{"show": "X", "id": "1", "season": 2, "episode": 5, "episode_end": 6}, 0)
	if got := segs[2]; got != "X - S02E05-E06" {
		t.Errorf("range: %q", got)
	}
	segs, _ = tpl.Render(Vars{"show": "X", "id": "1", "season": 0, "episode": 1}, 0)
	// season 0 (specials) must still render as 00, not vanish
	if got := strings.Join(segs, "/"); got != "X [tmdbid-1]/Season 00/X - S00E01" {
		t.Errorf("specials: %q", got)
	}
}

func TestTemplateValueInjection(t *testing.T) {
	tpl, _ := ParseTemplate(Presets["jellyfin"].Movie, MovieVars)
	segs, err := tpl.Render(Vars{"title": "../../etc/passwd", "year": 2000, "id": "1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("value injected a separator: %q", segs)
	}
	for _, s := range segs {
		if strings.Contains(s, "/") || s == ".." || strings.HasPrefix(s, ".") {
			t.Errorf("unsafe segment %q", s)
		}
	}
}

func TestTemplateTruncation(t *testing.T) {
	tpl, _ := ParseTemplate(Presets["jellyfin"].TV, TVVars)
	vars := Vars{"show": "Show", "id": "1", "season": 1, "episode": 1, "episode_title": strings.Repeat("Very long title ", 40)}
	segs, err := tpl.Render(vars, 40)
	if err != nil {
		t.Fatal(err)
	}
	last := segs[len(segs)-1]
	if len(last) > MaxComponentBytes-40 {
		t.Errorf("last segment %d bytes", len(last))
	}
	if !strings.HasPrefix(last, "Show - S01E01 - Very long title") {
		t.Errorf("structure lost: %q", last)
	}
}

func TestParseTemplateErrors(t *testing.T) {
	bad := []string{
		"", "{nope}", "{title", "{title:x}", "<{title}", "{title}>", "../{title}", "{title}//{id}",
		"a:b/{title}", "{title}}", "{year:2}",
	}
	for _, src := range bad {
		if _, err := ParseTemplate(src, MovieVars); err == nil {
			t.Errorf("ParseTemplate(%q) should fail", src)
		}
	}
	if _, err := ParseTemplate("{title} ({year})/{title}.{ext}", MovieVars); err != nil {
		t.Errorf("trailing .{ext} should be accepted: %v", err)
	}
}
