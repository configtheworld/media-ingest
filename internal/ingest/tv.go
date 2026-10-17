package ingest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/media-ingest/media-ingest/internal/config"
	"github.com/media-ingest/media-ingest/internal/naming"
	"github.com/media-ingest/media-ingest/internal/provider"
)

// Episode roles.
const (
	RoleEpisode = "episode"
	// RoleSubtitle is shared with movies.
)

const maxRange = 20 // a single file may cover at most this many extra episodes

// TVRequest describes episodes to ingest. Like MovieRequest it contains no
// paths: season and episode numbers plus provider metadata determine names.
type TVRequest struct {
	Provider string       `json:"provider"`
	ID       string       `json:"id"`
	Language string       `json:"language"`
	Title    string       `json:"title"` // optional display title override
	Folder   FolderChoice `json:"folder"`
	Items    []TVItem     `json:"items"`
}

type TVItem struct {
	Key        string `json:"key"`
	Name       string `json:"name"` // original file name: extension and display only
	Size       int64  `json:"size"`
	Season     int    `json:"season"` // 0 = specials
	Episode    int    `json:"episode"`
	EpisodeEnd int    `json:"episodeEnd"` // 0, or the last episode a multi-episode file covers
	Version    string `json:"version"`    // alternate-version label; for subtitles: the version they belong to
	Lang       string `json:"lang"`       // subtitles
	Forced     bool   `json:"forced"`
	SDH        bool   `json:"sdh"`
	Policy     string `json:"policy"`
}

type epKey struct{ season, episode int }

func episodeLabel(season, ep, end int) string {
	if season < 0 {
		return ""
	}
	s := fmt.Sprintf("S%02dE%02d", season, ep)
	if end > ep {
		s += fmt.Sprintf("-E%02d", end)
	}
	return s
}

// --- library awareness ---

var (
	libSxxEyy = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,3})[ ._-]?e(\d{1,4})(?:[ ._]?-?[ ._]?e(\d{1,4})|-(\d{1,4})(?:[^0-9p]|$))?`)
	libNxNN   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})x(\d{2,3})(?:-(?:\d{1,2}x)?(\d{2,3}))?(?:[^0-9]|$)`)
)

// parseLibraryName extracts season and episode range from a file name that
// follows common library conventions (S01E02, S01E02-E03, 1x02).
func parseLibraryName(name string) (season, ep, end int, ok bool) {
	stem := naming.Stem(name)
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	if m := libSxxEyy.FindStringSubmatch(stem); m != nil {
		season, ep = atoi(m[1]), atoi(m[2])
		if m[3] != "" {
			end = atoi(m[3])
		} else if m[4] != "" {
			end = atoi(m[4])
		}
	} else if m := libNxNN.FindStringSubmatch(stem); m != nil {
		season, ep = atoi(m[1]), atoi(m[2])
		if m[3] != "" {
			end = atoi(m[3])
		}
	} else {
		return 0, 0, 0, false
	}
	if end <= ep || end-ep > maxRange {
		end = 0
	}
	return season, ep, end, ep > 0
}

// scanEpisodes maps (season, episode) to the video files of a show folder.
func (s *Service) scanEpisodes(showDir string) (map[epKey][]string, error) {
	out := map[epKey][]string{}
	files, err := s.fs.WalkFiles(showDir, 3)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if !config.IsVideo(naming.Ext(f)) {
			continue
		}
		season, ep, end, ok := parseLibraryName(path.Base(f))
		if !ok {
			continue
		}
		for e := ep; e <= max(ep, end); e++ {
			k := epKey{season, e}
			out[k] = append(out[k], f)
		}
	}
	return out, nil
}

// LibraryEpisode is an episode already present in the library.
type LibraryEpisode struct {
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
	Path    string `json:"path"`
}

// LibraryView tells the mapping UI where a show lives and what it has.
type LibraryView struct {
	LibraryDir       string           `json:"libraryDir"`
	Folder           string           `json:"folder"`
	FolderSource     string           `json:"folderSource"`
	FolderExists     bool             `json:"folderExists"`
	FolderCandidates []string         `json:"folderCandidates"`
	Episodes         []LibraryEpisode `json:"episodes"`
}

// showFolder renders the template to find the show's top-level folder name.
// reusable is false when that folder would depend on season or episode.
func showFolder(tpl *naming.Template, vars naming.Vars) (string, bool, error) {
	probe := func(season, ep int) ([]string, error) {
		v := naming.Vars{}
		for k, x := range vars {
			v[k] = x
		}
		v["season"], v["episode"] = season, ep
		return tpl.Render(v, 100)
	}
	a, err := probe(1, 1)
	if err != nil {
		return "", false, invalid("name_too_long", err.Error())
	}
	b, err := probe(2, 3)
	if err != nil {
		return "", false, invalid("name_too_long", err.Error())
	}
	return a[0], len(a) > 1 && len(b) > 1 && a[0] == b[0], nil
}

func showVars(show *provider.ShowDetails, title, providerName string) naming.Vars {
	v := naming.Vars{"show": title, "original_show": show.Original, "id": show.ID, "provider": providerName}
	if show.Year > 0 {
		v["year"] = show.Year
	}
	return v
}

func (s *Service) tvBasics(ctx context.Context, providerName, id, lang, titleOverride string) (provider.Provider, *provider.ShowDetails, string, error) {
	p, ok := s.providers[providerName]
	if !ok {
		return nil, nil, "", invalid("unknown_provider", providerName)
	}
	if !p.ValidID(id) {
		return nil, nil, "", invalid("invalid_id", "")
	}
	if !ValidLanguage(lang) {
		return nil, nil, "", invalid("invalid_language", "")
	}
	if len(titleOverride) > 512 {
		return nil, nil, "", invalid("invalid_title", "")
	}
	show, err := p.Show(ctx, id, lang)
	if err != nil {
		return nil, nil, "", err
	}
	title := show.Title
	if t := strings.TrimSpace(titleOverride); t != "" {
		title = t
	}
	if naming.SanitizeComponent(title) == "" {
		return nil, nil, "", invalid("invalid_title", "")
	}
	return p, show, title, nil
}

// TVLibrary resolves the show folder and lists episodes already present.
func (s *Service) TVLibrary(ctx context.Context, providerName, id, lang string, choice FolderChoice) (*LibraryView, error) {
	p, show, title, err := s.tvBasics(ctx, providerName, id, lang, "")
	if err != nil {
		return nil, err
	}
	nm, err := s.GetNaming(ctx)
	if err != nil {
		return nil, err
	}
	folder, reusable, err := showFolder(nm.TV, showVars(show, title, p.Name()))
	if err != nil {
		return nil, err
	}
	plan := &Plan{FolderCandidates: []string{}}
	if err := s.resolveFolder(plan, s.cfg.TVDir, choice, folder, reusable, show.ID, []string{title, show.Title, show.Original}, show.Year); err != nil {
		return nil, err
	}
	view := &LibraryView{LibraryDir: s.cfg.TVDir, Folder: plan.Folder, FolderSource: plan.FolderSource,
		FolderCandidates: plan.FolderCandidates, Episodes: []LibraryEpisode{}}
	if plan.Folder == "" {
		return view, nil
	}
	dir := path.Join(s.cfg.TVDir, plan.Folder)
	view.FolderExists, _ = s.fs.Exists(dir)
	eps, err := s.scanEpisodes(dir)
	if err != nil {
		return nil, err
	}
	for k, paths := range eps {
		for _, p := range paths {
			view.Episodes = append(view.Episodes, LibraryEpisode{Season: k.season, Episode: k.episode, Path: p})
		}
	}
	sort.Slice(view.Episodes, func(i, j int) bool {
		a, b := view.Episodes[i], view.Episodes[j]
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		return a.Path < b.Path
	})
	return view, nil
}

// --- planning ---

// PlanTV validates an episode request and computes final paths.
func (s *Service) PlanTV(ctx context.Context, req *TVRequest) (*Plan, error) {
	p, show, title, err := s.tvBasics(ctx, req.Provider, req.ID, req.Language, req.Title)
	if err != nil {
		return nil, err
	}
	if len(req.Items) == 0 || len(req.Items) > maxItems {
		return nil, invalid("invalid_item_count", "")
	}
	nm, err := s.GetNaming(ctx)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Kind: "tv", Provider: p.Name(), ID: show.ID, Title: title, ProviderTitle: show.Title, Year: show.Year,
		LibraryDir: s.cfg.TVDir, FolderCandidates: []string{}, Warnings: []Warning{}}
	baseVars := showVars(show, title, p.Name())
	folder, reusable, err := showFolder(nm.TV, baseVars)
	if err != nil {
		return nil, err
	}
	if err := s.resolveFolder(plan, s.cfg.TVDir, req.Folder, folder, reusable, show.ID, []string{title, show.Title, show.Original}, show.Year); err != nil {
		return nil, err
	}
	showDir := ""
	library := map[epKey][]string{}
	if plan.Folder != "" {
		showDir = path.Join(s.cfg.TVDir, plan.Folder)
		plan.FolderExists, _ = s.fs.Exists(showDir)
		if library, err = s.scanEpisodes(showDir); err != nil {
			return nil, err
		}
	}

	// Load provider data for every season used.
	known := map[int]bool{}
	for _, se := range show.Seasons {
		known[se.Number] = true
	}
	seasons := map[int]map[int]provider.Episode{}
	seen := map[string]bool{}
	for _, it := range req.Items {
		if seen[it.Key] {
			return nil, invalid("duplicate_key", it.Key)
		}
		seen[it.Key] = true
		if it.Season < 0 || it.Season > 1000 || !known[it.Season] {
			return nil, &ValidationError{Code: "unknown_season", Params: map[string]any{"key": it.Key, "season": it.Season}}
		}
		if it.Episode < 1 || it.Episode > 10000 {
			return nil, invalid("invalid_episode", it.Key)
		}
		if it.EpisodeEnd != 0 && (it.EpisodeEnd <= it.Episode || it.EpisodeEnd-it.Episode > maxRange) {
			return nil, invalid("invalid_episode_range", it.Key)
		}
		if _, ok := seasons[it.Season]; !ok {
			sd, err := p.Season(ctx, show.ID, it.Season, req.Language)
			if err != nil {
				return nil, err
			}
			eps := map[int]provider.Episode{}
			for _, e := range sd.Episodes {
				eps[e.Number] = e
			}
			seasons[it.Season] = eps
		}
	}

	// Video files first, so subtitles can follow their names.
	type group struct{ season, ep, end int }
	videoBase := map[group]map[string]string{} // group -> version label -> base path (no extension)
	groupKeys := map[group][]string{}
	labels := map[group][]string{}
	unknown := map[string]bool{}
	ranges := 0
	for _, it := range req.Items {
		ext, err := s.checkFile(it.Key, it.Name, it.Size)
		if err != nil {
			return nil, err
		}
		if !config.IsVideo(ext) {
			if !config.IsSubtitle(ext) {
				return nil, &ValidationError{Code: "role_extension_mismatch", Params: map[string]any{"key": it.Key, "ext": ext}}
			}
			continue
		}
		g := group{it.Season, it.Episode, it.EpisodeEnd}
		vars := naming.Vars{}
		for k, v := range baseVars {
			vars[k] = v
		}
		vars["season"], vars["episode"] = it.Season, it.Episode
		var titles []string
		for e := it.Episode; e <= max(it.Episode, it.EpisodeEnd); e++ {
			ep, ok := seasons[it.Season][e]
			if !ok {
				unknown[episodeLabel(it.Season, e, 0)] = true
				continue
			}
			if ep.Title != "" && !slices.Contains(titles, ep.Title) {
				titles = append(titles, ep.Title)
			}
			if e == it.Episode && ep.AirDate != "" {
				vars["air_date"] = ep.AirDate
			}
		}
		if it.EpisodeEnd > 0 {
			vars["episode_end"] = it.EpisodeEnd
			ranges++
		}
		vars["episode_title"] = strings.Join(titles, " & ")
		segs, err := nm.TV.Render(vars, 100)
		if err != nil {
			return nil, invalid("name_too_long", err.Error())
		}
		if plan.Folder != "" && reusable {
			segs[0] = plan.Folder
		}
		label := naming.SanitizeComponent(it.Version)
		base := path.Join(append([]string{s.cfg.TVDir}, segs...)...)
		if label != "" {
			base += " - " + label
		}
		pi := PlannedItem{Key: it.Key, Role: RoleEpisode, Name: it.Name, Ext: ext, Size: it.Size, Policy: it.Policy,
			Path: base + "." + ext, Episode: episodeLabel(it.Season, it.Episode, it.EpisodeEnd),
			altPrefix: base + " - Version ", altSuffix: "." + ext,
			season: it.Season, episode: it.Episode, episodeEnd: it.EpisodeEnd}
		for e := it.Episode; e <= max(it.Episode, it.EpisodeEnd); e++ {
			for _, existing := range library[epKey{it.Season, e}] {
				if existing != pi.Path && !slices.Contains(pi.Existing, existing) {
					pi.Existing = append(pi.Existing, existing)
				}
			}
		}
		if videoBase[g] == nil {
			videoBase[g] = map[string]string{}
		}
		videoBase[g][label] = base
		groupKeys[g] = append(groupKeys[g], it.Key)
		labels[g] = append(labels[g], label)
		plan.Items = append(plan.Items, pi)
	}

	// Two files for one episode are merged into one item by Jellyfin, so
	// they must be explicit alternate versions with distinct labels.
	versions := 0
	for g, ls := range labels {
		if len(ls) < 2 {
			continue
		}
		uniq := map[string]bool{}
		for _, l := range ls {
			uniq[l] = true
		}
		if len(uniq) != len(ls) {
			return nil, &ValidationError{Code: "alternate_version_required", Params: map[string]any{
				"keys": groupKeys[g], "episode": episodeLabel(g.season, g.ep, g.end)}}
		}
		versions += len(ls)
	}

	// Overlapping episode coverage across different files.
	cover := map[epKey][]group{}
	for g := range videoBase {
		for e := g.ep; e <= max(g.ep, g.end); e++ {
			cover[epKey{g.season, e}] = append(cover[epKey{g.season, e}], g)
		}
	}
	var overlaps []string
	for k, gs := range cover {
		if len(gs) > 1 {
			overlaps = append(overlaps, episodeLabel(k.season, k.episode, 0))
		}
	}

	// Subtitles follow the name of their video: one in this batch, else the
	// file already in the library, else the template name.
	orphanSubs := 0
	for _, it := range req.Items {
		ext := naming.Ext(it.Name)
		if !config.IsSubtitle(ext) || config.IsVideo(ext) {
			continue
		}
		g := group{it.Season, it.Episode, it.EpisodeEnd}
		label := naming.SanitizeComponent(it.Version)
		base, ok := videoBase[g][label]
		if !ok {
			for _, b := range videoBase[g] {
				base, ok = b, true
				break
			}
		}
		if !ok {
			for gg, m := range videoBase { // a video covering this episode as part of a range
				if gg.season == it.Season && it.Episode >= gg.ep && it.Episode <= max(gg.ep, gg.end) {
					for _, b := range m {
						base, ok = b, true
						break
					}
				}
			}
		}
		if !ok {
			if existing := library[epKey{it.Season, it.Episode}]; len(existing) > 0 {
				e := existing[0]
				base, ok = strings.TrimSuffix(e, path.Ext(e)), true
			}
		}
		if !ok {
			vars := naming.Vars{}
			for k, v := range baseVars {
				vars[k] = v
			}
			vars["season"], vars["episode"] = it.Season, it.Episode
			if ep, found := seasons[it.Season][it.Episode]; found {
				vars["episode_title"] = ep.Title
			}
			segs, err := nm.TV.Render(vars, 100)
			if err != nil {
				return nil, invalid("name_too_long", err.Error())
			}
			if plan.Folder != "" && reusable {
				segs[0] = plan.Folder
			}
			base = path.Join(append([]string{s.cfg.TVDir}, segs...)...)
			orphanSubs++
		}
		if it.Lang != "" {
			if !subRe.MatchString(it.Lang) {
				return nil, invalid("invalid_subtitle_language", it.Key)
			}
			base += "." + it.Lang
		}
		if it.Forced {
			base += ".forced"
		}
		if it.SDH {
			base += ".sdh"
		}
		plan.Items = append(plan.Items, PlannedItem{Key: it.Key, Role: RoleSubtitle, Name: it.Name, Ext: ext, Size: it.Size,
			Policy: it.Policy, Path: base + "." + ext, Episode: episodeLabel(it.Season, it.Episode, it.EpisodeEnd),
			altPrefix: base + " (", altSuffix: ")." + ext, season: it.Season, episode: it.Episode, episodeEnd: it.EpisodeEnd})
	}

	// Warnings for the review screen.
	inLibrary := 0
	for _, pi := range plan.Items {
		if len(pi.Existing) > 0 {
			inLibrary++
		}
	}
	for season, eps := range seasons {
		var mapped []int
		count := 0
		for g := range videoBase {
			if g.season != season {
				continue
			}
			count++
			for e := g.ep; e <= max(g.ep, g.end); e++ {
				mapped = append(mapped, e)
			}
		}
		if len(mapped) == 0 {
			continue
		}
		slices.Sort(mapped)
		var gaps []string
		for e := mapped[0] + 1; e < mapped[len(mapped)-1]; e++ {
			if !slices.Contains(mapped, e) && len(library[epKey{season, e}]) == 0 {
				gaps = append(gaps, episodeLabel(season, e, 0))
			}
		}
		if len(gaps) > 0 {
			plan.Warnings = append(plan.Warnings, Warning{Code: "gaps", Params: map[string]any{"season": season, "episodes": strings.Join(gaps, ", ")}})
		}
		if len(eps) > 0 && count > len(eps) {
			plan.Warnings = append(plan.Warnings, Warning{Code: "more_files_than_episodes", Params: map[string]any{"season": season, "files": count, "episodes": len(eps)}})
		}
	}
	addList := func(code string, set []string) {
		if len(set) > 0 {
			sort.Strings(set)
			plan.Warnings = append(plan.Warnings, Warning{Code: code, Params: map[string]any{"count": len(set), "episodes": strings.Join(set, ", ")}})
		}
	}
	var unknownList []string
	for k := range unknown {
		unknownList = append(unknownList, k)
	}
	addList("unknown_episodes", unknownList)
	addList("overlapping_episodes", overlaps)
	if ranges > 0 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "multi_episode_files", Params: map[string]any{"count": ranges}})
	}
	if versions > 0 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "alternate_versions", Params: map[string]any{"count": versions}})
	}
	if inLibrary > 0 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "episodes_in_library", Params: map[string]any{"count": inLibrary}})
	}
	if orphanSubs > 0 {
		plan.Warnings = append(plan.Warnings, Warning{Code: "subtitles_without_video", Params: map[string]any{"count": orphanSubs}})
	}
	sort.SliceStable(plan.Items, func(i, j int) bool {
		a, b := plan.Items[i], plan.Items[j]
		if a.season != b.season {
			return a.season < b.season
		}
		if a.episode != b.episode {
			return a.episode < b.episode
		}
		return a.Role == RoleEpisode && b.Role != RoleEpisode
	})
	return plan, s.finishPlan(plan)
}
