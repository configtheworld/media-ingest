// Resumable uploads using the tus 1.0.0 protocol, a small parallel queue and
// the Screen Wake Lock API.

import { csrf } from './api.js';

const CHUNK = 8 * 1024 * 1024; // keeps every request below common proxy body limits
const RETRY_DELAYS = [1, 2, 4, 8, 15, 30, 30, 30, 60, 60, 60, 60]; // seconds, then give up

class HttpError extends Error {
  constructor(status, offset) { super(`HTTP ${status}`); this.status = status; this.offset = offset; }
}
class Aborted extends Error {}

function b64(s) {
  return btoa(String.fromCharCode(...new TextEncoder().encode(s)));
}

function tusFetch(method, url, headers = {}) {
  return fetch(url, {
    method,
    credentials: 'same-origin',
    redirect: 'manual',
    headers: { 'Tus-Resumable': '1.0.0', 'X-CSRF-Token': csrf(), ...headers },
  });
}

// One file upload. States: queued, uploading, paused, retrying, done, error, cancelled.
export class Upload {
  constructor({ file, itemId, batchId, url, offset = 0, onChange }) {
    Object.assign(this, { file, itemId, batchId, url, offset, onChange });
    this.state = 'queued';
    this.error = '';
    this.xhr = null;
    this.wakeRetry = null;
  }

  set(state, error = '') {
    this.state = state;
    this.error = error;
    this.onChange?.(this);
  }

  async run() {
    if (this.state === 'done' || this.state === 'cancelled') return;
    this.set('uploading');
    let attempt = 0;
    for (;;) {
      try {
        if (this.url) await this.syncOffset();
        if (!this.url) await this.create();
        while (this.offset < this.file.size) {
          if (this.state === 'paused' || this.state === 'cancelled') return;
          this.offset = await this.patch(this.offset);
          attempt = 0;
          this.onChange?.(this);
        }
        this.set('done');
        return;
      } catch (err) {
        if (err instanceof Aborted || this.state === 'paused' || this.state === 'cancelled') return;
        if (err instanceof HttpError && err.status === 404) { this.url = null; this.offset = 0; }
        else if (err instanceof HttpError && ![0, 409, 423, 429, 500, 502, 503, 504].includes(err.status)) {
          this.set('error', `HTTP ${err.status}`);
          return;
        }
        if (attempt >= RETRY_DELAYS.length) { this.set('error', 'network'); return; }
        this.set('retrying');
        await this.waitRetry(RETRY_DELAYS[attempt++]);
        if (this.state !== 'retrying') return; // paused or cancelled meanwhile
        this.set('uploading');
      }
    }
  }

  // Wait, but retry immediately when the device comes back online or the
  // page becomes visible again (phone woke up).
  waitRetry(seconds) {
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        window.removeEventListener('online', done);
        document.removeEventListener('visibilitychange', onVis);
        this.wakeRetry = null;
        resolve();
      };
      const onVis = () => { if (document.visibilityState === 'visible') done(); };
      const timer = setTimeout(done, seconds * 1000);
      window.addEventListener('online', done);
      document.addEventListener('visibilitychange', onVis);
      this.wakeRetry = done;
    });
  }

  async create() {
    const meta = `batch ${b64(this.batchId)},item ${b64(this.itemId)},filename ${b64(this.file.name)}`;
    const res = await tusFetch('POST', '/api/uploads', { 'Upload-Length': String(this.file.size), 'Upload-Metadata': meta });
    if (res.status !== 201) throw new HttpError(res.status === 0 ? 401 : res.status);
    this.url = res.headers.get('Location');
    this.offset = 0;
  }

  async syncOffset() {
    let res;
    try { res = await tusFetch('HEAD', this.url); } catch { throw new HttpError(0); }
    if (res.status === 404 || res.status === 410) throw new HttpError(404);
    if (!res.ok) throw new HttpError(res.status);
    this.offset = parseInt(res.headers.get('Upload-Offset'), 10) || 0;
  }

  patch(offset) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      this.xhr = xhr;
      xhr.open('PATCH', this.url);
      xhr.setRequestHeader('Tus-Resumable', '1.0.0');
      xhr.setRequestHeader('Upload-Offset', String(offset));
      xhr.setRequestHeader('Content-Type', 'application/offset+octet-stream');
      xhr.setRequestHeader('X-CSRF-Token', csrf());
      xhr.upload.onprogress = (e) => { this.progress = offset + e.loaded; this.onChange?.(this); };
      xhr.onload = () => {
        this.xhr = null;
        this.progress = undefined;
        if (xhr.status === 204) resolve(parseInt(xhr.getResponseHeader('Upload-Offset'), 10));
        else reject(new HttpError(xhr.status));
      };
      xhr.onerror = () => { this.xhr = null; reject(new HttpError(0)); };
      xhr.onabort = () => { this.xhr = null; reject(new Aborted()); };
      xhr.send(this.file.slice(offset, Math.min(offset + CHUNK, this.file.size)));
    });
  }

  pause() {
    if (['uploading', 'retrying', 'queued'].includes(this.state)) {
      this.set('paused');
      this.xhr?.abort();
      this.wakeRetry?.();
    }
  }

  async cancel() {
    this.set('cancelled');
    this.xhr?.abort();
    this.wakeRetry?.();
    if (this.url) await tusFetch('DELETE', this.url).catch(() => {});
  }

  get sent() { return this.progress ?? this.offset; }
}

// Queue runs up to `parallel` uploads at once and holds a wake lock while busy.
export class Queue {
  constructor({ parallel = 3, onChange }) {
    this.parallel = parallel;
    this.onChange = onChange;
    this.items = [];
    this.lock = null;
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible' && this.busy()) this.acquireLock();
    });
  }

  add(upload) {
    this.items.push(upload);
    this.pump();
  }

  busy() { return this.items.some((u) => ['uploading', 'retrying'].includes(u.state)); }

  pump() {
    let running = this.items.filter((u) => ['uploading', 'retrying'].includes(u.state)).length;
    for (const u of this.items) {
      if (running >= this.parallel) break;
      if (u.state === 'queued') {
        running++;
        u.run().finally(() => { this.pump(); this.onChange?.(); });
      }
    }
    if (this.busy()) this.acquireLock(); else this.releaseLock();
    this.onChange?.();
  }

  resume(u) {
    if (['paused', 'error'].includes(u.state)) { u.set('queued'); this.pump(); }
  }

  async acquireLock() {
    if (this.lock || !('wakeLock' in navigator) || document.visibilityState !== 'visible') return;
    try {
      this.lock = await navigator.wakeLock.request('screen');
      this.lock.addEventListener('release', () => { this.lock = null; this.onChange?.(); });
      this.onChange?.();
    } catch { /* not allowed (battery saver, iframe): uploads continue anyway */ }
  }

  releaseLock() {
    this.lock?.release().catch(() => {});
    this.lock = null;
  }
}
