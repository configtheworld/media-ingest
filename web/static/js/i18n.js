// UI strings live in /i18n/<lang>.json. English is the fallback for any
// missing key. The language follows the browser unless the user picks one.

// Native names, shown in the language switcher.
export const UI_LANGUAGES = { en: 'English', de: 'Deutsch', es: 'Español', tr: 'Türkçe' };
const STORE_KEY = 'mi.uiLang';

let strings = {};
let fallback = {};
let current = 'en';

// uiLanguage returns the stored choice or the best match for the browser.
export function uiLanguage() {
  try {
    const saved = localStorage.getItem(STORE_KEY);
    if (saved && saved in UI_LANGUAGES) return saved;
  } catch { /* storage blocked */ }
  for (const tag of navigator.languages || [navigator.language || 'en']) {
    const base = String(tag).toLowerCase().split('-')[0];
    if (base in UI_LANGUAGES) return base;
  }
  return 'en';
}

export function setUILanguage(code) {
  if (!(code in UI_LANGUAGES)) return;
  try { localStorage.setItem(STORE_KEY, code); } catch { /* storage blocked */ }
  location.reload();
}

export function currentLanguage() { return current; }

export async function loadI18n() {
  fallback = await fetchJSON('/i18n/en.json');
  current = uiLanguage();
  strings = current === 'en' ? fallback : (await fetchJSON(`/i18n/${current}.json`).catch(() => null)) || fallback;
  if (strings === fallback) current = 'en';
  document.documentElement.lang = current;
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
