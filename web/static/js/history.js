// History of batches with details and undo.

import { h, clear, fmtBytes, announce } from './dom.js';
import { t, errText } from './i18n.js';
import { get, post } from './api.js';
import { S } from './state.js';
import { go, main, errorBox } from './ui.js';

const PAGE = 50;

function hookText(hook) {
  if (!hook) return null;
  const method = hook.split(':')[0];
  return t(`history.hook.${method}`);
}

export async function renderHistory() {
  const list = h('ul', { class: 'history' });
  const status = h('div', { 'aria-live': 'polite' });
  const more = h('button', { type: 'button', hidden: true, onclick: () => load(offset) }, t('history.more'));
  let offset = 0;

  clear(main(), h('section', { class: 'view' },
    h('h2', null, t('history.title')),
    h('p', { class: 'hint' }, t('history.hint')),
    status, list, more,
    h('div', { class: 'nav-row' }, h('button', { type: 'button', onclick: () => go(S.step) }, t('nav.back')))));
  main().querySelector('h2').tabIndex = -1;
  main().querySelector('h2').focus();

  async function load(from) {
    try {
      const res = await get(`/api/history?offset=${from}`);
      if (from === 0) clear(list);
      if (!res.batches.length && from === 0) list.append(h('li', { class: 'muted' }, t('history.empty')));
      res.batches.forEach((b) => list.append(entry(b)));
      offset = from + res.batches.length;
      more.hidden = res.batches.length < PAGE;
    } catch (err) { clear(status, errorBox(err)); }
  }

  function entry(b) {
    const details = h('div', { hidden: true });
    const li = h('li', { class: `hentry status-${b.status}` });
    const title = b.kind === 'quick' ? b.title : `${b.title}${b.year ? ` (${b.year})` : ''}`;
    const counts = [
      t('history.published', { n: b.published }),
      b.skipped ? t('history.skipped', { n: b.skipped }) : null,
      b.failed ? t('history.failed', { n: b.failed }) : null,
      b.undone ? t('history.undone', { n: b.undone }) : null,
    ].filter(Boolean).join(' · ');

    async function toggle(btn) {
      if (!details.hidden) { details.hidden = true; btn.setAttribute('aria-expanded', 'false'); return; }
      try {
        const full = await get(`/api/batches/${b.id}`);
        clear(details, h('ul', { class: 'hitems' }, full.items.map((it) => h('li', null,
          h('div', { class: 'small' }, it.episode ? h('strong', null, `${it.episode} · `) : null, it.name, ' ',
            h('span', { class: 'badge' }, t(`state.${it.status}`))),
          it.finalPath ? h('code', { class: 'path small' }, it.finalPath) : null,
          it.undoPath ? h('div', { class: 'muted small' }, t('history.movedTo', { path: it.undoPath })) : null,
          it.error ? h('div', { class: 'warning small' }, errText({ code: it.error })) : null))));
        details.hidden = false;
        btn.setAttribute('aria-expanded', 'true');
      } catch (err) { clear(details, errorBox(err)); details.hidden = false; }
    }

    async function undo(btn) {
      if (!confirm(t('history.undoConfirm', { n: b.published, title }))) return;
      btn.disabled = true;
      try {
        const res = await post(`/api/batches/${b.id}/undo`, {});
        const moved = res.items.filter((i) => i.status === 'undone').length;
        const kept = res.items.filter((i) => i.status === 'published').length;
        announce(t('history.undoDone', { n: moved }));
        clear(status, h('p', { class: kept ? 'warning' : 'ok' }, t('history.undoDone', { n: moved }), kept ? ` ${t('history.undoKept', { n: kept })}` : ''));
        load(0);
      } catch (err) { clear(status, errorBox(err)); btn.disabled = false; }
    }

    const detailsBtn = h('button', { type: 'button', 'aria-expanded': 'false', onclick: (e) => toggle(e.currentTarget) }, t('history.details'));
    clear(li,
      h('div', { class: 'maphead' },
        h('span', { class: 'badge' }, t(`kind.${b.kind}`)),
        h('strong', { class: 'fname' }, title),
        h('span', { class: `badge st-${b.status}` }, t(`history.status.${b.status}`))),
      h('div', { class: 'muted small' }, new Date(b.createdAt).toLocaleString(), ' · ', counts, b.bytes ? ` · ${fmtBytes(b.bytes)}` : '',
        hookText(b.hook) ? ` · ${hookText(b.hook)}` : ''),
      h('div', { class: 'row small' }, detailsBtn,
        b.canUndo ? h('button', { type: 'button', class: 'danger', onclick: (e) => undo(e.currentTarget) }, t('history.undo')) : null),
      details);
    return li;
  }

  load(0);
}
