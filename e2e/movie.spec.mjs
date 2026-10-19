// End-to-end: the movie wizard in a mobile viewport against a fake TMDB.
import { test, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { MEDIA_ROOT } from '../playwright.config.mjs';

const FOLDER = 'Inception (2010) [tmdbid-27205]';
const MOVIE = `movies/${FOLDER}/${FOLDER}.mkv`;
const SUB = `movies/${FOLDER}/${FOLDER}.en.srt`;

// 20 MiB so the upload is split into several 8 MiB tus chunks.
const movieBytes = Buffer.alloc(20 * 1024 * 1024, 7);

async function searchAndPick(page) {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'What are you adding?' })).toBeVisible();
  await page.getByRole('searchbox').fill('Inception');
  await page.getByRole('button', { name: 'Search' }).click();
  const card = page.getByRole('button', { name: /Inception \(2010\)/ });
  await expect(card).toBeVisible();
  await expect(card.locator('img')).toHaveJSProperty('naturalWidth', 1); // poster served through the server-side proxy
  await card.click();
  await expect(page.getByRole('heading', { name: 'Add files' })).toBeVisible();
}

test('movie: search, add files, map, review, upload, publish', async ({ page }) => {
  const seen = [];
  page.on('request', (r) => seen.push(r.url()));

  await searchAndPick(page);

  await page.locator('input[type=file]').first().setInputFiles([
    { name: 'Inception.2010.1080p.BluRay.mkv', mimeType: 'video/x-matroska', buffer: movieBytes },
    { name: 'Inception.2010.en.srt', mimeType: 'text/plain', buffer: Buffer.from('1\n00:00:01,000 --> 00:00:02,000\nHi\n') },
    { name: '._Inception.2010.1080p.BluRay.mkv', mimeType: 'application/octet-stream', buffer: Buffer.from('junk') },
    { name: 'readme.txt', mimeType: 'text/plain', buffer: Buffer.from('nope') },
  ]);
  await expect(page.getByText('2 files ignored')).toBeVisible();
  await expect(page.locator('.filelist li')).toHaveCount(2);
  await page.getByRole('button', { name: 'Next' }).click();

  await expect(page.getByRole('heading', { name: 'Assign files' })).toBeVisible();
  const roles = page.getByLabel('Use as');
  await expect(roles.nth(0)).toHaveValue('movie');
  await expect(roles.nth(1)).toHaveValue('subtitle');
  await expect(page.getByLabel('Language', { exact: true })).toHaveValue('en');
  await page.getByRole('button', { name: 'Next' }).click();

  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  await expect(page.getByText(MOVIE, { exact: true })).toBeVisible();
  await expect(page.getByText(SUB, { exact: true })).toBeVisible();
  await page.screenshot({ path: 'e2e/.tmp/review-mobile.png', fullPage: true });
  // Nothing may exist on disk before confirming.
  expect(fs.existsSync(path.join(MEDIA_ROOT, 'movies'))).toBe(false);
  await page.getByRole('button', { name: 'Upload and publish' }).click();

  await expect(page.getByRole('heading', { name: 'Upload and publish' })).toBeVisible();
  await expect(page.getByText(/2 of 2 files published/)).toBeVisible({ timeout: 30_000 });
  await page.screenshot({ path: 'e2e/.tmp/done-mobile.png', fullPage: true });

  const movie = path.join(MEDIA_ROOT, MOVIE);
  expect(fs.statSync(movie).size).toBe(movieBytes.length);
  expect(fs.statSync(movie).mode & 0o777).toBe(0o644);
  expect(fs.statSync(path.dirname(movie)).mode & 0o777).toBe(0o755);
  expect(fs.readFileSync(path.join(MEDIA_ROOT, SUB), 'utf8')).toContain('Hi');
  expect(fs.readdirSync(path.join(MEDIA_ROOT, '.incoming'))).toEqual([]);
  expect(fs.existsSync(path.join(MEDIA_ROOT, 'movies', FOLDER, '._Inception.2010.1080p.BluRay.mkv'))).toBe(false);

  // The browser never talks to TMDB and never sees the API key.
  expect(seen.filter((u) => !u.startsWith('http://127.0.0.1:18097/'))).toEqual([]);
  expect(seen.some((u) => u.includes('fake-api-key'))).toBe(false);
  const multipleChunks = seen.filter((u) => u.includes('/api/uploads/')).length;
  expect(multipleChunks).toBeGreaterThanOrEqual(3);
});

test('movie: existing file requires an explicit decision, keep both', async ({ page }) => {
  await searchAndPick(page);
  await page.locator('input[type=file]').first().setInputFiles([
    { name: 'Inception.remux.mkv', mimeType: 'video/x-matroska', buffer: Buffer.from('second copy') },
  ]);
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();

  await expect(page.getByText('1 destination(s) already exist. Choose what to do for each.')).toBeVisible();
  const confirm = page.getByRole('button', { name: 'Upload and publish' });
  await expect(confirm).toBeDisabled();
  await page.getByLabel('Already exists. What should happen?').selectOption('keep_both');
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect(page.getByText(/1 of 1 files published/)).toBeVisible({ timeout: 30_000 });

  expect(fs.statSync(path.join(MEDIA_ROOT, MOVIE)).size).toBe(movieBytes.length); // original untouched
  expect(fs.readFileSync(path.join(MEDIA_ROOT, `movies/${FOLDER}/${FOLDER} - Version 2.mkv`), 'utf8')).toBe('second copy');
});

test('movie: upload survives a page reload and resumes from the server offset', async ({ page }) => {
  const dir = path.resolve('e2e/.tmp/resume');
  fs.mkdirSync(dir, { recursive: true });
  const file = path.join(dir, 'The.Matrix.1999.mkv');
  fs.writeFileSync(file, Buffer.alloc(20 * 1024 * 1024, 3)); // 3 chunks of up to 8 MiB

  // Let the first chunk through, then simulate a dead connection.
  let patches = 0;
  await page.route('**/api/uploads/*', (route) => {
    if (route.request().method() !== 'PATCH') return route.continue();
    patches++;
    return patches === 1 ? route.continue() : route.abort('connectionreset');
  });

  await page.goto('/');
  await page.getByRole('searchbox').fill('Matrix');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByRole('button', { name: /The Matrix \(1999\)/ }).click();
  await page.locator('input[type=file]').first().setInputFiles(file);
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Upload and publish' }).click();
  await expect(page.getByText('Connection lost, retrying…')).toBeVisible({ timeout: 15_000 });

  // Reload mid-upload: the File object is gone, the batch is not.
  await page.unroute('**/api/uploads/*');
  page.on('dialog', (d) => d.accept()); // beforeunload prompt while uploading
  await page.reload();
  await expect(page.getByText(/Select the same files again to continue 1 upload/)).toBeVisible();

  const after = [];
  page.on('request', (r) => { if (r.url().includes('/api/uploads/')) after.push(r.method()); });
  await page.locator('input[type=file]').first().setInputFiles(file);
  await expect(page.getByText(/1 of 1 files published/)).toBeVisible({ timeout: 30_000 });

  // Resumed with HEAD + the two remaining chunks, not restarted from zero.
  expect(after.filter((m) => m === 'HEAD').length).toBeGreaterThanOrEqual(1);
  expect(after.filter((m) => m === 'PATCH').length).toBe(2);
  expect(after.includes('POST')).toBe(false);
  const out = path.join(MEDIA_ROOT, 'movies/The Matrix (1999) [tmdbid-603]/The Matrix (1999) [tmdbid-603].mkv');
  expect(fs.statSync(out).size).toBe(20 * 1024 * 1024);
  expect(fs.readFileSync(out).equals(fs.readFileSync(file))).toBe(true);
});
