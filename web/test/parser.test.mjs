import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { compileRules, parseName, dirsOf } from '../static/js/parser.js';

// The exact rule file the server embeds and serves.
const rules = compileRules(JSON.parse(fs.readFileSync(new URL('../../internal/parser/default.json', import.meta.url))));

const cases = [
  // [path, season, episode, episodeEnd]
  ['Show.S01E03.1080p.WEB-DL.x264.mkv', 1, 3, null],
  ['show.s1e3.mkv', 1, 3, null],
  ['Show - S02E10 - Title.mkv', 2, 10, null],
  ['Show.S01.E03.mkv', 1, 3, null],
  ['Show S01E01E02.mkv', 1, 1, 2],
  ['Show S01E01-E02.mkv', 1, 1, 2],
  ['Show S01E01-02.mkv', 1, 1, 2],
  ['Show S01E01-1080p.mkv', 1, 1, null],
  ['Show.S01E01.720p.HDTV.mkv', 1, 1, null],
  ['Show.S03E07.2160p.DV.HDR10.DDP5.1.Atmos.mkv', 3, 7, null],
  ['Show.2019.S01E05.mkv', 1, 5, null],
  ['Show 1x03.mkv', 1, 3, null],
  ['Show 1x03-04.mkv', 1, 3, 4],
  ['Show 1x03-1x04.mkv', 1, 3, 4],
  ['Show 10x101.avi', 10, 101, null],
  ['Show 1920x1080 Episode 5.mkv', null, 5, null],
  ['Season 1 Episode 3.mkv', 1, 3, null],
  ['Show.Season.2.Episode.11.mkv', 2, 11, null],
  ['Show Season 1 - Episode 4.mp4', 1, 4, null],
  ['Episode 7.mkv', null, 7, null],
  ['Show Ep.07.mkv', null, 7, null],
  ['Show E07 1080p.mkv', null, 7, null],
  ['Show Part 2.mkv', null, 2, null],
  ['[Group] Show - 03 [1080p][ABCD1234].mkv', null, 3, null],
  ['[Group] Show - 12v2 (BD).mkv', null, 12, null],
  ['03 - Pilot.mkv', null, 3, null],
  ['03.mkv', null, 3, null],
  ['Show 05.mkv', null, 5, null],
  ['Show Season 2 - 05.mkv', 2, 5, null],
  // season from folders
  ['Show/Season 2/03 - Title.mkv', 2, 3, null],
  ['Show/Season.02/Episode 4.mkv', 2, 4, null],
  ['Show/S03/E05.mkv', 3, 5, null],
  ['Show/Specials/E01.mkv', 0, 1, null],
  ['Show/Series 4/Disc 1/E02.mkv', 4, 2, null],
  ['Show/Season 1/Show.S02E01.mkv', 2, 1, null], // the name wins over the folder
];

for (const [path, season, episode, episodeEnd] of cases) {
  test(`parse ${path}`, () => {
    const name = path.split('/').pop();
    const got = parseName(name, dirsOf(path), rules);
    assert.ok(got, 'no match');
    assert.deepEqual([got.season, got.episode, got.episodeEnd], [season, episode, episodeEnd]);
  });
}

const noMatch = ['Movie (2010).mkv', 'Documentary.1080p.x264.mkv', 'notes.mkv', '2019.mkv', 'Show.H.264.AAC2.0.mkv'];
for (const name of noMatch) {
  test(`no episode in ${name}`, () => {
    assert.equal(parseName(name, [], rules), null);
  });
}

test('episode 0 and absurd ranges are ignored', () => {
  assert.equal(parseName('Show E00.mkv', [], rules), null);
  assert.equal(parseName('Show S01E05E99.mkv', [], rules).episodeEnd, null);
});

// ---------- localized rule packs (composed by the Go server itself) ----------
import { execFileSync } from 'node:child_process';

const repo = new URL('../../', import.meta.url).pathname;
const composed = compileRules(JSON.parse(execFileSync('go', ['run', './cmd/media-ingest', 'rules'], { cwd: repo, encoding: 'utf8' })));

const localized = [
  // Turkish
  ['Dizi 1. Sezon 3. Bölüm.mkv', 1, 3],
  ['Dizi.2.Sezon.10.Bolum.720p.mkv', 2, 10],
  ['Dizi Sezon 1 Bölüm 4 Türkçe Dublaj.mp4', 1, 4],
  ['Dizi 5.Bölüm.mkv', null, 5],
  ['DİZİ BÖLÜM 7 FULL HD İZLE.mkv', null, 7],
  ['Dizi/3. Sezon/12. Bölüm.mkv', 3, 12],
  ['Dizi/Sezon 2/Bölüm 1.mkv', 2, 1],
  ['Dizi.S01E05.Turkce.Altyazili.mkv', 1, 5],
  ['Dizi/Özel Bölümler/Bölüm 2.mkv', 0, 2],
  // German
  ['Serie Staffel 2 Folge 5.mkv', 2, 5],
  ['Serie.S02E05.German.DL.1080p.mkv', 2, 5],
  ['Serie - 1. Staffel 3. Folge.mkv', 1, 3],
  ['Serie Folge 12.mkv', null, 12],
  ['Serie/Staffel 3/Folge 4.mkv', 3, 4],
  ['Serie Teil 2.mkv', null, 2],
  ['Serie/2. Staffel/07 - Titel.mkv', 2, 7],
  ['Serie/Specials/Folge 1.mkv', 0, 1],
  // Spanish
  ['Serie Temporada 1 Capitulo 3.mkv', 1, 3],
  ['Serie [Cap.103][Castellano].mkv', 1, 3],
  ['Serie Cap.1012 Latino.mkv', 10, 12],
  ['Serie T02E07.mkv', 2, 7],
  ['Serie Capítulo 8.mkv', null, 8],
  ['Serie Episodio 9 VOSE.mkv', null, 9],
  ['Serie/Temporada 2/Cap 05.mkv', 2, 5],
  ['Serie 1x03 Español.mkv', 1, 3],
  ['Serie/Especiales/Episodio 1.mkv', 0, 1],
];

for (const [path, season, episode] of localized) {
  test(`localized: ${path}`, () => {
    const got = parseName(path.split('/').pop(), dirsOf(path), composed);
    assert.ok(got, 'no match');
    assert.deepEqual([got.season, got.episode], [season, episode]);
  });
}

// Enabling every pack must not change any default result.
for (const [path, season, episode, episodeEnd] of cases) {
  test(`all packs keep: ${path}`, () => {
    const got = parseName(path.split('/').pop(), dirsOf(path), composed);
    assert.ok(got, 'no match');
    assert.deepEqual([got.season, got.episode, got.episodeEnd], [season, episode, episodeEnd]);
  });
}
for (const name of noMatch) {
  test(`all packs: no episode in ${name}`, () => assert.equal(parseName(name, [], composed), null));
}
