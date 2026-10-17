import { h, clear, fmtBytes, uid, announce } from './dom.js';
import { loadI18n, t, errText } from './i18n.js';
import { get, post, put, setCsrf, ApiError } from './api.js';
import { filterFiles, filesFromDataTransfer, fromInput, fingerprint, extOf } from './files.js';
import { suggestMovieRoles, validateMovieMapping } from './movie.js';
import { Upload, Queue } from './upload.js';
import { compileRules } from './parser.js';
import { S, LANGUAGES, persist, restore, resetWizard } from './state.js';
import { main, go, registerViews, view, navRow, errorBox, renderMessage, selectedCard, field, startBatch } from './ui.js';
import { pickShow, renderSeasons, renderTVMap, renderTVReview } from './tv.js';
import { startQuick, renderQuickFolder, renderQuickReview } from './quick.js';
import { renderHistory } from './history.js';

let queue; // upload queue, created at boot

// ---------- boot ----------

async function boot() {
  await loadI18n();
  document.title = t('app.name');
  queue = new Queue({ parallel: 3, onChange: scheduleUploadRender });
  try {
    S.session = await get('/api/session');
  } catch (err) {
    if (err instanceof ApiError && err.status === 401 && err.data?.authMode === 'basic') {
      setCsrf(err.data.csrfToken);
      renderChrome();
      return renderLogin();
    }
    renderChrome();
    return renderMessage(t('auth.failedTitle'), err.status === 401 ? t('auth.proxyMissing') : errText(err));
  }
  setCsrf(S.session.csrfToken);
  S.videoExts = new Set(S.session.videoExtensions);
  S.subExts = new Set(S.session.subtitleExtensions);
  renderChrome();
  const kind = () => S.selected?.kind || S.kind;
  registerViews({
    search: renderSearch,
    seasons: renderSeasons,
    folder: renderQuickFolder,
    files: renderFiles,
    map: () => (kind() === 'tv' ? renderTVMap() : renderMap()),
    review: () => ({ tv: renderTVReview, quick: renderQuickReview }[kind()] || renderReview)(),
    upload: renderUpload,
  });
  if (S.session.needsSetup) return renderSetup();
  try { S.rules = compileRules(await get('/api/parser/rules')); } catch { S.rules = compileRules({ episode: [], season: [], junk: [] }); }

  const saved = restore();
  S.kind = saved.kind === 'tv' ? 'tv' : 'movie';
  S.quick = null;
  S.lang = LANGUAGES.includes(saved.lang) ? saved.lang : 'en-US';
  S.fps = saved.fps || {};
  if (saved.batchId) {
    try {
      const b = await get(`/api/batches/${saved.batchId}`);
      S.batch = b;
      S.selected = saved.selected;
      return go('upload');
    } catch { /* batch gone: start fresh */ }
  }
  if (saved.selected) { S.selected = saved.selected; S.title = saved.title || saved.selected.title; }
  if (saved.kind === 'quick' && saved.quick) {
    Object.assign(S, { kind: 'quick', quick: saved.quick, selected: saved.selected || { kind: 'quick', title: '' } });
    return go('folder');
  }
  if (S.selected?.kind === 'tv') {
    S.show = saved.show;
    S.seasonSel = saved.seasonSel || [];
    return go(S.show ? 'seasons' : 'search');
  }
  go(S.selected ? 'files' : 'search');
}

function renderChrome() {
  const header = document.getElementById('header');
  const actions = [];
  if (S.session && !S.session.needsSetup) {
    actions.push(h('button', { class: 'ghost', type: 'button', onclick: () => { if (!queue?.busy()) { resetWizard(); go('search'); } else go('upload'); } }, t('nav.new')));
    actions.push(h('button', { class: 'ghost', type: 'button', onclick: () => { if (!queue?.busy()) { resetWizard(); startQuick(); } else go('upload'); } }, t('nav.quick')));
    actions.push(h('button', { class: 'ghost', type: 'button', onclick: renderHistory }, t('nav.history')));
  }
  if (S.session?.admin) actions.push(h('button', { class: 'ghost', type: 'button', onclick: renderSettings }, t('nav.settings')));
  if (S.session?.authMode === 'basic') {
    actions.push(h('button', { class: 'ghost', type: 'button', onclick: logout }, t('nav.logout')));
  }
  clear(header,
    h('a', { href: '/', class: 'brand' }, t('app.name')),
    h('div', { class: 'header-actions' },
      S.session ? h('span', { class: 'user', 'aria-label': t('nav.signedInAs', { user: S.session.user }) }, S.session.user) : null,
      actions));
  const footer = document.getElementById('footer');
  clear(footer, (S.session?.providers || []).map((p) =>
    h('p', { class: 'attribution' }, p.attribution.text, ' ', h('a', { href: p.attribution.url, rel: 'noopener noreferrer', target: '_blank' }, p.name.toUpperCase()))),
  S.session ? h('p', { class: 'version' }, `media-ingest ${S.session.version}`) : null);
}

async function logout() {
  await post('/api/auth/logout', {}).catch(() => {});
  location.reload();
}

// ---------- setup and login ----------

function renderSetup() {
  if (!S.session.admin) return renderMessage(t('setup.title'), t('setup.adminOnly'));
  const status = h('div', { 'aria-live': 'polite' });
  const input = h('input', { type: 'password', id: 'apikey', required: true, autocomplete: 'off', spellcheck: 'false' });
  const form = h('form', {
    class: 'stack',
    onsubmit: async (e) => {
      e.preventDefault();
      clear(status, t('setup.checking'));
      try {
        await post('/api/setup/tmdb', { apiKey: input.value.trim() });
        location.reload();
      } catch (err) { clear(status, errorBox(err)); }
    },
  },
  h('label', { for: 'apikey' }, t('setup.keyLabel')), input,
  h('p', { class: 'hint' }, t('setup.keyHint'), ' ',
    h('a', { href: 'https://www.themoviedb.org/settings/api', target: '_blank', rel: 'noopener noreferrer' }, 'themoviedb.org/settings/api')),
  h('button', { type: 'submit', class: 'primary' }, t('setup.save')), status);
  clear(main(), h('section', { class: 'view' }, h('h2', null, t('setup.title')), h('p', null, t('setup.intro')), form));
}

function renderLogin() {
  const status = h('div', { 'aria-live': 'polite' });
  const user = h('input', { id: 'user', autocomplete: 'username', required: true, autocapitalize: 'none' });
  const pass = h('input', { id: 'pass', type: 'password', autocomplete: 'current-password', required: true });
  const form = h('form', {
    class: 'stack',
    onsubmit: async (e) => {
      e.preventDefault();
      try {
        await post('/api/auth/login', { username: user.value, password: pass.value });
        location.reload();
      } catch (err) { clear(status, errorBox(err)); pass.value = ''; pass.focus(); }
    },
  }, h('label', { for: 'user' }, t('login.user')), user, h('label', { for: 'pass' }, t('login.password')), pass,
  h('button', { type: 'submit', class: 'primary' }, t('login.submit')), status);
  clear(main(), h('section', { class: 'view narrow' }, h('h2', null, t('login.title')), form));
  user.focus();
}

// ---------- step 1: search ----------

function langName(code) {
  try { return new Intl.DisplayNames([document.documentElement.lang || 'en'], { type: 'language' }).of(code); } catch { return code; }
}

function renderSearch() {
  const results = h('div', { class: 'results', 'aria-live': 'polite' });
  const input = h('input', { type: 'search', id: 'q', value: S.query, enterkeyhint: 'search', autocomplete: 'off', required: true });
  const kindToggle = h('fieldset', { class: 'segmented' }, h('legend', { class: 'sr-only' }, t('search.kind')),
    ['movie', 'tv'].map((k) => h('label', null,
      h('input', { type: 'radio', name: 'kind', value: k, checked: S.kind === k, onchange: () => { S.kind = k; persist(); doSearch(); } }),
      h('span', null, t(`kind.${k}`)))));
  const langSel = h('select', { id: 'lang', onchange: (e) => { S.lang = e.target.value; persist(); doSearch(); } },
    LANGUAGES.map((l) => h('option', { value: l, selected: l === S.lang }, `${langName(l)} (${l})`)));

  async function doSearch() {
    S.query = input.value.trim();
    if (!S.query) return;
    clear(results, h('p', { class: 'muted' }, t('search.searching')));
    try {
      const res = await get(`/api/meta/tmdb/search?kind=${S.kind}&lang=${encodeURIComponent(S.lang)}&q=${encodeURIComponent(S.query)}`);
      S.results = res.results;
      renderResults();
      announce(t('search.count', { n: res.results.length }));
    } catch (err) { clear(results, errorBox(err)); }
  }

  function renderResults() {
    if (!S.results.length) return clear(results, h('p', { class: 'muted' }, t('search.none')));
    clear(results, h('ul', { class: 'cards' }, S.results.map((r) => h('li', null,
      h('button', { type: 'button', class: 'card', onclick: () => pick(r) },
        r.poster ? h('img', { src: `/api/img/tmdb/w185${r.poster}`, alt: '', loading: 'lazy', width: 92, height: 138 })
          : h('div', { class: 'noposter', 'aria-hidden': 'true' }, '?'),
        h('div', { class: 'card-body' },
          h('strong', null, r.title, r.year ? ` (${r.year})` : ''),
          r.originalTitle ? h('div', { class: 'muted small' }, r.originalTitle) : null,
          h('p', { class: 'overview' }, r.overview || '')))))));
  }

  view(t('search.title'),
    h('form', { role: 'search', class: 'search', onsubmit: (e) => { e.preventDefault(); doSearch(); } },
      kindToggle,
      h('div', { class: 'row' },
        h('label', { for: 'q', class: 'sr-only' }, t('search.label')), input,
        h('button', { type: 'submit', class: 'primary' }, t('search.button'))),
      h('div', { class: 'row small' }, h('label', { for: 'lang' }, t('search.language')), langSel)),
    results,
    h('p', { class: 'hint' }, t('search.quickHint'), ' ', h('button', { type: 'button', class: 'linkish', onclick: startQuick }, t('nav.quick'))));
  if (S.results.length) renderResults();
  setTimeout(() => input.focus(), 0);
}

async function pick(r) {
  if (r.kind === 'tv') return pickShow(r);
  try {
    const m = await get(`/api/meta/tmdb/movie/${r.id}?lang=${encodeURIComponent(S.lang)}`);
    S.selected = { id: m.id, kind: 'movie', title: m.title, year: m.year, poster: m.poster, overview: m.overview };
    S.title = m.title;
    S.folder = { mode: 'auto', name: '' };
    S.policies = {};
    S.plan = null;
    S.needsAutoMap = true;
    go('files');
  } catch (err) { alert(errText(err)); }
}

// ---------- step 3: add files ----------

function addFiles(list) {
  const { accepted, ignored } = filterFiles(list, new Set([...S.videoExts, ...S.subExts]));
  const have = new Set(S.files.map((f) => fingerprint(f.file)));
  for (const { file, path } of accepted) {
    if (have.has(fingerprint(file))) continue;
    have.add(fingerprint(file));
    S.files.push({ key: uid(), file, name: file.name, path, size: file.size, lastModified: file.lastModified });
  }
  S.ignored = ignored;
  S.needsAutoMap = true; // re-suggest the mapping for the new set
  S.plan = null;
}

function renderFiles() {
  const list = h('div');
  const picker = h('input', { type: 'file', multiple: true, hidden: true, accept: [...S.videoExts, ...S.subExts].map((e) => `.${e}`).join(','), onchange: (e) => { addFiles(fromInput(e.target)); e.target.value = ''; renderList(); } });
  const folderPicker = h('input', { type: 'file', multiple: true, hidden: true, webkitdirectory: true, onchange: (e) => { addFiles(fromInput(e.target)); e.target.value = ''; renderList(); } });
  const touch = matchMedia('(pointer: coarse)').matches;
  const canFolder = 'webkitdirectory' in HTMLInputElement.prototype && !touch;

  const drop = h('div', {
    class: 'dropzone', role: 'button', tabindex: 0, 'aria-label': t('files.dropLabel'),
    onclick: () => picker.click(),
    onkeydown: (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); picker.click(); } },
    ondragover: (e) => { e.preventDefault(); drop.classList.add('over'); },
    ondragleave: () => drop.classList.remove('over'),
    ondrop: async (e) => {
      e.preventDefault();
      drop.classList.remove('over');
      addFiles(await filesFromDataTransfer(e.dataTransfer));
      renderList();
    },
  }, h('p', null, touch ? t('files.tapToAdd') : t('files.dropHere')), h('p', { class: 'muted small' }, t('files.nothingUploaded')));

  const next = h('button', { type: 'button', class: 'primary', onclick: () => go(S.selected.kind === 'quick' ? 'review' : 'map') }, t('nav.next'));

  function renderList() {
    next.disabled = !S.files.length;
    clear(list,
      S.ignored.length ? h('details', { class: 'ignored' },
        h('summary', null, t('files.ignored', { n: S.ignored.length })),
        h('ul', null, S.ignored.map((i) => h('li', null, `${i.name} — ${t(`files.reason.${i.reason}`)}`)))) : null,
      S.files.length ? h('ul', { class: 'filelist' }, S.files.map((f) => h('li', null,
        h('span', { class: 'fname' }, f.path), h('span', { class: 'muted small' }, fmtBytes(f.size)),
        h('button', {
          type: 'button', class: 'ghost icon', 'aria-label': t('files.remove', { name: f.name }),
          onclick: () => { S.files = S.files.filter((x) => x !== f); S.needsAutoMap = true; renderList(); },
        }, '✕')))) : h('p', { class: 'muted' }, t('files.none')));
  }

  view(t('files.title'), selectedCard(), drop, picker, folderPicker,
    h('div', { class: 'row' },
      h('button', { type: 'button', onclick: () => picker.click() }, t('files.chooseFiles')),
      canFolder ? h('button', { type: 'button', onclick: () => folderPicker.click() }, t('files.chooseFolder')) : null),
    list,
    navRow(h('button', { type: 'button', onclick: () => go({ tv: 'seasons', quick: 'folder' }[S.selected.kind] || 'search') }, t('nav.back')), next));
  renderList();
}

// ---------- step 4: map ----------

function renderMap() {
  if (S.needsAutoMap || S.files.some((f) => !f.role)) {
    S.files = suggestMovieRoles(S.files, S.videoExts, S.subExts);
    S.needsAutoMap = false;
  }
  const problems = h('div', { 'aria-live': 'polite' });
  const next = h('button', { type: 'button', class: 'primary', onclick: () => go('review') }, t('nav.next'));
  const rows = h('ul', { class: 'maplist' });

  function check() {
    const p = validateMovieMapping(S.files);
    next.disabled = p.length > 0;
    clear(problems, p.map((code) => h('p', { class: 'error' }, t(`error.${code}`))));
    S.plan = null;
  }

  function renderRows() {
    const movies = S.files.filter((f) => f.role === 'movie');
    clear(rows, S.files.map((f) => {
      const isVideo = S.videoExts.has(extOf(f.name));
      const roles = isVideo ? ['movie', 'extra', 'ignore'] : ['subtitle', 'ignore'];
      const details = [];
      if (f.role === 'movie') {
        details.push(field(t('map.versionLabel'), h('input', {
          value: f.version, placeholder: t('map.versionPlaceholder'), maxlength: 60,
          oninput: (e) => { f.version = e.target.value; check(); },
        })));
      } else if (f.role === 'extra') {
        details.push(field(t('map.extraType'), h('select', { onchange: (e) => { f.extraType = e.target.value; check(); } },
          S.session.extraTypes.map((x) => h('option', { value: x, selected: f.extraType === x }, t(`extra.${x}`))))));
        details.push(field(t('map.extraTitle'), h('input', { value: f.label, maxlength: 200, oninput: (e) => { f.label = e.target.value; check(); } })));
      } else if (f.role === 'subtitle') {
        details.push(field(t('map.subLang'), h('input', {
          value: f.lang, placeholder: 'en', maxlength: 10, size: 6, autocapitalize: 'none',
          oninput: (e) => { f.lang = e.target.value.trim(); check(); },
        })));
        details.push(h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: f.forced, onchange: (e) => { f.forced = e.target.checked; check(); } }), t('map.forced')));
        details.push(h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: f.sdh, onchange: (e) => { f.sdh = e.target.checked; check(); } }), t('map.sdh')));
        if (movies.length > 1) {
          details.push(field(t('map.subFor'), h('select', { onchange: (e) => { f.version = e.target.value; check(); } },
            movies.map((m) => h('option', { value: m.version, selected: f.version === m.version }, m.version || m.name)))));
        }
      }
      return h('li', { class: `maprow role-${f.role}` },
        h('div', { class: 'maphead' }, h('span', { class: 'fname' }, f.path), h('span', { class: 'muted small' }, fmtBytes(f.size))),
        h('div', { class: 'mapfields' },
          field(t('map.role'), h('select', {
            onchange: (e) => {
              f.role = e.target.value;
              if (f.role === 'subtitle' && movies[0]) f.version = movies[0].version;
              renderRows();
              check();
            },
          }, roles.map((r) => h('option', { value: r, selected: f.role === r }, t(`role.${r}`))))),
          details));
    }));
  }

  view(t('map.title'), selectedCard(), h('p', { class: 'hint' }, t('map.hintMovie')), rows, problems,
    navRow(h('button', { type: 'button', onclick: () => go('files') }, t('nav.back')), next));
  renderRows();
  check();
}

// ---------- step 5: review ----------

function movieRequest(withPolicies) {
  return {
    provider: 'tmdb',
    id: S.selected.id,
    language: S.lang,
    title: S.title.trim(),
    folder: S.folder,
    items: S.files.filter((f) => f.role !== 'ignore').map((f) => ({
      key: f.key, name: f.name, size: f.size, role: f.role, version: f.version.trim(), extraType: f.extraType,
      label: f.label, lang: f.lang, forced: f.forced, sdh: f.sdh, policy: withPolicies ? (S.policies[f.key] || '') : '',
    })),
  };
}

function renderReview() {
  const body = h('div', { 'aria-live': 'polite' }, h('p', { class: 'muted' }, t('review.planning')));
  const confirm = h('button', { type: 'button', class: 'primary', disabled: true, onclick: start }, t('review.confirm'));
  const titleInput = h('input', {
    id: 'dtitle', value: S.title, maxlength: 200,
    onchange: (e) => { S.title = e.target.value; persist(); plan(); },
  });

  async function plan() {
    confirm.disabled = true;
    try {
      S.plan = await post('/api/plan/movie', movieRequest(false));
      renderPlan();
    } catch (err) { clear(body, errorBox(err)); }
  }

  function renderPlan() {
    const p = S.plan;
    const conflicts = p.items.filter((i) => i.exists);
    const undecided = conflicts.filter((i) => !S.policies[i.key]);
    confirm.disabled = undecided.length > 0 || !p.summary.enoughSpace;
    const folderChoice = p.folderCandidates.length ? h('fieldset', { class: 'stack' },
      h('legend', null, t('review.folderChoice')),
      [{ mode: 'new', name: '', label: t('review.folderNew') }, ...p.folderCandidates.map((c) => ({ mode: 'existing', name: c, label: c }))].map((o) =>
        h('label', { class: 'check' }, h('input', {
          type: 'radio', name: 'folder', checked: (S.folder.mode === o.mode && S.folder.name === o.name) || (S.folder.mode === 'auto' && o.mode === 'new'),
          onchange: () => { S.folder = { mode: o.mode, name: o.name }; plan(); },
        }), o.label))) : null;

    clear(body,
      h('dl', { class: 'summary' },
        h('dt', null, t('review.folder')), h('dd', null, h('code', null, `${p.libraryDir}/${p.folder}`), ' ',
          h('span', { class: 'badge' }, t(`review.folderSource.${p.folderSource}`)),
          p.folderExists ? h('span', { class: 'badge' }, t('review.folderExists')) : null),
        h('dt', null, t('review.files')), h('dd', null, String(p.summary.files)),
        h('dt', null, t('review.size')), h('dd', null, fmtBytes(p.summary.bytes)),
        h('dt', null, t('review.free')), h('dd', { class: p.summary.enoughSpace ? '' : 'bad' }, fmtBytes(p.summary.freeBytes)),
        h('dt', null, t('review.conflicts')), h('dd', { class: p.summary.conflicts ? 'bad' : '' }, String(p.summary.conflicts))),
      folderChoice,
      p.warnings.map((w) => h('p', { class: 'warning' }, t(`warning.${w.code}`, w.params))),
      h('ul', { class: 'planlist' }, p.items.map((i) => h('li', { class: i.exists ? 'conflict' : '' },
        h('div', { class: 'muted small' }, i.name, ' → '),
        h('code', { class: 'path' }, i.path),
        i.exists ? h('div', { class: 'field' },
          h('label', { for: `pol-${i.key}` }, t('review.exists')),
          h('select', {
            id: `pol-${i.key}`, onchange: (e) => { S.policies[i.key] = e.target.value; renderPlan(); },
          }, h('option', { value: '', selected: !S.policies[i.key] }, t('review.choose')),
          ['skip', 'replace', 'keep_both'].map((pol) => h('option', { value: pol, selected: S.policies[i.key] === pol }, t(`policy.${pol}`))))) : null))));
  }

  async function start() {
    confirm.disabled = true;
    try {
      startBatch(await post('/api/batches/movie', movieRequest(true)));
    } catch (err) {
      clear(body, errorBox(err));
      confirm.disabled = false;
    }
  }

  view(t('review.title'), selectedCard(),
    h('div', { class: 'field' }, h('label', { for: 'dtitle' }, t('review.displayTitle')), titleInput,
      h('p', { class: 'hint' }, t('review.displayTitleHint'))),
    body,
    navRow(h('button', { type: 'button', onclick: () => go('map') }, t('nav.back')), confirm));
  plan();
}

// ---------- step 6: upload and publish ----------

let renderPending = false;
function scheduleUploadRender() {
  if (renderPending || S.step !== 'upload') return;
  renderPending = true;
  requestAnimationFrame(() => { renderPending = false; updateUploadView(); });
}

let uploadRefs = null;
let hookPoll = null;

function fileForItem(it) {
  const byKey = S.files.find((f) => f.key === it.key);
  if (byKey) return byKey.file;
  const fp = S.fps[it.id];
  return S.files.find((f) => fingerprint(f.file) === fp)?.file || null;
}

function startUploads() {
  for (const it of S.batch.items) {
    if (S.uploads.has(it.id) || !['pending', 'uploading'].includes(it.status)) continue;
    const file = fileForItem(it);
    if (!file) continue;
    const u = new Upload({
      file, itemId: it.id, batchId: S.batch.id, url: it.uploadUrl || null, offset: it.offset || 0,
      onChange: (up) => { if (up.state === 'done' || up.state === 'error') refreshBatch(); scheduleUploadRender(); },
    });
    S.uploads.set(it.id, u);
    queue.add(u);
  }
}

async function refreshBatch() {
  try {
    S.batch = await get(`/api/batches/${S.batch.id}`);
    persist();
    renderUploadList();
    if (S.batch.status === 'done') announce(t('upload.allDone'));
  } catch { /* transient; next event retries */ }
}

function renderUpload() {
  const reselect = h('input', {
    type: 'file', multiple: true, hidden: true,
    onchange: (e) => {
      for (const { file, path } of fromInput(e.target)) S.files.push({ key: uid(), file, name: file.name, path, size: file.size });
      e.target.value = '';
      startUploads();
      renderUploadList();
    },
  });
  uploadRefs = {
    overall: h('progress', { max: 1, value: 0, 'aria-label': t('upload.overall') }),
    overallText: h('p', { class: 'muted small' }),
    wake: h('p', { class: 'muted small' }),
    list: h('ul', { class: 'uploadlist' }),
    missing: h('div'),
    reselect,
    actions: h('div', { class: 'nav-row' }),
    rows: new Map(),
  };
  view(t('upload.title'), S.selected ? selectedCard() : null, uploadRefs.overall, uploadRefs.overallText, uploadRefs.wake,
    uploadRefs.missing, reselect, uploadRefs.list, uploadRefs.actions);
  startUploads();
  renderUploadList();
}

function renderUploadList() {
  if (!uploadRefs || S.step !== 'upload') return;
  const b = S.batch;
  uploadRefs.rows.clear();
  clear(uploadRefs.list, b.items.map((it) => {
    const progress = h('progress', { max: it.size, value: it.offset, 'aria-label': t('upload.progressOf', { name: it.name }) });
    const status = h('span', { class: 'status' });
    const buttons = h('div', { class: 'row small' });
    uploadRefs.rows.set(it.id, { progress, status, buttons, item: it });
    return h('li', { class: `up status-${it.status}` },
      h('div', { class: 'fname' }, it.episode ? h('strong', null, `${it.episode} · `) : null, it.name),
      h('code', { class: 'path small' }, it.finalPath || it.target),
      progress, h('div', { class: 'row small' }, status, buttons),
      it.permWarning ? h('p', { class: 'warning small' }, t('upload.permWarning', { msg: it.permWarning })) : null,
      it.trashedPath ? h('p', { class: 'muted small' }, t('upload.replaced', { path: it.trashedPath })) : null);
  }));

  const missing = b.items.filter((it) => ['pending', 'uploading'].includes(it.status) && !S.uploads.has(it.id));
  clear(uploadRefs.missing, missing.length && b.status === 'active' ? h('div', { class: 'warning' },
    h('p', null, t('upload.reselect', { n: missing.length })),
    h('ul', null, missing.map((it) => h('li', null, `${it.name} (${fmtBytes(it.size)})`))),
    h('button', { type: 'button', class: 'primary', onclick: () => uploadRefs.reselect.click() }, t('upload.reselectButton'))) : null);

  if (b.status === 'done' && S.session.jellyfin) {
    uploadRefs.missing.append(h('p', { class: 'muted small' }, b.hook ? t(`history.hook.${b.hook.split(':')[0]}`) : t('upload.jellyfinPending')));
    if (!b.hook && !hookPoll) hookPoll = setTimeout(() => { hookPoll = null; refreshBatch(); }, 2500);
  }
  clear(uploadRefs.actions,
    b.status === 'active' ? h('button', {
      type: 'button', class: 'danger', onclick: async () => {
        if (!confirm(t('upload.cancelConfirm'))) return;
        for (const u of S.uploads.values()) u.pause();
        try { S.batch = await post(`/api/batches/${b.id}/cancel`, {}); } catch (err) { alert(errText(err)); }
        renderUploadList();
      },
    }, t('upload.cancelAll')) : null,
    b.status !== 'active' ? h('button', { type: 'button', class: 'primary', onclick: startOver }, t('upload.startOver')) : null);
  updateUploadView();
}

function updateUploadView() {
  if (!uploadRefs) return;
  let total = 0;
  let sent = 0;
  for (const it of S.batch.items) {
    if (it.status === 'skipped' || it.status === 'cancelled') continue;
    total += it.size;
    const u = S.uploads.get(it.id);
    sent += it.status === 'published' ? it.size : u ? u.sent : it.offset;
    const row = uploadRefs.rows.get(it.id);
    if (!row) continue;
    row.progress.value = it.status === 'published' ? it.size : u ? u.sent : it.offset;
    // The server status is authoritative once terminal; before that the
    // client knows better (uploading, retrying, paused…).
    const terminal = ['published', 'skipped', 'conflict', 'failed', 'cancelled'].includes(it.status);
    const state = !terminal && u ? u.state : it.status;
    row.status.textContent = t(`state.${state}`) + (u?.error ? ` (${u.error})` : '') + (it.error && it.status !== 'published' ? ` — ${errorItemText(it.error)}` : '');
    clear(row.buttons, itemButtons(it, u));
  }
  uploadRefs.overall.max = total || 1;
  uploadRefs.overall.value = sent;
  const done = S.batch.items.filter((i) => i.status === 'published').length;
  uploadRefs.overallText.textContent = t('upload.overallText', { done, total: S.batch.items.length, sent: fmtBytes(sent), size: fmtBytes(total) });
  uploadRefs.wake.textContent = queue.lock ? t('upload.wakeOn') : queue.busy() ? t('upload.wakeOff') : '';
}

function errorItemText(code) {
  const s = t(`itemError.${code}`);
  return s === `itemError.${code}` ? code : s;
}

function itemButtons(it, u) {
  const out = [];
  if (it.status === 'conflict') {
    for (const pol of ['skip', 'replace', 'keep_both']) {
      out.push(h('button', {
        type: 'button', onclick: async () => {
          try { S.batch = await post(`/api/batches/${S.batch.id}/items/${it.id}/resolve`, { policy: pol }); renderUploadList(); } catch (err) { alert(errText(err)); }
        },
      }, t(`policy.${pol}`)));
    }
    return out;
  }
  if (!u) {
    if (it.status === 'failed' || it.status === 'cancelled') {
      const file = fileForItem(it);
      if (file && S.batch.status === 'active') {
        out.push(h('button', { type: 'button', onclick: () => { it.status = 'pending'; it.uploadUrl = ''; startUploads(); renderUploadList(); } }, t('upload.retry')));
      }
    }
    return out;
  }
  if (['uploading', 'retrying', 'queued'].includes(u.state)) {
    out.push(h('button', { type: 'button', onclick: () => u.pause() }, t('upload.pause')));
  }
  if (['paused', 'error'].includes(u.state)) {
    out.push(h('button', { type: 'button', onclick: () => queue.resume(u) }, u.state === 'error' ? t('upload.retry') : t('upload.resume')));
  }
  if (!['done', 'cancelled'].includes(u.state) && it.status !== 'published') {
    out.push(h('button', {
      type: 'button', class: 'ghost', onclick: async () => { await u.cancel(); S.uploads.delete(it.id); refreshBatch(); },
    }, t('upload.cancel')));
  }
  return out;
}

function startOver() {
  resetWizard();
  go('search');
}

window.addEventListener('beforeunload', (e) => {
  if (queue?.busy()) { e.preventDefault(); e.returnValue = ''; }
});

// ---------- settings ----------

async function renderSettings() {
  let data;
  try { data = await get('/api/settings'); } catch (err) { return renderMessage(t('settings.title'), errText(err)); }
  const status = h('div', { 'aria-live': 'polite' });
  const preset = h('select', { id: 'preset' }, Object.keys(data.presets).map((p) => h('option', { value: p, selected: data.naming.preset === p }, p)));
  const movieTpl = h('input', { id: 'movietpl', value: data.naming.movieTemplate || '', spellcheck: 'false' });
  const tvTpl = h('input', { id: 'tvtpl', value: data.naming.tvTemplate || '', spellcheck: 'false' });
  const placeholders = () => {
    movieTpl.placeholder = data.presets[preset.value].movie;
    tvTpl.placeholder = data.presets[preset.value].tv;
  };
  preset.addEventListener('change', placeholders);
  placeholders();
  const enabled = new Set(data.parser.packs);
  const packBoxes = data.packs.map((p) => h('label', { class: 'check' },
    h('input', { type: 'checkbox', value: p.id, checked: enabled.has(p.id) }),
    h('span', null, h('strong', null, p.name), p.description ? ` — ${p.description}` : '',
      h('span', { class: 'muted small' }, ` (${p.id}${p.source === 'file' ? `, ${t('settings.packFromFile')}` : ''})`))));
  const custom = h('textarea', { id: 'customrules', rows: 8, spellcheck: 'false', placeholder: t('settings.customPlaceholder') });
  custom.value = data.parser.custom || '';

  clear(main(), h('section', { class: 'view' }, h('h2', { tabindex: -1 }, t('settings.title')),
    h('form', {
      class: 'stack',
      onsubmit: async (e) => {
        e.preventDefault();
        try {
          const packs = packBoxes.map((l) => l.querySelector('input')).filter((i) => i.checked).map((i) => i.value);
          await put('/api/settings', {
            naming: { preset: preset.value, movieTemplate: movieTpl.value, tvTemplate: tvTpl.value },
            parser: { packs, custom: custom.value },
          });
          S.rules = compileRules(await get('/api/parser/rules'));
          S.needsAutoMap = true;
          clear(status, h('p', { class: 'ok' }, t('settings.saved')));
        } catch (err) { clear(status, errorBox(err)); }
      },
    },
    h('h3', null, t('settings.naming')),
    h('label', { for: 'preset' }, t('settings.preset')), preset,
    h('label', { for: 'movietpl' }, t('settings.movieTemplate')), movieTpl,
    h('label', { for: 'tvtpl' }, t('settings.tvTemplate')), tvTpl,
    h('p', { class: 'hint' }, t('settings.templateHelp')),
    h('h3', null, t('settings.rules')),
    h('p', { class: 'hint' }, t('settings.rulesHint')),
    h('fieldset', { class: 'stack' }, h('legend', null, t('settings.packs')), packBoxes),
    h('label', { for: 'customrules' }, t('settings.customRules')), custom,
    h('p', { class: 'hint' }, t('settings.customHint')),
    h('h3', null, t('settings.server')),
    h('ul', { class: 'small' },
      h('li', null, data.jellyfin ? t('settings.jellyfinOn') : t('settings.jellyfinOff')),
      h('li', null, data.trashRetentionDays ? t('settings.trashDays', { n: data.trashRetentionDays }) : t('settings.trashForever'))),
    h('div', { class: 'nav-row' },
      h('button', { type: 'button', onclick: () => go(S.step) }, t('nav.back')),
      h('button', { type: 'submit', class: 'primary' }, t('settings.save'))),
    status)));
  main().querySelector('h2').focus();
}

boot();
