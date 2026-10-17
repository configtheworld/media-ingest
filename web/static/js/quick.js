// Quick upload: no metadata. Choose a folder under MEDIA_ROOT, add files,
// keep their names.

import { h, clear, fmtBytes } from './dom.js';
import { t } from './i18n.js';
import { get, post } from './api.js';
import { S, persist } from './state.js';
import { go, view, navRow, errorBox, selectedCard, field, warningList, startBatch } from './ui.js';

export function startQuick() {
  Object.assign(S, {
    kind: 'quick', selected: { kind: 'quick', id: null, title: '' }, files: [], ignored: [], plan: null, policies: {},
    quick: { folder: '', newFolder: '', keepStructure: true }, needsAutoMap: true,
  });
  go('folder');
}

function destination() {
  const q = S.quick;
  return [q.folder, q.newFolder.trim()].filter(Boolean).join('/');
}

// ---------- folder ----------

export function renderQuickFolder() {
  const q = S.quick || (S.quick = { folder: '', newFolder: '', keepStructure: true });
  const crumbs = h('nav', { class: 'crumbs', 'aria-label': t('quick.location') });
  const list = h('ul', { class: 'dirlist' });
  const dest = h('p', { class: 'dest' });
  const next = h('button', { type: 'button', class: 'primary', onclick: () => go('files') }, t('nav.next'));
  const status = h('div', { 'aria-live': 'polite' });

  function update() {
    S.selected.title = destination();
    next.disabled = !q.folder;
    clear(dest, t('quick.destination'), ' ', h('code', null, destination() || '—'));
    persist();
  }

  async function open(path) {
    try {
      const v = await get(`/api/browse?path=${encodeURIComponent(path)}`);
      q.folder = v.path;
      const parts = v.path ? v.path.split('/') : [];
      clear(crumbs,
        h('button', { type: 'button', class: 'ghost', onclick: () => open('') }, 'MEDIA_ROOT'),
        parts.map((p, i) => [' / ', h('button', { type: 'button', class: 'ghost', 'aria-current': i === parts.length - 1 ? 'location' : null, onclick: () => open(parts.slice(0, i + 1).join('/')) }, p)]));
      clear(list, v.dirs.length
        ? v.dirs.map((d) => h('li', null, h('button', { type: 'button', class: 'dir', onclick: () => open(v.path ? `${v.path}/${d}` : d) }, `📁 ${d}`)))
        : h('li', { class: 'muted' }, t('quick.noSubfolders')));
      clear(status);
      update();
    } catch (err) { clear(status, errorBox(err)); }
  }

  const newFolder = h('input', {
    id: 'newfolder', value: q.newFolder, maxlength: 200, autocomplete: 'off',
    oninput: (e) => { q.newFolder = e.target.value; update(); },
  });
  view(t('quick.title'), h('p', { class: 'hint' }, t('quick.hint')), crumbs, list, status,
    field(t('quick.newFolder'), newFolder), dest,
    navRow(h('button', { type: 'button', onclick: () => go('search') }, t('nav.back')), next));
  open(q.folder);
}

// ---------- review ----------

function quickRequest(withPolicies) {
  const q = S.quick;
  return {
    folder: q.folder, newFolder: q.newFolder.trim(), keepStructure: q.keepStructure,
    items: S.files.map((f) => ({ key: f.key, name: f.name, path: f.path || f.name, size: f.size, policy: withPolicies ? (S.policies[f.key] || '') : '' })),
  };
}

export function renderQuickReview() {
  const body = h('div', { 'aria-live': 'polite' }, h('p', { class: 'muted' }, t('review.planning')));
  const confirm = h('button', { type: 'button', class: 'primary', disabled: true, onclick: start }, t('review.confirm'));
  const nested = S.files.some((f) => (f.path || '').includes('/'));

  async function plan() {
    confirm.disabled = true;
    try {
      S.plan = await post('/api/plan/quick', quickRequest(false));
      renderPlan();
    } catch (err) { clear(body, errorBox(err)); }
  }

  function renderPlan() {
    const p = S.plan;
    const conflicts = p.items.filter((i) => i.conflict);
    confirm.disabled = conflicts.some((i) => !S.policies[i.key]) || !p.summary.enoughSpace;
    clear(body,
      h('dl', { class: 'summary' },
        h('dt', null, t('review.folder')), h('dd', null, h('code', null, p.title), p.folderExists ? null : h('span', { class: 'badge' }, t('review.folderSource.new'))),
        h('dt', null, t('review.files')), h('dd', null, String(p.summary.files)),
        h('dt', null, t('review.size')), h('dd', null, fmtBytes(p.summary.bytes)),
        h('dt', null, t('review.free')), h('dd', { class: p.summary.enoughSpace ? '' : 'bad' }, fmtBytes(p.summary.freeBytes)),
        h('dt', null, t('review.conflicts')), h('dd', { class: p.summary.conflicts ? 'bad' : '' }, String(p.summary.conflicts))),
      warningList(p.warnings),
      h('ul', { class: 'planlist' }, p.items.map((i) => h('li', { class: i.conflict ? 'conflict' : '' },
        h('div', { class: 'muted small' }, i.name, ' → '),
        h('code', { class: 'path' }, i.path),
        i.conflict ? field(t('review.exists'), h('select', { id: `pol-${i.key}`, onchange: (e) => { S.policies[i.key] = e.target.value; renderPlan(); } },
          h('option', { value: '', selected: !S.policies[i.key] }, t('review.choose')),
          ['skip', 'replace', 'keep_both'].map((pol) => h('option', { value: pol, selected: S.policies[i.key] === pol }, t(`policy.${pol}`))))) : null))));
  }

  async function start() {
    confirm.disabled = true;
    try {
      startBatch(await post('/api/batches/quick', quickRequest(true)));
    } catch (err) {
      clear(body, errorBox(err));
      confirm.disabled = false;
    }
  }

  view(t('review.title'), selectedCard(),
    nested ? h('label', { class: 'check' }, h('input', {
      type: 'checkbox', checked: S.quick.keepStructure, onchange: (e) => { S.quick.keepStructure = e.target.checked; plan(); },
    }), t('quick.keepStructure')) : null,
    body,
    navRow(h('button', { type: 'button', onclick: () => go('files') }, t('nav.back')), confirm));
  plan();
}
