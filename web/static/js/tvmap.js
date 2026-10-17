// TV mapping: assign files to episodes. Pure functions (no DOM), unit tested.
//
// files: [{key, name, path, size, kind: 'video'|'subtitle', season, episode,
//          episodeEnd, version, lang, forced, sdh, how}]
// episodes: Map<season number, [{number, title, airDate}]> for the selected seasons
// library: Set of "season:episode" already present in the library

import { parseName, dirsOf } from './parser.js';
import { extOf, stemOf } from './files.js';
import { subtitleInfo } from './movie.js';

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
export const naturalCompare = (a, b) => collator.compare(a, b);

export const epKey = (season, episode) => `${season}:${episode}`;

export function covered(f) {
  if (f.episode == null) return [];
  const out = [];
  for (let e = f.episode; e <= Math.max(f.episode, f.episodeEnd || 0); e++) out.push(e);
  return out;
}

function clearAssignment(f) {
  return { ...f, season: null, episode: null, episodeEnd: null, how: null };
}

// autoAssign maps files from their names. Only when no video file parses
// at all does it fall back to natural sort order starting at `start`.
export function autoAssign(files, { episodes, rules, videoExts, start }) {
  const seasons = [...episodes.keys()];
  const defaultSeason = seasons.length === 1 ? seasons[0] : null;
  const out = files.map((f) => {
    const kind = videoExts.has(extOf(f.name)) ? 'video' : 'subtitle';
    const base = { version: '', lang: '', forced: false, sdh: false, ...clearAssignment(f), kind };
    return kind === 'subtitle' ? { ...base, ...subtitleInfo(f.name) } : base;
  });
  let parsedVideos = 0;
  for (const f of out) {
    const p = parseName(f.name, dirsOf(f.path), rules);
    if (!p) continue;
    const season = p.season ?? defaultSeason;
    if (season == null || !episodes.has(season)) continue;
    Object.assign(f, { season, episode: p.episode, episodeEnd: p.episodeEnd, how: 'name' });
    if (f.kind === 'video') parsedVideos++;
  }
  if (parsedVideos === 0 && start && episodes.has(start.season)) {
    assignSequential(out, start, episodes);
  }
  return matchSubtitles(out);
}

// assignSequential assigns all video files, in natural order of their
// paths, to consecutive episodes of start.season from start.episode on.
// Files beyond the last episode stay unassigned.
export function assignSequential(files, start, episodes) {
  const eps = (episodes.get(start.season) || []).map((e) => e.number).filter((n) => n >= start.episode);
  const videos = files.filter((f) => f.kind === 'video').sort((a, b) => naturalCompare(a.path || a.name, b.path || b.name));
  videos.forEach((f, i) => {
    Object.assign(f, clearAssignment(f));
    if (i < eps.length) Object.assign(f, { season: start.season, episode: eps[i], how: 'sequence' });
  });
  return files;
}

// matchSubtitles gives unassigned subtitles the episode of the video whose
// file name they extend ("Show 01.mkv" -> "Show 01.en.srt").
export function matchSubtitles(files) {
  const videos = files.filter((f) => f.kind === 'video' && f.episode != null);
  for (const s of files) {
    if (s.kind !== 'subtitle' || s.episode != null) continue;
    const stem = stemOf(s.name).toLowerCase();
    let best = null;
    for (const v of videos) {
      const vs = stemOf(v.name).toLowerCase();
      if (stem.startsWith(vs) && (!best || vs.length > stemOf(best.name).length)) best = v;
    }
    if (best) Object.assign(s, { season: best.season, episode: best.episode, episodeEnd: best.episodeEnd, version: best.version, how: 'subtitle' });
  }
  return files;
}

// assign puts file `key` on (season, episode). If another video already sits
// there, the two swap places (the other one takes this file's old slot).
export function assign(files, key, season, episode, { swap = true } = {}) {
  const f = files.find((x) => x.key === key);
  if (!f) return files;
  const prev = { season: f.season, episode: f.episode, episodeEnd: f.episodeEnd };
  if (swap && f.kind === 'video' && season != null) {
    const other = files.find((x) => x !== f && x.kind === 'video' && x.season === season && x.episode === episode);
    if (other) Object.assign(other, { ...prev, how: 'manual' });
  }
  if (season == null) Object.assign(f, clearAssignment(f));
  else Object.assign(f, { season, episode, episodeEnd: null, how: 'manual' });
  return files;
}

// swapWith exchanges the assignments of two files (used by move up/down).
export function swapWith(files, keyA, keyB) {
  const a = files.find((x) => x.key === keyA);
  const b = files.find((x) => x.key === keyB);
  if (!a || !b) return files;
  const pick = (x) => ({ season: x.season, episode: x.episode, episodeEnd: x.episodeEnd });
  const pa = pick(a);
  Object.assign(a, pick(b), { how: 'manual' });
  Object.assign(b, pa, { how: 'manual' });
  return files;
}

// nextFree returns the first episode (seasons in order) that no video in
// this batch covers and that is not already in the library.
export function nextFree(files, episodes, library) {
  const taken = new Set();
  for (const f of files) if (f.kind === 'video') for (const e of covered(f)) taken.add(epKey(f.season, e));
  for (const season of [...episodes.keys()].sort((a, b) => a - b)) {
    for (const ep of episodes.get(season)) {
      const k = epKey(season, ep.number);
      if (!taken.has(k) && !library.has(k)) return { season, episode: ep.number };
    }
  }
  return null;
}

// analyze returns blocking problems and warnings for the mapping.
export function analyze(files, episodes, library) {
  const problems = [];
  const warnings = [];
  const videos = files.filter((f) => f.kind === 'video' && f.episode != null);
  const assigned = files.filter((f) => f.episode != null);
  if (!assigned.length) problems.push({ code: 'no_episode_file' });

  const groups = new Map(); // "s:e:end" -> files
  for (const f of videos) {
    const g = `${f.season}:${f.episode}:${f.episodeEnd || 0}`;
    groups.set(g, [...(groups.get(g) || []), f]);
  }
  const needVersion = new Set();
  let versions = 0;
  for (const list of groups.values()) {
    if (list.length < 2) continue;
    versions += list.length;
    const labels = list.map((f) => (f.version || '').trim().toLowerCase());
    if (new Set(labels).size !== labels.length) list.forEach((f) => needVersion.add(f.key));
  }
  if (needVersion.size) problems.push({ code: 'alternate_version_required', keys: [...needVersion] });
  if (versions) warnings.push({ code: 'alternate_versions', params: { count: versions } });

  const coverage = new Map(); // "s:e" -> group ids
  for (const [g, list] of groups) {
    for (const e of covered(list[0])) {
      const k = epKey(list[0].season, e);
      coverage.set(k, [...(coverage.get(k) || []), g]);
    }
  }
  const overlaps = [...coverage].filter(([, gs]) => gs.length > 1).map(([k]) => label(k));
  if (overlaps.length) warnings.push({ code: 'overlapping_episodes', params: { count: overlaps.length, episodes: overlaps.join(', ') } });

  const inLib = [...new Set(videos.flatMap((f) => covered(f).map((e) => epKey(f.season, e))))].filter((k) => library.has(k));
  if (inLib.length) warnings.push({ code: 'episodes_in_library', params: { count: inLib.length, episodes: inLib.map(label).join(', ') } });

  const ranges = videos.filter((f) => f.episodeEnd);
  if (ranges.length) warnings.push({ code: 'multi_episode_files', params: { count: ranges.length } });

  const unknown = [];
  for (const f of videos) {
    const known = new Set((episodes.get(f.season) || []).map((e) => e.number));
    for (const e of covered(f)) if (!known.has(e)) unknown.push(label(epKey(f.season, e)));
  }
  if (unknown.length) warnings.push({ code: 'unknown_episodes', params: { count: unknown.length, episodes: unknown.join(', ') } });

  for (const season of episodes.keys()) {
    const nums = [...coverage.keys()].filter((k) => Number(k.split(':')[0]) === season).map((k) => Number(k.split(':')[1])).sort((a, b) => a - b);
    if (nums.length < 2) continue;
    const gaps = [];
    for (let e = nums[0] + 1; e < nums[nums.length - 1]; e++) {
      if (!nums.includes(e) && !library.has(epKey(season, e))) gaps.push(label(epKey(season, e)));
    }
    if (gaps.length) warnings.push({ code: 'gaps', params: { season, episodes: gaps.join(', ') } });
  }

  const totalEpisodes = [...episodes.values()].reduce((n, list) => n + list.length, 0);
  const allVideos = files.filter((f) => f.kind === 'video').length;
  if (allVideos > totalEpisodes) warnings.push({ code: 'more_files_than_episodes_total', params: { files: allVideos, episodes: totalEpisodes } });

  const unassigned = files.filter((f) => f.episode == null).length;
  if (unassigned) warnings.push({ code: 'unassigned_files', params: { count: unassigned } });
  return { problems, warnings, needVersion };
}

export function label(key) {
  const [s, e] = key.split(':').map(Number);
  return `S${String(s).padStart(2, '0')}E${String(e).padStart(2, '0')}`;
}

export function episodeLabel(f) {
  if (f.episode == null) return '';
  const base = label(epKey(f.season, f.episode));
  return f.episodeEnd ? `${base}-E${String(f.episodeEnd).padStart(2, '0')}` : base;
}
