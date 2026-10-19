// End-to-end: quick upload, history + undo, Jellyfin hook, rule packs.
import { test, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { MEDIA_ROOT } from '../playwright.config.mjs';

test.describe.configure({ mode: 'serial' });

const jellyfinCalls = async (request) => (await request.get('http://127.0.0.1:18080/jellyfin/_calls')).json();

test('quick upload into a new sub-folder, Jellyfin notified, then undo from history', async ({ page, request }) => {
  fs.mkdirSync(path.join(MEDIA_ROOT, 'home videos', '2023'), { recursive: true });
  await page.goto('/');
  await page.getByRole('button', { name: 'Quick upload' }).first().click();
  await expect(page.getByRole('heading', { name: 'Where should the files go?' })).toBeVisible();
  await expect(page.getByRole('button', { name: /\.incoming|\.trash/ })).toHaveCount(0);
  await page.getByRole('button', { name: '📁 home videos' }).tap();
  await expect(page.getByRole('button', { name: '📁 2023' })).toBeVisible();
  await page.getByLabel('New sub-folder (optional)').fill('Trip: Lisbon');
  await expect(page.getByText('home videos/Trip: Lisbon')).toBeVisible();
  await page.screenshot({ path: 'e2e/.tmp/quick-folder-mobile.png', fullPage: true });
  await page.getByRole('button', { name: 'Next', exact: true }).click();

  await page.locator('input[type=file]').first().setInputFiles([
    { name: 'IMG_0001.mp4', mimeType: 'video/mp4', buffer: Buffer.alloc(4000, 7) },
    { name: 'Clip|2.mkv', mimeType: 'video/x-matroska', buffer: Buffer.alloc(1000, 8) },
  ]);
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  await expect(page.getByText('home videos/Trip - Lisbon/IMG_0001.mp4', { exact: true })).toBeVisible();
  await expect(page.getByText('home videos/Trip - Lisbon/Clip-2.mkv', { exact: true })).toBeVisible();
  await expect(page.getByText('1 file name(s) contained characters')).toBeVisible();
  await page.getByRole('button', { name: 'Upload and publish' }).click();
  await expect(page.getByText(/2 of 2 files published/)).toBeVisible({ timeout: 30_000 });
  await expect(page.getByText('Jellyfin notified')).toBeVisible({ timeout: 10_000 });

  const dir = path.join(MEDIA_ROOT, 'home videos', 'Trip - Lisbon');
  expect(fs.statSync(path.join(dir, 'IMG_0001.mp4')).size).toBe(4000);
  const calls = await jellyfinCalls(request);
  const last = calls[calls.length - 1];
  expect(last.path).toBe('/Library/Media/Updated');
  expect(last.auth).toBe('MediaBrowser Token="jf-test-key"');
  expect(last.body.Updates).toContainEqual({ Path: '/jf/media/home videos/Trip - Lisbon/IMG_0001.mp4', UpdateType: 'Created' });

  // History + undo.
  await page.getByRole('button', { name: 'History' }).click();
  await expect(page.getByRole('heading', { name: 'History' })).toBeVisible();
  const entry = page.locator('.hentry', { hasText: 'home videos/Trip - Lisbon' });
  await expect(entry.getByText('2 published')).toBeVisible();
  await entry.getByRole('button', { name: 'Details' }).click();
  await expect(entry.getByText('home videos/Trip - Lisbon/Clip-2.mkv')).toBeVisible();
  page.once('dialog', (d) => d.accept());
  await entry.getByRole('button', { name: 'Undo' }).click();
  await expect(page.getByText('2 file(s) moved to the trash.')).toBeVisible();
  await expect(page.locator('.hentry', { hasText: 'home videos/Trip - Lisbon' }).getByText('undone', { exact: true })).toBeVisible();
  await page.screenshot({ path: 'e2e/.tmp/history-mobile.png', fullPage: true });
  expect(fs.existsSync(dir)).toBe(false); // emptied folder removed
  expect(fs.existsSync(path.join(MEDIA_ROOT, 'home videos', '2023'))).toBe(true);
  const trash = fs.readdirSync(path.join(MEDIA_ROOT, '.trash'));
  const undoDir = trash.find((d) => d.endsWith('-undo'));
  expect(fs.statSync(path.join(MEDIA_ROOT, '.trash', undoDir, 'home videos', 'Trip - Lisbon', 'IMG_0001.mp4')).size).toBe(4000);
  await expect.poll(async () => (await jellyfinCalls(request)).at(-1).body.Updates[0].UpdateType).toBe('Deleted');
});

test('German file names are mapped by the bundled rule pack', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'New', exact: true }).click();
  await page.getByRole('radio', { name: 'TV show' }).check();
  await page.getByRole('searchbox').fill('Quillmoor');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByRole('button', { name: /Quillmoor Bay \(2019\)/ }).click();
  await page.getByRole('checkbox', { name: /^Season 1/ }).check();
  await page.getByRole('checkbox', { name: /^Season 2/ }).check();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.locator('input[type=file]').first().setInputFiles([
    { name: 'Quillmoor Bay Staffel 2 Folge 3 German DL.mkv', mimeType: 'video/x-matroska', buffer: Buffer.alloc(10) },
    { name: 'Quillmoor Bay 1. Sezon 5. Bölüm.mkv', mimeType: 'video/x-matroska', buffer: Buffer.alloc(10) },
    { name: 'Quillmoor Bay Temporada 1 Capitulo 6.mkv', mimeType: 'video/x-matroska', buffer: Buffer.alloc(10) },
  ]);
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  const badge = (n) => page.locator('.frow', { hasText: n }).locator('.badge').first();
  await expect(badge('Staffel 2 Folge 3')).toHaveText('S02E03');
  await expect(badge('Sezon 5')).toHaveText('S01E05');
  await expect(badge('Capitulo 6')).toHaveText('S01E06');
});

test('settings: rule packs can be switched off', async ({ page, request }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Settings' }).click();
  await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();
  await expect(page.getByText('Jellyfin is notified after each batch')).toBeVisible();
  await page.screenshot({ path: 'e2e/.tmp/settings-mobile.png', fullPage: true });
  await page.getByRole('checkbox', { name: /Deutsch/ }).uncheck();
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Saved.')).toBeVisible();
  const rules = await (await request.get('/api/parser/rules')).json();
  expect(rules.episode.some((r) => r.name === 'Staffel 1 Folge 3')).toBe(false);
  expect(rules.episode.some((r) => r.name === 'Bölüm 3')).toBe(true);
  // Restore for other tests.
  await page.getByRole('checkbox', { name: /Deutsch/ }).check();
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Saved.')).toBeVisible();
});
