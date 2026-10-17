// UI strings live in /i18n/<lang>.json. English is the fallback.

let strings = {};
let fallback = {};

export async function loadI18n() {
  fallback = await fetchJSON('/i18n/en.json');
  const want = (navigator.language || 'en').toLowerCase().split('-')[0];
  strings = want === 'en' ? fallback : (await fetchJSON(`/i18n/${want}.json`).catch(() => null)) || fallback;
  document.documentElement.lang = strings === fallback ? 'en' : want;
}

async function fetchJSON(url) {
  const res = await fetch(url, { credentials: 'same-origin' });
  if (!res.ok) throw new Error(url);
  return res.json();
}

// t('key', {n: 3}) -> string with {n} replaced.
export function t(key, params) {
  let s = strings[key] ?? fallback[key] ?? key;
  if (params) s = s.replace(/\{(\w+)\}/g, (m, k) => (k in params ? String(params[k]) : m));
  return s;
}

// Error codes from the API map to "error.<code>" strings.
export function errText(err) {
  const code = err?.code || 'unknown';
  const key = `error.${code}`;
  const s = t(key, err?.data?.params);
  return s === key ? t('error.unknown', { code }) : s;
}
