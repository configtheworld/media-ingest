package server

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/configtheworld/media-digest/internal/config"
	"github.com/configtheworld/media-digest/internal/ingest"
)

const show = "tv/Quillmoor Bay (2019) [tmdbid-900100]"

func tvReq(items ...map[string]any) map[string]any {
	return map[string]any{"provider": "tmdb", "id": "900100", "language": "en-US", "folder": map[string]any{"mode": "auto"}, "items": items}
}

func ep(key, name string, season, episode int, extra ...any) map[string]any {
	m := map[string]any{"key": key, "name": name, "size": 4, "season": season, "episode": episode}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

func paths(p ingest.Plan) map[string]string {
	out := map[string]string{}
	for _, it := range p.Items {
		out[it.Key] = it.Path
	}
	return out
}

func hasWarning(p ingest.Plan, code string) bool {
	for _, w := range p.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestTVPlanNaming(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	var plan ingest.Plan
	code := e.json("POST", "/api/plan/tv", tvReq(
		ep("a", "x.mkv", 1, 1),
		ep("s", "x.en.srt", 1, 1, "lang", "en"),
		ep("b", "y.mkv", 1, 3, "episodeEnd", 4),
		ep("c", "special.mp4", 0, 1),
	), &plan)
	if code != 200 {
		t.Fatalf("plan: %d", code)
	}
	want := map[string]string{
		"a": show + "/Season 01/Quillmoor Bay - S01E01 - Arrival.mkv",
		"s": show + "/Season 01/Quillmoor Bay - S01E01 - Arrival.en.srt",
		"b": show + "/Season 01/Quillmoor Bay - S01E03-E04 - Undertow & Low Tide.mkv",
		"c": show + "/Season 00/Quillmoor Bay - S00E01 - Behind the Bay.mp4",
	}
	got := paths(plan)
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s:\n got %q\nwant %q", k, got[k], w)
		}
	}
	if !hasWarning(plan, "multi_episode_files") || !hasWarning(plan, "gaps") {
		t.Errorf("warnings: %+v", plan.Warnings)
	}
}

func TestTVAlternateVersionsRequired(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	var apiErr apiError
	code := e.json("POST", "/api/plan/tv", tvReq(ep("a", "a.mkv", 1, 2), ep("b", "b.mkv", 1, 2)), &apiErr)
	if code != 400 || apiErr.Error != "alternate_version_required" {
		t.Fatalf("%d %+v", code, apiErr)
	}
	var plan ingest.Plan
	e.json("POST", "/api/plan/tv", tvReq(ep("a", "a.mkv", 1, 2, "version", "1080p"), ep("b", "b.mkv", 1, 2, "version", "2160p"),
		ep("s", "b.de.srt", 1, 2, "version", "2160p", "lang", "de")), &plan)
	got := paths(plan)
	if got["a"] != show+"/Season 01/Quillmoor Bay - S01E02 - Fog - 1080p.mkv" ||
		got["b"] != show+"/Season 01/Quillmoor Bay - S01E02 - Fog - 2160p.mkv" ||
		got["s"] != show+"/Season 01/Quillmoor Bay - S01E02 - Fog - 2160p.de.srt" {
		t.Fatalf("%v", got)
	}
}

func TestTVValidation(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	cases := map[string]struct {
		req  map[string]any
		code string
	}{
		"unknown season": {tvReq(ep("a", "a.mkv", 7, 1)), "unknown_season"},
		"episode zero":   {tvReq(ep("a", "a.mkv", 1, 0)), "invalid_episode"},
		"bad range":      {tvReq(ep("a", "a.mkv", 1, 3, "episodeEnd", 2)), "invalid_episode_range"},
		"huge range":     {tvReq(ep("a", "a.mkv", 1, 1, "episodeEnd", 99)), "invalid_episode_range"},
		"junk":           {tvReq(ep("a", "._a.mkv", 1, 1)), "junk_file"},
		"duplicate subs": {tvReq(ep("a", "a.mkv", 1, 1), ep("s", "a.en.srt", 1, 1, "lang", "en"), ep("t", "b.en.srt", 1, 1, "lang", "en")), "duplicate_target"},
	}
	for name, c := range cases {
		var apiErr apiError
		if code := e.json("POST", "/api/plan/tv", c.req, &apiErr); code != 400 || apiErr.Error != c.code {
			t.Errorf("%s: %d %q want %q", name, code, apiErr.Error, c.code)
		}
	}
	var plan ingest.Plan
	e.json("POST", "/api/plan/tv", tvReq(ep("a", "a.mkv", 1, 9)), &plan)
	if !hasWarning(plan, "unknown_episodes") {
		t.Errorf("unknown episode warning missing: %+v", plan.Warnings)
	}
}

func TestTVLibraryAwarenessAndReplace(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	old := show + "/Season 01/Quillmoor.Bay.S01E02.720p.mkv"
	os.MkdirAll(filepath.Join(e.media, filepath.Dir(old)), 0o755)
	os.WriteFile(filepath.Join(e.media, old), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(e.media, show, "Season 01", "Quillmoor.Bay.S01E05E06.mkv"), []byte("x"), 0o644)

	var lib ingest.LibraryView
	if code := e.json("GET", "/api/library/tmdb/tv/900100?lang=en-US", nil, &lib); code != 200 {
		t.Fatalf("library: %d", code)
	}
	have := map[string]bool{}
	for _, x := range lib.Episodes {
		have[strings.Join([]string{itoa(x.Season), itoa(x.Episode)}, "x")] = true
	}
	if !have["1x2"] || !have["1x5"] || !have["1x6"] || len(lib.Episodes) != 3 || !lib.FolderExists {
		t.Fatalf("library: %+v", lib)
	}

	// The same episode under another name is a conflict that needs a decision.
	req := tvReq(ep("a", "new.mkv", 1, 2), ep("s", "x.en.srt", 1, 5, "lang", "en"))
	var plan ingest.Plan
	e.json("POST", "/api/plan/tv", req, &plan)
	if !plan.Items[0].Conflict || len(plan.Items[0].Existing) != 1 || plan.Items[0].Existing[0] != old {
		t.Fatalf("existing episode not detected: %+v", plan.Items[0])
	}
	// A subtitle for an episode that exists only in the library follows that file's name.
	if got := paths(plan)["s"]; got != show+"/Season 01/Quillmoor.Bay.S01E05E06.en.srt" {
		t.Fatalf("subtitle path %q", got)
	}
	var apiErr apiError
	if code := e.json("POST", "/api/batches/tv", req, &apiErr); code != 400 || apiErr.Error != "conflict_policy_required" {
		t.Fatalf("%d %+v", code, apiErr)
	}

	req["items"].([]map[string]any)[0]["policy"] = "replace"
	var batch ingest.BatchView
	if code := e.json("POST", "/api/batches/tv", req, &batch); code != 201 {
		t.Fatalf("batch: %d", code)
	}
	for _, it := range batch.Items {
		e.upload(batch.ID, it.ID, []byte("new!"))
	}
	var got ingest.BatchView
	e.json("GET", "/api/batches/"+batch.ID, nil, &got)
	if got.Status != "done" {
		t.Fatalf("%+v", got)
	}
	newPath := show + "/Season 01/Quillmoor Bay - S01E02 - Fog.mkv"
	if b, _ := os.ReadFile(filepath.Join(e.media, newPath)); string(b) != "new!" {
		t.Fatalf("new file %q", b)
	}
	if _, err := os.Stat(filepath.Join(e.media, old)); !os.IsNotExist(err) {
		t.Fatal("old episode file should have moved to trash")
	}
	var trashed string
	for _, it := range got.Items {
		if it.Key == "a" {
			trashed = it.TrashedPath
		}
	}
	if b, err := os.ReadFile(filepath.Join(e.media, trashed)); err != nil || string(b) != "old" {
		t.Fatalf("trash %q: %v", trashed, err)
	}
	if !strings.HasPrefix(trashed, ".trash/") {
		t.Fatalf("trash path %q", trashed)
	}
}

func TestTVKeepBothWithExistingEpisode(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	target := show + "/Season 01/Quillmoor Bay - S01E01 - Arrival.mkv"
	os.MkdirAll(filepath.Join(e.media, filepath.Dir(target)), 0o755)
	os.WriteFile(filepath.Join(e.media, target), []byte("old"), 0o644)
	req := tvReq(ep("a", "a.mkv", 1, 1, "policy", "keep_both"))
	var batch ingest.BatchView
	e.json("POST", "/api/batches/tv", req, &batch)
	e.upload(batch.ID, batch.Items[0].ID, []byte("new!"))
	var got ingest.BatchView
	e.json("GET", "/api/batches/"+batch.ID, nil, &got)
	if p := got.Items[0].FinalPath; p != show+"/Season 01/Quillmoor Bay - S01E01 - Arrival - Version 2.mkv" {
		t.Fatalf("%q", p)
	}
	if b, _ := os.ReadFile(filepath.Join(e.media, target)); string(b) != "old" {
		t.Fatal("original overwritten")
	}
}

func TestTVFolderReuse(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	os.MkdirAll(filepath.Join(e.media, "tv", "Quillmoor Bay {tmdb-900100}", "Season 1"), 0o755)
	var plan ingest.Plan
	e.json("POST", "/api/plan/tv", tvReq(ep("a", "a.mkv", 2, 1)), &plan)
	if plan.FolderSource != "existing-id" || paths(plan)["a"] != "tv/Quillmoor Bay {tmdb-900100}/Season 02/Quillmoor Bay - S02E01 - Return.mkv" {
		t.Fatalf("%+v", plan)
	}
}

func TestParserRulesEndpoint(t *testing.T) {
	e := newEnv(t, config.AuthProxy)
	var rules struct {
		Episode []struct{ Name, Pattern string }
		Junk    []string
	}
	if code := e.json("GET", "/api/parser/rules", nil, &rules); code != 200 || len(rules.Episode) < 5 || len(rules.Junk) == 0 {
		t.Fatalf("%d %+v", code, rules)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
