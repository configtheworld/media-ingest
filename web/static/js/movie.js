// Movie mapping: suggest a role for each file from its name and size.
// Pure functions (no DOM) so they can be unit tested with node --test.

import { extOf, stemOf } from './files.js';

const EXTRA_HINTS = [
  [/trailer|teaser/i, 'trailer'],
  [/deleted/i, 'deleted'],
  [/behind[ ._-]?the[ ._-]?scenes|making[ ._-]?of/i, 'behindthescenes'],
  [/interview/i, 'interview'],
  [/featurette/i, 'featurette'],
  [/\bscene\b/i, 'scene'],
  [/\bshort\b/i, 'short'],
];

const VERSION_HINTS = [
  [/2160p|\b4k\b|\buhd\b/i, '2160p'],
  [/1080p/i, '1080p'],
  [/720p/i, '720p'],
  [/director'?s[ ._-]?cut/i, "Director's Cut"],
  [/extended/i, 'Extended'],
  [/theatrical/i, 'Theatrical'],
  [/unrated/i, 'Unrated'],
  [/remastered/i, 'Remastered'],
];

const SUB_FLAGS = new Set(['forced', 'sdh', 'cc', 'hi']);

export function extraTypeFor(name) {
  for (const [re, type] of EXTRA_HINTS) if (re.test(name)) return type;
  return 'other';
}

export function versionLabelFor(name) {
  for (const [re, label] of VERSION_HINTS) if (re.test(name)) return label;
  return '';
}

// "Movie.en.forced.srt" -> {lang: 'en', forced: true, sdh: false}
export function subtitleInfo(name) {
  const parts = stemOf(name).split('.').slice(1).map((p) => p.toLowerCase());
  const info = { lang: '', forced: false, sdh: false };
  for (const p of parts.reverse()) {
    if (p === 'forced') info.forced = true;
    else if (SUB_FLAGS.has(p)) info.sdh = true;
    else if (!info.lang && /^[a-z]{2,3}$/.test(p)) info.lang = p;
  }
  return info;
}

// suggestMovieRoles assigns roles to files [{key, name, size}] given the
// video and subtitle extension lists. Returns new objects with role fields.
export function suggestMovieRoles(files, videoExts, subExts) {
  const videos = files.filter((f) => videoExts.has(extOf(f.name))).sort((a, b) => b.size - a.size);
  const largest = videos[0]?.size || 0;
  const out = new Map();
  const movies = [];
  for (const f of videos) {
    const looksExtra = EXTRA_HINTS.some(([re]) => re.test(f.name));
    if (f === videos[0] || (!looksExtra && f.size >= largest * 0.5)) {
      movies.push(f);
      out.set(f.key, { role: 'movie', version: '' });
    } else {
      out.set(f.key, { role: 'extra', extraType: extraTypeFor(f.name), label: stemOf(f.name) });
    }
  }
  if (movies.length > 1) {
    const used = new Set();
    movies.forEach((f, i) => {
      let label = versionLabelFor(f.name) || `Version ${i + 1}`;
      while (used.has(label)) label = `${label} ${i + 1}`;
      used.add(label);
      out.get(f.key).version = label;
    });
  }
  const firstVersion = movies.length ? out.get(movies[0].key).version : '';
  for (const f of files) {
    if (out.has(f.key)) continue;
    if (subExts.has(extOf(f.name))) {
      out.set(f.key, { role: 'subtitle', version: firstVersion, ...subtitleInfo(f.name) });
    } else {
      out.set(f.key, { role: 'ignore' });
    }
  }
  return files.map((f) => ({ ...f, extraType: 'other', label: '', lang: '', forced: false, sdh: false, version: '', ...out.get(f.key) }));
}

// validateMovieMapping returns a list of problem codes for the UI.
export function validateMovieMapping(files) {
  const problems = [];
  const movies = files.filter((f) => f.role === 'movie');
  if (!movies.length) problems.push('no_movie_file');
  const labels = movies.map((f) => f.version.trim());
  if (new Set(labels).size !== labels.length) problems.push('version_labels_required');
  const known = new Set(labels);
  if (files.some((f) => f.role === 'subtitle' && !known.has(f.version.trim()))) problems.push('subtitle_unknown_version');
  if (files.some((f) => f.role === 'subtitle' && f.lang && !/^[a-z]{2,3}(-[A-Za-z]{2,4})?$/.test(f.lang))) problems.push('invalid_subtitle_language');
  return problems;
}
