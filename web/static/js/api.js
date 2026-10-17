// JSON API client. Unsafe methods carry the CSRF token.

let csrfToken = '';
export function setCsrf(tok) { csrfToken = tok || ''; }
export function csrf() { return csrfToken; }

export class ApiError extends Error {
  constructor(status, code, data) {
    super(code);
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

export async function api(method, url, body) {
  const opts = { method, headers: { Accept: 'application/json' }, credentials: 'same-origin', redirect: 'manual' };
  if (method !== 'GET') opts.headers['X-CSRF-Token'] = csrfToken;
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(url, opts);
  } catch {
    throw new ApiError(0, 'network');
  }
  // An expired proxy (Authelia) session answers with a redirect to the portal.
  if (res.type === 'opaqueredirect') throw new ApiError(401, 'session_expired');
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new ApiError(res.status, data?.error || `http_${res.status}`, data);
  return data;
}

export const get = (url) => api('GET', url);
export const post = (url, body) => api('POST', url, body);
export const put = (url, body) => api('PUT', url, body);
