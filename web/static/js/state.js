// Wizard state shared by all views, and its persistence in localStorage
// (File objects cannot be persisted; they are re-selected after a reload).

export const LANGUAGES = ['en-US', 'en-GB', 'de-DE', 'tr-TR', 'es-ES', 'es-MX', 'fr-FR', 'it-IT', 'pt-BR', 'pt-PT', 'nl-NL', 'pl-PL',
  'sv-SE', 'da-DK', 'nb-NO', 'fi-FI', 'cs-CZ', 'hu-HU', 'el-GR', 'ru-RU', 'uk-UA', 'ar-SA', 'he-IL', 'hi-IN', 'ja-JP', 'ko-KR', 'zh-CN', 'zh-TW'];

const STORE_KEY = 'mi.state';

export const S = {
  session: null,
  videoExts: new Set(),
  subExts: new Set(),
  rules: null, // compiled parser rules (TV)
  step: 'search',
  kind: 'movie',
  lang: 'en-US',
  query: '',
  results: [],
  selected: null, // {id, kind, title, year, poster, overview}
  files: [], // [{key, file, name, path, size, lastModified, ...role or episode fields}]
  needsAutoMap: true, // re-run automatic mapping when entering the map step
  ignored: [],
  title: '',
  folder: { mode: 'auto', name: '' },
  policies: {},
  plan: null,
  batch: null,
  fps: {}, // itemId -> file fingerprint, to resume after reload
  uploads: new Map(), // itemId -> Upload
  // TV
  show: null, // {id, title, year, seasons: [{number, name, episodeCount, airDate}]}
  seasonSel: [], // selected season numbers
  episodes: new Map(), // season -> [{number, title, airDate}]
  library: null, // {folder, folderSource, folderCandidates, episodes: [{season, episode, path}]}
  start: { season: 1, episode: 1 },
  // Quick upload
  quick: null, // {folder, newFolder, keepStructure}
};

export function stepsFor(kind) {
  if (kind === 'tv') return ['search', 'seasons', 'files', 'map', 'review', 'upload'];
  if (kind === 'quick') return ['folder', 'files', 'review', 'upload'];
  return ['search', 'files', 'map', 'review', 'upload'];
}

export function persist() {
  const { kind, lang, selected, title, fps, show, seasonSel, quick } = S;
  const data = { kind, lang, selected, title, fps, show, seasonSel, quick, batchId: S.batch?.id || null };
  try { localStorage.setItem(STORE_KEY, JSON.stringify(data)); } catch { /* private mode */ }
}

export function restore() {
  try { return JSON.parse(localStorage.getItem(STORE_KEY)) || {}; } catch { return {}; }
}

// resetWizard forgets the current title and files (after a finished batch).
export function resetWizard() {
  Object.assign(S, {
    batch: null, files: [], fps: {}, selected: null, plan: null, policies: {}, results: [], ignored: [],
    show: null, seasonSel: [], episodes: new Map(), library: null, folder: { mode: 'auto', name: '' }, needsAutoMap: true,
    quick: null,
  });
  if (S.kind === 'quick') S.kind = 'movie';
  S.uploads.clear();
}
