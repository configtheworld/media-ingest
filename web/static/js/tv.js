// TV flow views: season picker, episode mapping and review.

import { h, clear, fmtBytes, announce } from './dom.js';
import { t, errText } from './i18n.js';
import { get, post } from './api.js';
import { S, persist } from './state.js';
import { go, view, navRow, errorBox, selectedCard, field, warningList, startBatch } from './ui.js';
import { autoAssign, assignSequential, assign, swapWith, nextFree, analyze, epKey, episodeLabel, label, naturalCompare } from './tvmap.js';
import { versionLabelFor } from './movie.js';

const lang = () => encodeURIComponent(S.lang);

export async function pickShow(r) {
  try {
    const sh = await get(`/api/meta/tmdb/tv/${r.id}?lang=${lang()}`);
    Object.assign(S, {
      selected: { id: sh.id, kind: 'tv', title: sh.title, year: sh.year, poster: sh.poster, overview: sh.overview },
      show: sh, title: sh.title, seasonSel: [], episodes: new Map(), library: null,
      folder: { mode: 'auto', name: '' }, policies: {}, plan: null, needsAutoMap: true,
    });
    go('seasons');
  } catch (err) { alert(errText(err)); }
}

function seasonName(n) {
  return n === 0 ? t('tv.specials') : t('tv.season', { n });
}

async function loadLibrary() {
  const q = new URLSearchParams({ lang: S.lang, mode: S.folder.mode, folder: S.folder.name });
  S.library = await get(`/api/library/tmdb/tv/${S.show.id}?${q}`);
  return S.library;
}

function librarySet() {
  return new Set((S.library?.episodes || []).map((e) => epKey(e.season, e.episode)));
}

async function loadEpisodes() {
  const need = S.seasonSel.filter((n) => !S.episodes.has(n));
  const loaded = await Promise.all(need.map((n) => get(`/api/meta/tmdb/tv/${S.show.id}/season/${n}?lang=${lang()}`)));
  loaded.forEach((sd, i) => S.episodes.set(need[i], sd.episodes));
  for (const n of [...S.episodes.keys()]) if (!S.seasonSel.includes(n)) S.episodes.delete(n);
  S.episodes = new Map([...S.episodes].sort((a, b) => a[0] - b[0]));
}

// ---------- step 2: seasons ----------

export function renderSeasons() {
  const libBox = h('div', { 'aria-live': 'polite' }, h('p', { class: 'muted' }, t('tv.checkingLibrary')));
  const list = h('ul', { class: 'seasonlist' });
  const status = h('div', { 'aria-live': 'polite' });
  const next = h('button', { type: 'button', class: 'primary', onclick: proceed }, t('nav.next'));

  function renderList() {
    const inLib = {};
    for (const e of S.library?.episodes || []) inLib[e.season] = (inLib[e.season] || new Set()).add(e.episode);
    next.disabled = !S.seasonSel.length;
    clear(list, S.show.seasons.map((se) => h('li', null, h('label', { class: 'check season' },
      h('input', {
        type: 'checkbox', checked: S.seasonSel.includes(se.number),
        onchange: (e) => {
          S.seasonSel = e.target.checked ? [...S.seasonSel, se.number].sort((a, b) => a - b) : S.seasonSel.filter((n) => n !== se.number);
          S.needsAutoMap = true;
          persist();
          renderList();
        },
      }),
      h('span', null, h('strong', null, seasonName(se.number)),
        h('span', { class: 'muted small' }, ' · ', t('tv.episodeCount', { n: se.episodeCount }),
          se.airDate ? ` · ${se.airDate.slice(0, 4)}` : '',
          inLib[se.number] ? ` · ${t('tv.inLibraryCount', { n: inLib[se.number].size })}` : ''))))));
  }

  function renderLibrary() {
    const L = S.library;
    const folderChoice = L.folderCandidates.length ? h('fieldset', { class: 'stack' },
      h('legend', null, t('review.folderChoice')),
      [{ mode: 'new', name: '', label: t('review.folderNew') }, ...L.folderCandidates.map((c) => ({ mode: 'existing', name: c, label: c }))].map((o) =>
        h('label', { class: 'check' }, h('input', {
          type: 'radio', name: 'folder',
          checked: (S.folder.mode === o.mode && S.folder.name === o.name) || (S.folder.mode === 'auto' && o.mode === 'new'),
          onchange: async () => { S.folder = { mode: o.mode, name: o.name }; await refresh(); },
        }), o.label))) : null;
    clear(libBox,
      h('p', null, t('tv.libraryFolder'), ' ', h('code', null, `${L.libraryDir}/${L.folder}`), ' ',
        h('span', { class: 'badge' }, t(`review.folderSource.${L.folderSource}`)),
        L.folderExists ? h('span', { class: 'badge' }, t('tv.episodesInLibrary', { n: L.episodes.length })) : h('span', { class: 'badge' }, t('tv.notInLibrary'))),
      folderChoice);
  }

  async function refresh() {
    try {
      await loadLibrary();
      S.needsAutoMap = true;
      renderLibrary();
    } catch (err) { clear(libBox, errorBox(err)); }
    renderList();
  }

  async function proceed() {
    next.disabled = true;
    clear(status, h('p', { class: 'muted' }, t('tv.loadingEpisodes')));
    try {
      await loadEpisodes();
      const first = S.seasonSel.find((n) => n > 0) ?? S.seasonSel[0];
      if (!S.seasonSel.includes(S.start.season)) S.start = { season: first, episode: 1 };
      go('files');
    } catch (err) {
      clear(status, errorBox(err));
      next.disabled = false;
    }
  }

  view(t('tv.seasonsTitle'), selectedCard(), libBox, h('p', { class: 'hint' }, t('tv.seasonsHint')), list, status,
    navRow(h('button', { type: 'button', onclick: () => go('search') }, t('nav.back')), next));
  renderList();
  refresh();
}

// ---------- step 4: map files to episodes ----------

let selectedKey = null; // tap-to-assign: the file waiting for an episode

export async function renderTVMap() {
  // After a reload the episode lists and library are not in memory yet.
  if (!S.seasonSel.every((n) => S.episodes.has(n)) || !S.library) {
    view(t('tv.mapTitle'), h('p', { class: 'muted' }, t('tv.loadingEpisodes')));
    try {
      await Promise.all([loadEpisodes(), S.library ? null : loadLibrary()]);
    } catch (err) { return view(t('tv.mapTitle'), errorBox(err)); }
  }
  if (S.needsAutoMap) {
    S.files = withVersionHints(autoAssign(S.files, { episodes: S.episodes, rules: S.rules, videoExts: S.videoExts, start: S.start }));
    S.needsAutoMap = false;
    S.plan = null;
  }
  selectedKey = null;

  const lib = librarySet();
  const libPaths = new Map((S.library?.episodes || []).map((e) => [epKey(e.season, e.episode), e.path]));
  const filesCol = h('ul', { class: 'filecol', 'aria-label': t('tv.filesColumn') });
  const epsCol = h('div', { class: 'epcol' });
  const banner = h('div', { class: 'tapbanner', 'aria-live': 'polite', hidden: true });
  const report = h('div', { 'aria-live': 'polite' });
  const next = h('button', { type: 'button', class: 'primary', onclick: () => go('review') }, t('nav.next'));

  // Episode options for the per-file dropdown.
  const options = [h('option', { value: '' }, t('tv.notAssigned'))];
  for (const [season, list] of S.episodes) {
    options.push(h('optgroup', { label: seasonName(season) }, list.map((e) =>
      h('option', { value: epKey(season, e.number) }, `${label(epKey(season, e.number))}${e.title ? ` · ${e.title}` : ''}`))));
  }

  function rerender() {
    const focusId = document.activeElement?.id;
    renderFiles();
    renderEpisodes();
    check();
    if (focusId) document.getElementById(focusId)?.focus();
  }

  function check() {
    const { problems, warnings, needVersion } = analyze(S.files, S.episodes, lib);
    next.disabled = problems.length > 0;
    clear(report,
      problems.map((p) => h('p', { class: 'error' }, t(`error.${p.code}`))),
      warningList(warnings));
    for (const li of filesCol.children) li.classList.toggle('needs-version', needVersion.has(li.dataset.key));
    S.plan = null;
    return needVersion;
  }

  function doAssign(key, season, episode) {
    const f = S.files.find((x) => x.key === key);
    assign(S.files, key, season, episode, { swap: !f?.alt });
    selectedKey = null;
    banner.hidden = true;
    rerender();
    if (season != null) announce(t('tv.assigned', { name: f.name, episode: label(epKey(season, episode)) }));
  }

  function select(key) {
    selectedKey = selectedKey === key ? null : key;
    const f = S.files.find((x) => x.key === selectedKey);
    banner.hidden = !f;
    if (f) {
      clear(banner, h('span', null, t('tv.tapEpisode', { name: f.name })),
        h('button', { type: 'button', class: 'ghost', onclick: () => select(selectedKey) }, t('upload.cancel')));
    }
    for (const li of filesCol.children) li.classList.toggle('picked', li.dataset.key === selectedKey);
    epsCol.classList.toggle('picking', !!f);
  }

  function ordered() {
    return [...S.files].sort((a, b) => {
      if ((a.episode == null) !== (b.episode == null)) return a.episode == null ? 1 : -1;
      if (a.episode != null) {
        if (a.season !== b.season) return a.season - b.season;
        if (a.episode !== b.episode) return a.episode - b.episode;
        if (a.kind !== b.kind) return a.kind === 'video' ? -1 : 1;
      }
      return naturalCompare(a.path || a.name, b.path || b.name);
    });
  }

  function renderFiles() {
    const list = ordered();
    const videos = list.filter((f) => f.kind === 'video');
    const needVersion = analyze(S.files, S.episodes, lib).needVersion;
    clear(filesCol, list.map((f) => {
      const vi = videos.indexOf(f);
      const controls = [];
      const sel = h('select', {
        id: `ep-${f.key}`,
        onchange: (e) => {
          const [s, ep] = e.target.value ? e.target.value.split(':').map(Number) : [null, null];
          doAssign(f.key, s, ep);
        },
      }, options.map((o) => o.cloneNode(true)));
      sel.value = f.episode != null ? epKey(f.season, f.episode) : '';
      controls.push(field(t('tv.episode'), sel, 'grow'));

      if (f.kind === 'video' && f.episode != null) {
        const eps = (S.episodes.get(f.season) || []).map((e) => e.number).filter((n) => n > f.episode).slice(0, 3);
        if (eps.length) {
          const end = h('select', {
            id: `end-${f.key}`,
            onchange: (e) => { f.episodeEnd = e.target.value ? Number(e.target.value) : null; f.how = 'manual'; rerender(); },
          }, h('option', { value: '' }, '—'), eps.map((n) => h('option', { value: n }, `E${String(n).padStart(2, '0')}`)));
          end.value = f.episodeEnd ? String(f.episodeEnd) : '';
          controls.push(field(t('tv.through'), end, 'narrow'));
        }
      }
      if (f.kind === 'video' && (f.alt || needVersion.has(f.key) || (f.version || '').trim())) {
        controls.push(field(t('tv.versionLabel'), h('input', {
          id: `ver-${f.key}`, value: f.version || '', placeholder: '1080p', maxlength: 60,
          oninput: (e) => { f.version = e.target.value; check(); },
        })));
      }
      if (f.kind === 'subtitle') {
        controls.push(field(t('map.subLang'), h('input', {
          id: `lang-${f.key}`, value: f.lang || '', placeholder: 'en', maxlength: 10, size: 5, autocapitalize: 'none',
          oninput: (e) => { f.lang = e.target.value.trim(); check(); },
        }), 'narrow'));
        controls.push(h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: !!f.forced, onchange: (e) => { f.forced = e.target.checked; } }), t('map.forced')));
        controls.push(h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: !!f.sdh, onchange: (e) => { f.sdh = e.target.checked; } }), t('map.sdh')));
      }

      const actions = [
        h('button', { type: 'button', class: 'pick', 'aria-pressed': String(selectedKey === f.key), onclick: () => select(f.key) }, t('tv.pick')),
      ];
      if (f.kind === 'video') {
        actions.push(h('button', {
          type: 'button', onclick: () => {
            const free = nextFree(S.files.filter((x) => x !== f), S.episodes, lib);
            if (free) doAssign(f.key, free.season, free.episode); else alert(t('tv.noFreeEpisode'));
          },
        }, t('tv.nextFree')));
        actions.push(h('button', { type: 'button', class: 'icon', 'aria-label': t('tv.moveUp', { name: f.name }), disabled: vi <= 0 || f.episode == null || videos[vi - 1].episode == null, onclick: () => { swapWith(S.files, f.key, videos[vi - 1].key); rerender(); } }, '↑'));
        actions.push(h('button', { type: 'button', class: 'icon', 'aria-label': t('tv.moveDown', { name: f.name }), disabled: vi < 0 || vi >= videos.length - 1 || f.episode == null || videos[vi + 1].episode == null, onclick: () => { swapWith(S.files, f.key, videos[vi + 1].key); rerender(); } }, '↓'));
        actions.push(h('label', { class: 'check small' }, h('input', { type: 'checkbox', checked: !!f.alt, onchange: (e) => { f.alt = e.target.checked; rerender(); } }), t('tv.alternate')));
      }
      if (f.episode != null) actions.push(h('button', { type: 'button', class: 'ghost', onclick: () => doAssign(f.key, null, null) }, t('tv.skip')));

      return h('li', {
        class: `frow kind-${f.kind}${f.episode == null ? ' unassigned' : ''}${selectedKey === f.key ? ' picked' : ''}`,
        'data-key': f.key, draggable: 'true',
        ondragstart: (e) => { e.dataTransfer.setData('text/plain', f.key); e.dataTransfer.effectAllowed = 'move'; },
      },
      h('div', { class: 'maphead' },
        h('span', { class: 'fname' }, f.path || f.name),
        h('span', { class: 'badge' }, f.episode != null ? episodeLabel(f) : t('tv.notAssigned')),
        f.kind === 'subtitle' ? h('span', { class: 'badge' }, t('role.subtitle')) : null,
        h('span', { class: 'muted small' }, fmtBytes(f.size))),
      h('div', { class: 'mapfields' }, controls),
      h('div', { class: 'row small' }, actions));
    }));
  }

  function renderEpisodes() {
    const byEp = new Map();
    for (const f of S.files) {
      if (f.episode == null) continue;
      for (let e = f.episode; e <= Math.max(f.episode, f.episodeEnd || 0); e++) {
        const k = epKey(f.season, e);
        byEp.set(k, [...(byEp.get(k) || []), f]);
      }
    }
    clear(epsCol, [...S.episodes].map(([season, list]) => h('section', null,
      h('h3', null, seasonName(season), h('span', { class: 'muted small' }, ` · ${t('tv.episodeCount', { n: list.length })}`)),
      h('ul', { class: 'eplist' }, list.map((e) => {
        const k = epKey(season, e.number);
        const files = byEp.get(k) || [];
        const videos = files.filter((f) => f.kind === 'video');
        const inLib = lib.has(k);
        return h('li', null, h('button', {
          type: 'button',
          class: `erow${videos.length ? ' filled' : ''}${inLib ? ' in-lib' : ''}${videos.length > 1 ? ' dup' : ''}`,
          'aria-label': t('tv.episodeButton', { episode: label(k), title: e.title || '' }),
          onclick: () => { if (selectedKey) doAssign(selectedKey, season, e.number); },
          ondragover: (ev) => { ev.preventDefault(); ev.currentTarget.classList.add('over'); },
          ondragleave: (ev) => ev.currentTarget.classList.remove('over'),
          ondrop: (ev) => {
            ev.preventDefault();
            const key = ev.dataTransfer.getData('text/plain');
            if (key) doAssign(key, season, e.number);
          },
        },
        h('span', { class: 'epnum' }, `E${String(e.number).padStart(2, '0')}`),
        h('span', { class: 'eptitle' }, e.title || '', e.airDate ? h('span', { class: 'muted small' }, ` · ${e.airDate}`) : null),
        h('span', { class: 'epfiles' },
          inLib ? h('span', { class: 'badge lib', title: libPaths.get(k) }, t('tv.inLibrary')) : null,
          files.map((f) => h('span', { class: `chip ${f.kind}` }, f.name)))));
      })))));
  }

  // Toolbar: re-run auto mapping, or assign everything in natural order.
  const seasonSel = h('select', { id: 'seq-season' }, [...S.episodes.keys()].map((n) => h('option', { value: n, selected: n === S.start.season }, seasonName(n))));
  const startEp = h('input', { id: 'seq-start', type: 'number', min: 1, max: 9999, value: S.start.episode, inputmode: 'numeric' });
  const toolbar = h('div', { class: 'toolbar' },
    h('button', {
      type: 'button', onclick: () => {
        S.files = withVersionHints(autoAssign(S.files, { episodes: S.episodes, rules: S.rules, videoExts: S.videoExts, start: S.start }));
        rerender();
      },
    }, t('tv.autoAssign')),
    h('div', { class: 'row small' },
      field(t('tv.sequentialSeason'), seasonSel), field(t('tv.startAt'), startEp, 'narrow'),
      h('button', {
        type: 'button', onclick: () => {
          S.start = { season: Number(seasonSel.value), episode: Math.max(1, Number(startEp.value) || 1) };
          assignSequential(S.files, S.start, S.episodes);
          rerender();
        },
      }, t('tv.assignInOrder'))));

  view(t('tv.mapTitle'), selectedCard(), h('p', { class: 'hint' }, t('tv.mapHint')), toolbar,
    h('div', { class: 'mapgrid' },
      h('section', null, h('h3', null, t('tv.filesColumn')), filesCol),
      h('section', null, h('h3', null, t('tv.episodesColumn')), epsCol)),
    report, banner,
    navRow(h('button', { type: 'button', onclick: () => go('files') }, t('nav.back')), next));
  rerender();
}

// withVersionHints pre-fills version labels for files sharing an episode.
function withVersionHints(files) {
  const groups = new Map();
  for (const f of files) {
    if (f.kind !== 'video' || f.episode == null) continue;
    const g = `${f.season}:${f.episode}:${f.episodeEnd || 0}`;
    groups.set(g, [...(groups.get(g) || []), f]);
  }
  for (const list of groups.values()) {
    if (list.length < 2) continue;
    for (const f of list) if (!f.version) f.version = versionLabelFor(f.name);
  }
  return files;
}

// ---------- step 5: review ----------

function tvRequest(withPolicies) {
  return {
    provider: 'tmdb',
    id: S.show.id,
    language: S.lang,
    title: S.title.trim(),
    folder: S.folder,
    items: S.files.filter((f) => f.episode != null).map((f) => ({
      key: f.key, name: f.name, size: f.size, season: f.season, episode: f.episode, episodeEnd: f.episodeEnd || 0,
      version: (f.version || '').trim(), lang: f.kind === 'subtitle' ? (f.lang || '') : '',
      forced: f.kind === 'subtitle' && !!f.forced, sdh: f.kind === 'subtitle' && !!f.sdh,
      policy: withPolicies ? (S.policies[f.key] || '') : '',
    })),
  };
}

export function renderTVReview() {
  const body = h('div', { 'aria-live': 'polite' }, h('p', { class: 'muted' }, t('review.planning')));
  const confirm = h('button', { type: 'button', class: 'primary', disabled: true, onclick: start }, t('review.confirm'));
  const titleInput = h('input', { id: 'dtitle', value: S.title, maxlength: 200, onchange: (e) => { S.title = e.target.value; persist(); plan(); } });
  const skipped = S.files.filter((f) => f.episode == null).length;

  async function plan() {
    confirm.disabled = true;
    try {
      S.plan = await post('/api/plan/tv', tvRequest(false));
      renderPlan();
    } catch (err) { clear(body, errorBox(err)); }
  }

  function setAll(policy) {
    for (const i of S.plan.items) if (i.conflict) S.policies[i.key] = policy;
    renderPlan();
  }

  function renderPlan() {
    const p = S.plan;
    const conflicts = p.items.filter((i) => i.conflict);
    confirm.disabled = conflicts.some((i) => !S.policies[i.key]) || !p.summary.enoughSpace;
    clear(body,
      h('dl', { class: 'summary' },
        h('dt', null, t('review.folder')), h('dd', null, h('code', null, `${p.libraryDir}/${p.folder}`), ' ',
          h('span', { class: 'badge' }, t(`review.folderSource.${p.folderSource}`))),
        h('dt', null, t('review.files')), h('dd', null, String(p.summary.files), skipped ? h('span', { class: 'muted small' }, ` · ${t('tv.skippedFiles', { n: skipped })}`) : null),
        h('dt', null, t('review.size')), h('dd', null, fmtBytes(p.summary.bytes)),
        h('dt', null, t('review.free')), h('dd', { class: p.summary.enoughSpace ? '' : 'bad' }, fmtBytes(p.summary.freeBytes)),
        h('dt', null, t('review.conflicts')), h('dd', { class: p.summary.conflicts ? 'bad' : '' }, String(p.summary.conflicts))),
      warningList(p.warnings),
      conflicts.length > 1 ? h('div', { class: 'row small' }, h('span', null, t('review.applyAll')),
        ['skip', 'replace', 'keep_both'].map((pol) => h('button', { type: 'button', onclick: () => setAll(pol) }, t(`policy.${pol}`)))) : null,
      h('ul', { class: 'planlist' }, p.items.map((i) => h('li', { class: i.conflict ? 'conflict' : '' },
        h('div', { class: 'muted small' }, h('strong', null, i.episode), ' · ', i.name, ' → '),
        h('code', { class: 'path' }, i.path),
        i.existing?.length ? h('div', { class: 'small' }, t('tv.existingFiles'), h('ul', null, i.existing.map((x) => h('li', null, h('code', { class: 'path' }, x))))) : null,
        i.conflict ? h('div', { class: 'field' },
          h('label', { for: `pol-${i.key}` }, i.exists ? t('review.exists') : t('tv.episodeExists')),
          h('select', { id: `pol-${i.key}`, onchange: (e) => { S.policies[i.key] = e.target.value; renderPlan(); } },
            h('option', { value: '', selected: !S.policies[i.key] }, t('review.choose')),
            ['skip', 'replace', 'keep_both'].map((pol) => h('option', { value: pol, selected: S.policies[i.key] === pol }, t(`policy.${pol}`))))) : null))));
  }

  async function start() {
    confirm.disabled = true;
    try {
      startBatch(await post('/api/batches/tv', tvRequest(true)));
    } catch (err) {
      clear(body, errorBox(err));
      confirm.disabled = false;
    }
  }

  view(t('review.title'), selectedCard(),
    h('div', { class: 'field' }, h('label', { for: 'dtitle' }, t('review.displayTitle')), titleInput, h('p', { class: 'hint' }, t('review.displayTitleHint'))),
    body,
    navRow(h('button', { type: 'button', onclick: () => go('map') }, t('nav.back')), confirm));
  plan();
}
