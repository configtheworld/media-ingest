// Episode number parser driven by the server's rule set (see
// internal/parser/default.json). Pure functions: no DOM.

import { stemOf } from './files.js';

const MAX_RANGE = 20;

// compileRules turns the JSON rule set into RegExps (case-insensitive).
export function compileRules(spec) {
  return {
    episode: (spec.episode || []).map((r) => ({ name: r.name, re: new RegExp(r.pattern, 'i') })),
    season: (spec.season || []).map((r) => ({ name: r.name, re: new RegExp(r.pattern, 'i') })),
    junk: (spec.junk || []).map((p) => new RegExp(p, 'gi')),
  };
}

export function stripJunk(text, rules) {
  let out = text;
  for (const re of rules.junk) {
    re.lastIndex = 0;
    out = out.replace(re, ' ');
  }
  return out;
}

// seasonOf finds a season number in a folder or file name (null if none).
export function seasonOf(text, rules) {
  if (!text) return null;
  for (const r of rules.season) {
    const m = r.re.exec(text);
    if (!m?.groups) continue;
    if (m.groups.specials) return 0;
    if (m.groups.season != null) return Number(m.groups.season);
  }
  return null;
}

// parseName returns {season, episode, episodeEnd, rule, seasonFrom} or null.
// season is null when neither the name nor its folders reveal it.
// dirs: parent folder names, nearest first.
export function parseName(name, dirs, rules) {
  const clean = stripJunk(stemOf(name), rules);
  for (const r of rules.episode) {
    const m = r.re.exec(clean);
    const g = m?.groups;
    if (!g?.episode) continue;
    const episode = Number(g.episode);
    if (!episode) continue;
    let episodeEnd = g.episodeEnd != null ? Number(g.episodeEnd) : null;
    if (episodeEnd != null && (episodeEnd <= episode || episodeEnd - episode > MAX_RANGE)) episodeEnd = null;
    let season = g.season != null ? Number(g.season) : null;
    let seasonFrom = season != null ? 'name' : null;
    if (season == null) {
      season = seasonOf(clean, rules);
      if (season != null) seasonFrom = 'name';
    }
    for (const d of dirs || []) {
      if (season != null) break;
      season = seasonOf(d, rules);
      if (season != null) seasonFrom = 'folder';
    }
    return { season, episode, episodeEnd, rule: r.name, seasonFrom };
  }
  return null;
}

// dirsOf returns the parent folder names of a relative path, nearest first.
export function dirsOf(path) {
  const parts = String(path || '').split(/[\\/]/);
  parts.pop();
  return parts.reverse();
}
