// Every locale must have exactly the English keys, no empty strings and the
// same {placeholders}, so no message can lose a number or a name.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const dir = new URL('../static/i18n/', import.meta.url);
const en = JSON.parse(fs.readFileSync(new URL('en.json', dir)));
const placeholders = (s) => (s.match(/\{[A-Za-z_]+(?::\d+)?\}/g) || []).sort();
const locales = fs.readdirSync(dir).filter((f) => f.endsWith('.json') && f !== 'en.json');

test('at least one translation ships', () => assert.ok(locales.length >= 3, locales.join(',')));

for (const file of locales) {
  const tr = JSON.parse(fs.readFileSync(new URL(file, dir)));
  test(`${file}: same keys as en.json`, () => {
    assert.deepEqual(Object.keys(tr).sort(), Object.keys(en).sort());
  });
  test(`${file}: no empty strings, same placeholders`, () => {
    for (const [k, v] of Object.entries(en)) {
      assert.ok(typeof tr[k] === 'string' && tr[k].trim(), `${k} is empty`);
      assert.deepEqual(placeholders(tr[k]), placeholders(v), `${k}: placeholders differ`);
    }
  });
}

test('switcher lists every shipped locale', async () => {
  const { UI_LANGUAGES } = await import('../static/js/i18n.js');
  assert.deepEqual(Object.keys(UI_LANGUAGES).sort(), ['en', ...locales.map((f) => f.replace('.json', ''))].sort());
});
