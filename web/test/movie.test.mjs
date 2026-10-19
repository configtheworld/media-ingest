import { test } from 'node:test';
import assert from 'node:assert/strict';
import { suggestMovieRoles, validateMovieMapping, subtitleInfo, extraTypeFor } from '../static/js/movie.js';
import { isJunk, extOf } from '../static/js/files.js';

const video = new Set(['mkv', 'mp4', 'avi']);
const subs = new Set(['srt', 'ass']);
const GB = 1024 ** 3;

test('single movie with subtitle and trailer', () => {
  const out = suggestMovieRoles([
    { key: 'a', name: 'The.Clockwork.Garden.2015.1080p.mkv', size: 8 * GB },
    { key: 'b', name: 'The.Clockwork.Garden.2015.en.forced.srt', size: 1000 },
    { key: 'c', name: 'Clockwork Garden Trailer.mp4', size: 50e6 },
    { key: 'd', name: 'notes.txt', size: 10 },
  ], video, subs);
  const by = Object.fromEntries(out.map((f) => [f.key, f]));
  assert.equal(by.a.role, 'movie');
  assert.equal(by.a.version, '');
  assert.equal(by.b.role, 'subtitle');
  assert.equal(by.b.lang, 'en');
  assert.equal(by.b.forced, true);
  assert.equal(by.c.role, 'extra');
  assert.equal(by.c.extraType, 'trailer');
  assert.equal(by.d.role, 'ignore');
  assert.deepEqual(validateMovieMapping(out), []);
});

test('two similar-size files become labelled versions', () => {
  const out = suggestMovieRoles([
    { key: 'a', name: 'Movie.2160p.mkv', size: 40 * GB },
    { key: 'b', name: 'Movie.1080p.mkv', size: 25 * GB },
  ], video, subs);
  assert.deepEqual(out.map((f) => [f.role, f.version]), [['movie', '2160p'], ['movie', '1080p']]);
  assert.deepEqual(validateMovieMapping(out), []);
});

test('versions without hints get distinct labels', () => {
  const out = suggestMovieRoles([
    { key: 'a', name: 'Movie A.mkv', size: 10 * GB },
    { key: 'b', name: 'Movie B.mkv', size: 9 * GB },
  ], video, subs);
  const labels = out.map((f) => f.version);
  assert.equal(new Set(labels).size, 2);
});

test('validation catches problems', () => {
  assert.deepEqual(validateMovieMapping([{ role: 'extra', version: '' }]), ['no_movie_file']);
  assert.ok(validateMovieMapping([
    { role: 'movie', version: 'x' }, { role: 'movie', version: 'x' },
  ]).includes('version_labels_required'));
  assert.ok(validateMovieMapping([
    { role: 'movie', version: '' }, { role: 'subtitle', version: 'nope', lang: 'en' },
  ]).includes('subtitle_unknown_version'));
  assert.ok(validateMovieMapping([
    { role: 'movie', version: '' }, { role: 'subtitle', version: '', lang: '../x' },
  ]).includes('invalid_subtitle_language'));
});

test('subtitle info parsing', () => {
  assert.deepEqual(subtitleInfo('Film.tr.srt'), { lang: 'tr', forced: false, sdh: false });
  assert.deepEqual(subtitleInfo('Film.2010.deu.sdh.srt'), { lang: 'deu', forced: false, sdh: true });
  assert.deepEqual(subtitleInfo('Film.srt'), { lang: '', forced: false, sdh: false });
});

test('extra type hints', () => {
  assert.equal(extraTypeFor('Making of.mkv'), 'behindthescenes');
  assert.equal(extraTypeFor('Deleted Scene 1.mkv'), 'deleted');
  assert.equal(extraTypeFor('random.mkv'), 'other');
});

test('junk filter', () => {
  for (const n of ['._Movie.mkv', '.DS_Store', 'Folder/._x.srt', 'Thumbs.db']) assert.ok(isJunk(n), n);
  for (const n of ['Movie.mkv', '_x.mkv']) assert.ok(!isJunk(n), n);
  assert.equal(extOf('A.MKV'), 'mkv');
  assert.equal(extOf('.hidden'), '');
});
