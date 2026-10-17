// Navigation and view helpers shared by the movie and TV flows.

import { h, clear, uid } from './dom.js';
import { t, errText } from './i18n.js';
import { S, persist, stepsFor } from './state.js';
import { fingerprint } from './files.js';

export const main = () => document.getElementById('main');

const views = {};

// registerViews maps step names to render functions.
export function registerViews(map) { Object.assign(views, map); }

export function go(step) {
  S.step = step;
  persist();
  views[step]();
  const h2 = main().querySelector('h2');
  if (h2) { h2.tabIndex = -1; h2.focus({ preventScroll: false }); }
}

function stepper() {
  const steps = stepsFor(S.selected?.kind || S.kind);
  const idx = steps.indexOf(S.step);
  return h('nav', { class: 'stepper', 'aria-label': t('steps.label') },
    h('ol', null, steps.map((s, i) => h('li', {
      class: i < idx ? 'done' : i === idx ? 'current' : '',
      'aria-current': i === idx ? 'step' : null,
    }, h('span', { class: 'num', 'aria-hidden': 'true' }, String(i + 1)), h('span', { class: 'label' }, t(`steps.${s}`))))));
}

export function view(title, ...content) {
  clear(main(), stepper(), h('section', { class: 'view' }, h('h2', null, title), content));
}

export function navRow(...buttons) {
  return h('div', { class: 'nav-row' }, buttons);
}

export function errorBox(err) {
  return h('div', { class: 'error', role: 'alert' }, typeof err === 'string' ? err : errText(err));
}

export function renderMessage(title, text) {
  clear(main(), h('section', { class: 'view' }, h('h2', null, title), h('p', null, text)));
}

export function selectedCard() {
  const m = S.selected;
  if (m.kind === 'quick') {
    return h('div', { class: 'selected' },
      h('div', null, h('strong', null, t('kind.quick')), h('div', { class: 'muted small' }, h('code', null, m.title || '—'))),
      S.step === 'upload' ? null : h('button', { type: 'button', class: 'ghost', onclick: () => go('folder') }, t('files.change')));
  }
  return h('div', { class: 'selected' },
    m.poster ? h('img', { src: `/api/img/tmdb/w92${m.poster}`, alt: '', width: 46, height: 69 }) : null,
    h('div', null, h('strong', null, m.title, m.year ? ` (${m.year})` : ''),
      h('div', { class: 'muted small' }, `${t(`kind.${m.kind}`)} · TMDB ${m.id}`)),
    S.step === 'upload' ? null : h('button', { type: 'button', class: 'ghost', onclick: () => go('search') }, t('files.change')));
}

// field wraps a control with a visible label.
export function field(label, control, cls = '') {
  const id = control.id || `f-${uid()}`;
  control.id = id;
  return h('div', { class: `field ${cls}` }, h('label', { for: id }, label), control);
}

export function warningList(items) {
  return items.map((w) => h('p', { class: 'warning' }, t(`warning.${w.code}`, w.params)));
}

// startBatch switches to the upload step for a freshly created batch.
export function startBatch(b) {
  S.batch = b;
  S.fps = {};
  for (const it of b.items) {
    const f = S.files.find((x) => x.key === it.key);
    if (f) S.fps[it.id] = fingerprint(f.file);
  }
  S.uploads.clear();
  go('upload');
}
