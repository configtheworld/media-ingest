import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { compileRules } from '../static/js/parser.js';
import { autoAssign, assignSequential, assign, swapWith, nextFree, analyze, naturalCompare, episodeLabel } from '../static/js/tvmap.js';

const rules = compileRules(JSON.parse(fs.readFileSync(new URL('../../internal/parser/default.json', import.meta.url))));
const videoExts = new Set(['mkv', 'mp4']);
const eps = (n) => Array.from({ length: n }, (_, i) => ({ number: i + 1, title: `T${i + 1}` }));
const one = new Map([[1, eps(6)]]);
const two = new Map([[0, eps(2)], [1, eps(6)], [2, eps(4)]]);
const files = (...names) => names.map((name, i) => ({ key: `k${i}`, name, path: name, size: 1 }));
const map = (list) => Object.fromEntries(list.map((f) => [f.name, episodeLabel(f)]));

test('natural order', () => {
  const list = ['Ep 10.mkv', 'Ep 2.mkv', 'ep 1.mkv'].sort(naturalCompare);
  assert.deepEqual(list, ['ep 1.mkv', 'Ep 2.mkv', 'Ep 10.mkv']);
});

test('numbers from names, single selected season fills missing season', () => {
  const out = autoAssign(files('Show S01E02.mkv', 'Episode 3.mkv', 'random.mkv'), { episodes: one, rules, videoExts });
  assert.deepEqual(map(out), { 'Show S01E02.mkv': 'S01E02', 'Episode 3.mkv': 'S01E03', 'random.mkv': '' });
});

test('episode-only names need a season when several seasons are selected', () => {
  const out = autoAssign(files('Show S02E01.mkv', 'Episode 3.mkv', 'Season 1/Episode 4.mkv'), { episodes: two, rules, videoExts });
  assert.equal(episodeLabel(out[0]), 'S02E01');
  assert.equal(episodeLabel(out[1]), ''); // ambiguous: no season known
  assert.equal(episodeLabel(out[2]), 'S01E04'); // from the folder
});

test('season not selected stays unassigned', () => {
  const out = autoAssign(files('Show S05E01.mkv'), { episodes: one, rules, videoExts });
  assert.equal(out[0].episode, null);
});

test('fallback: nothing parses -> natural order from start episode', () => {
  const out = autoAssign(files('b.mkv', 'a10.mkv', 'a2.mkv', 'c.mkv'), { episodes: one, rules, videoExts, start: { season: 1, episode: 3 } });
  assert.deepEqual(map(out), { 'a2.mkv': 'S01E03', 'a10.mkv': 'S01E04', 'b.mkv': 'S01E05', 'c.mkv': 'S01E06' });
});

test('fallback does not run when something parsed', () => {
  const out = autoAssign(files('Show S01E01.mkv', 'sample.mkv'), { episodes: one, rules, videoExts, start: { season: 1, episode: 1 } });
  assert.equal(out[1].episode, null);
});

test('sequential assignment leaves extra files unassigned', () => {
  const list = autoAssign(files('a.mkv', 'b.mkv', 'c.mkv'), { episodes: new Map([[1, eps(2)]]), rules, videoExts });
  assignSequential(list, { season: 1, episode: 1 }, new Map([[1, eps(2)]]));
  assert.deepEqual(list.map(episodeLabel), ['S01E01', 'S01E02', '']);
});

test('ranges', () => {
  const out = autoAssign(files('Show S01E01-E02.mkv'), { episodes: one, rules, videoExts });
  assert.equal(episodeLabel(out[0]), 'S01E01-E02');
  const { warnings } = analyze(out, one, new Set());
  assert.ok(warnings.some((w) => w.code === 'multi_episode_files'));
});

test('subtitles follow their video and keep language info', () => {
  const out = autoAssign(files('Pilot.mkv', 'Pilot.de.forced.srt', 'Other.en.srt'), { episodes: one, rules, videoExts, start: { season: 1, episode: 1 } });
  const sub = out.find((f) => f.name === 'Pilot.de.forced.srt');
  assert.equal(episodeLabel(sub), 'S01E01');
  assert.equal(sub.kind, 'subtitle');
  assert.equal(sub.lang, 'de');
  assert.equal(sub.forced, true);
  assert.equal(out.find((f) => f.name === 'Other.en.srt').episode, null);
});

test('duplicates require distinct alternate-version labels', () => {
  const out = autoAssign(files('Show S01E01 1080p.mkv', 'Show S01E01 2160p.mkv'), { episodes: one, rules, videoExts });
  let r = analyze(out, one, new Set());
  assert.equal(r.problems[0].code, 'alternate_version_required');
  assert.equal(r.needVersion.size, 2);
  out[0].version = '1080p';
  out[1].version = '2160p';
  r = analyze(out, one, new Set());
  assert.deepEqual(r.problems, []);
  assert.ok(r.warnings.some((w) => w.code === 'alternate_versions'));
});

test('gaps, library and unknown episodes', () => {
  const out = autoAssign(files('S01E01.mkv', 'S01E05.mkv', 'S01E09.mkv'), { episodes: one, rules, videoExts });
  const { warnings } = analyze(out, one, new Set(['1:3']));
  const byCode = Object.fromEntries(warnings.map((w) => [w.code, w.params]));
  assert.equal(byCode.gaps.episodes, 'S01E02, S01E04, S01E06, S01E07, S01E08');
  assert.equal(byCode.unknown_episodes.episodes, 'S01E09');
});

test('episodes already in the library are reported', () => {
  const out = autoAssign(files('S01E02.mkv'), { episodes: one, rules, videoExts });
  const { warnings } = analyze(out, one, new Set(['1:2']));
  assert.equal(warnings.find((w) => w.code === 'episodes_in_library').params.episodes, 'S01E02');
});

test('more files than episodes', () => {
  const out = autoAssign(files('a.mkv', 'b.mkv', 'c.mkv'), { episodes: new Map([[1, eps(2)]]), rules, videoExts, start: { season: 1, episode: 1 } });
  const { warnings } = analyze(out, new Map([[1, eps(2)]]), new Set());
  assert.ok(warnings.some((w) => w.code === 'more_files_than_episodes_total'));
  assert.ok(warnings.some((w) => w.code === 'unassigned_files'));
});

test('assign swaps with the video already on that episode', () => {
  const out = autoAssign(files('S01E01.mkv', 'S01E02.mkv'), { episodes: one, rules, videoExts });
  assign(out, 'k0', 1, 2);
  assert.deepEqual(out.map(episodeLabel), ['S01E02', 'S01E01']);
  assign(out, 'k0', null, null);
  assert.deepEqual(out.map(episodeLabel), ['', 'S01E01']);
});

test('swapWith exchanges two assignments', () => {
  const out = autoAssign(files('S01E01.mkv', 'S01E03-E04.mkv'), { episodes: one, rules, videoExts });
  swapWith(out, 'k0', 'k1');
  assert.deepEqual(out.map(episodeLabel), ['S01E03-E04', 'S01E01']);
});

test('next free skips assigned, covered-by-range and library episodes', () => {
  const out = autoAssign(files('S00E01.mkv', 'S01E01-E02.mkv'), { episodes: two, rules, videoExts });
  assert.deepEqual(nextFree(out, two, new Set(['0:2', '1:3'])), { season: 1, episode: 4 });
});
