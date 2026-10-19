// End-to-end: the TV wizard (phone: tap to assign; desktop: drag and drop).
import { test, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { MEDIA_ROOT } from '../playwright.config.mjs';

const SHOW = 'tv/Harbor Lights (2019) [tmdbid-900100]';
const bytes = (n, fill = 1) => Buffer.alloc(n, fill);
const mkv = (name, size = 1024, fill = 1) => ({ name, mimeType: 'video/x-matroska', buffer: bytes(size, fill) });

test.describe.configure({ mode: 'serial' });

async function openShow(page, seasons) {
  await page.goto('/');
  await page.getByRole('radio', { name: 'TV show' }).check();
  await page.getByRole('searchbox').fill('Harbor');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByRole('button', { name: /Harbor Lights \(2019\)/ }).click();
  await expect(page.getByRole('heading', { name: 'Which seasons?' })).toBeVisible();
  for (const s of seasons) await page.getByRole('checkbox', { name: new RegExp(`^${s}`) }).check();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Add files' })).toBeVisible();
}

const row = (page, name) => page.locator('.frow', { hasText: name });

test('tv: seasons, auto-mapping, tap to assign a special, upload', async ({ page }) => {
  await openShow(page, ['Specials', 'Season 1']);
  await page.locator('input[type=file]').first().setInputFiles([
    mkv('Harbor.Lights.S01E01.1080p.WEB-DL.mkv', 300_000, 1),
    mkv('Harbor.Lights.S01E02.mkv', 2048, 2),
    mkv('Harbor.Lights.S01E03E04.mkv', 2048, 3),
    { name: 'Harbor.Lights.S01E01.1080p.WEB-DL.en.srt', mimeType: 'text/plain', buffer: Buffer.from('1\n00:00:01,000 --> 00:00:02,000\nHi\n') },
    mkv('behind the scenes special.mkv', 1024, 4),
    { name: '._Harbor.Lights.S01E02.mkv', mimeType: 'application/octet-stream', buffer: Buffer.from('junk') },
  ]);
  await expect(page.getByText('1 files ignored')).toBeVisible();
  await page.getByRole('button', { name: 'Next', exact: true }).click();

  await expect(page.getByRole('heading', { name: 'Match files to episodes' })).toBeVisible();
  await expect(row(page, 'S01E01.1080p.WEB-DL.mkv').locator('.badge').first()).toHaveText('S01E01');
  await expect(row(page, 'S01E02.mkv').locator('.badge').first()).toHaveText('S01E02');
  await expect(row(page, 'S01E03E04.mkv').locator('.badge').first()).toHaveText('S01E03-E04');
  await expect(row(page, '.en.srt').locator('.badge').first()).toHaveText('S01E01');
  await expect(row(page, 'special.mkv').locator('.badge').first()).toHaveText('— not assigned —');
  await expect(page.getByText('1 file(s) cover several episodes')).toBeVisible();

  // Touch: tap "Pick" on the file, then tap the episode.
  await row(page, 'special.mkv').getByRole('button', { name: 'Pick' }).tap();
  await expect(page.getByText('Now tap the episode for behind the scenes special.mkv.')).toBeVisible();
  await page.getByRole('button', { name: 'S00E01 Behind the Harbor' }).tap();
  await expect(row(page, 'special.mkv').locator('.badge').first()).toHaveText('S00E01');
  await expect(page.locator('.tapbanner')).toBeHidden();
  await page.screenshot({ path: 'e2e/.tmp/tv-map-mobile.png', fullPage: true });

  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  const want = [
    `${SHOW}/Season 00/Harbor Lights - S00E01 - Behind the Harbor.mkv`,
    `${SHOW}/Season 01/Harbor Lights - S01E01 - Arrival.mkv`,
    `${SHOW}/Season 01/Harbor Lights - S01E01 - Arrival.en.srt`,
    `${SHOW}/Season 01/Harbor Lights - S01E02 - Fog.mkv`,
    `${SHOW}/Season 01/Harbor Lights - S01E03-E04 - Undertow & Low Tide.mkv`,
  ];
  for (const p of want) await expect(page.getByText(p, { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Upload and publish' }).click();
  await expect(page.getByText(/5 of 5 files published/)).toBeVisible({ timeout: 30_000 });

  for (const p of want) expect(fs.existsSync(path.join(MEDIA_ROOT, p)), p).toBe(true);
  expect(fs.statSync(path.join(MEDIA_ROOT, want[1])).size).toBe(300_000);
  expect(fs.readdirSync(path.join(MEDIA_ROOT, '.incoming'))).toEqual([]);
  await page.getByRole('button', { name: 'Add something else' }).click();
});

test('tv: library awareness, alternate versions, keep both', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('radio', { name: 'TV show' }).check();
  await page.getByRole('searchbox').fill('Harbor');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByRole('button', { name: /Harbor Lights \(2019\)/ }).click();
  // The season list knows what the library already has.
  await expect(page.getByText('5 episode files already in the library')).toBeVisible();
  await expect(page.getByRole('checkbox', { name: /^Season 1 · 6 episodes · 2019 · 4 in library/ })).toBeVisible();
  await page.getByRole('checkbox', { name: /^Season 1/ }).check();
  await page.getByRole('button', { name: 'Next', exact: true }).click();

  await page.locator('input[type=file]').first().setInputFiles([
    mkv('Harbor.Lights.S01E02.2160p.mkv', 2048, 5),
    mkv('Harbor.Lights.S01E02.REMUX.2160p.mkv', 4096, 6),
  ]);
  await page.getByRole('button', { name: 'Next', exact: true }).click();

  // Two files on one episode: both suggested "2160p", so labels must differ.
  await expect(page.getByText('Two files share an episode.')).toBeVisible();
  const next = page.getByRole('button', { name: 'Next', exact: true });
  await expect(next).toBeDisabled();
  await expect(page.getByRole('button', { name: /^S01E02 Fog/ }).getByText('in library')).toBeVisible();
  await row(page, 'REMUX').getByLabel('Alternate version label').fill('Remux');
  await expect(next).toBeEnabled();
  await next.click();

  await expect(page.getByText('2 destination(s) already exist')).toBeVisible();
  await expect(page.getByText(`${SHOW}/Season 01/Harbor Lights - S01E02 - Fog.mkv`).first()).toBeVisible(); // listed as existing
  const confirm = page.getByRole('button', { name: 'Upload and publish' });
  await expect(confirm).toBeDisabled();
  await page.getByRole('button', { name: 'Keep both' }).click(); // apply to all conflicts
  await expect(confirm).toBeEnabled();
  await confirm.click();
  await expect(page.getByText(/2 of 2 files published/)).toBeVisible({ timeout: 30_000 });

  const dir = path.join(MEDIA_ROOT, SHOW, 'Season 01');
  expect(fs.statSync(path.join(dir, 'Harbor Lights - S01E02 - Fog - 2160p.mkv')).size).toBe(2048);
  expect(fs.statSync(path.join(dir, 'Harbor Lights - S01E02 - Fog - Remux.mkv')).size).toBe(4096);
  expect(fs.readFileSync(path.join(dir, 'Harbor Lights - S01E02 - Fog.mkv'))[0]).toBe(2); // original untouched
});

test('tv: desktop drag and drop, swap, sequential fallback @desktop', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('radio', { name: 'TV show' }).check();
  await page.getByRole('searchbox').fill('Harbor');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByRole('button', { name: /Harbor Lights \(2019\)/ }).click();
  await page.getByRole('checkbox', { name: /^Season 2/ }).check();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  // No episode numbers in these names: natural order from S02E01.
  await page.locator('input[type=file]').first().setInputFiles([mkv('clip b.mkv'), mkv('clip a.mkv'), mkv('clip c.mkv')]);
  await page.getByRole('button', { name: 'Next', exact: true }).click();

  await expect(row(page, 'clip a.mkv').locator('.badge').first()).toHaveText('S02E01');
  await expect(row(page, 'clip b.mkv').locator('.badge').first()).toHaveText('S02E02');
  await expect(row(page, 'clip c.mkv').locator('.badge').first()).toHaveText('S02E03');

  // Drag onto a free episode.
  await row(page, 'clip c.mkv').dragTo(page.getByRole('button', { name: /^S02E04 Dawn/ }), { sourcePosition: { x: 12, y: 12 } });
  await expect(row(page, 'clip c.mkv').locator('.badge').first()).toHaveText('S02E04');
  // Drag onto an occupied episode: the two files swap.
  await row(page, 'clip c.mkv').dragTo(page.getByRole('button', { name: /^S02E01 Return/ }), { sourcePosition: { x: 12, y: 12 } });
  await expect(row(page, 'clip c.mkv').locator('.badge').first()).toHaveText('S02E01');
  await expect(row(page, 'clip a.mkv').locator('.badge').first()).toHaveText('S02E04');
  // Keyboard/dropdown path and "skip".
  await row(page, 'clip b.mkv').getByLabel('Episode').selectOption({ label: 'S02E03 · Storm' });
  await expect(row(page, 'clip b.mkv').locator('.badge').first()).toHaveText('S02E03');
  await row(page, 'clip b.mkv').getByRole('button', { name: 'Skip' }).click();
  await expect(row(page, 'clip b.mkv').locator('.badge').first()).toHaveText('— not assigned —');
  await expect(page.getByText('1 file(s) not assigned; they will be skipped.')).toBeVisible();
  await row(page, 'clip b.mkv').getByRole('button', { name: 'Next free episode' }).click();
  await expect(row(page, 'clip b.mkv').locator('.badge').first()).toHaveText('S02E02');
  await page.screenshot({ path: 'e2e/.tmp/tv-map-desktop.png', fullPage: true });
});
