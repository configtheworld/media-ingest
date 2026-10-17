package ingest

import "testing"

func TestParseLibraryName(t *testing.T) {
	tests := []struct {
		name            string
		season, ep, end int
		ok              bool
	}{
		{"Show - S01E02 - Title.mkv", 1, 2, 0, true},
		{"Show.S01E02E03.1080p.mkv", 1, 2, 3, true},
		{"Show S01E02-E03.mkv", 1, 2, 3, true},
		{"Show S01E02-03.mkv", 1, 2, 3, true},
		{"show.s10e100.mkv", 10, 100, 0, true},
		{"Show S01E02-1080p.mkv", 1, 2, 0, true},
		{"Show 1x02.mkv", 1, 2, 0, true},
		{"Show 3x10-11.mkv", 3, 10, 11, true},
		{"Show 1920x1080.mkv", 0, 0, 0, false},
		{"Show S00E01.mkv", 0, 1, 0, true},
		{"Movie (2010).mkv", 0, 0, 0, false},
		{"Show S01E05E99.mkv", 1, 5, 0, true}, // absurd range ignored
	}
	for _, tt := range tests {
		s, e, end, ok := parseLibraryName(tt.name)
		if ok != tt.ok || ok && (s != tt.season || e != tt.ep || end != tt.end) {
			t.Errorf("%s: got %d %d %d %v", tt.name, s, e, end, ok)
		}
	}
}
