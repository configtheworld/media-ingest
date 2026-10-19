// Renders the README screenshots (fake data): npm run screenshots
import { test, expect, devices } from '@playwright/test';

const out = (name) => `docs/screenshots/${name}.png`;
const mkv = (name) => ({ name, mimeType: 'video/x-matroska', buffer: Buffer.alloc(2048, 1) });

test('screenshots @docs', async ({ browser }) => {
  const desktop = await browser.newContext({ ...devices['Desktop Chrome'], viewport: { width: 1280, height: 860 } });
  let page = await desktop.newPage();
  await page.goto('/');
  await page.getByRole('radio', { name: 'TV show' }).check();
  await page.getByRole('searchbox').fill('Harbor');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByRole('button', { name: /Harbor Lights \(2019\)/ }).click();
  await page.getByRole('checkbox', { name: /^Specials/ }).check();
  await page.getByRole('checkbox', { name: /^Season 1/ }).check();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.locator('input[type=file]').first().setInputFiles([
    mkv('Harbor.Lights.S01E01.1080p.WEB-DL.mkv'), mkv('Harbor.Lights.S01E02.mkv'), mkv('Harbor.Lights.S01E03E04.mkv'),
    { name: 'Harbor.Lights.S01E01.1080p.WEB-DL.en.srt', mimeType: 'text/plain', buffer: Buffer.from('x') },
    mkv('behind the scenes.mkv'),
  ]);
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Match files to episodes' })).toBeVisible();
  await page.screenshot({ path: out('tv-mapping-desktop'), fullPage: true });
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByText(/Season 01\/Harbor Lights - S01E01 - Arrival\.mkv/).first()).toBeVisible();
  await page.screenshot({ path: out('review-desktop'), fullPage: true });
  await desktop.close();

  const phone = await browser.newContext({ ...devices['Pixel 7'] });
  page = await phone.newPage();
  await page.goto('/');
  await page.getByRole('searchbox').fill('The Clockwork Garden');
  await page.getByRole('button', { name: 'Search' }).click();
  await expect(page.getByRole('button', { name: /The Clockwork Garden \(2015\)/ })).toBeVisible();
  await page.screenshot({ path: out('search-mobile') });
  await page.getByRole('button', { name: /The Clockwork Garden \(2015\)/ }).click();
  await page.locator('input[type=file]').first().setInputFiles([mkv('The.Clockwork.Garden.2015.2160p.mkv')]);
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.getByRole('button', { name: 'Upload and publish' }).click();
  await expect(page.getByText(/1 of 1 files published/)).toBeVisible({ timeout: 30_000 });
  await page.screenshot({ path: out('published-mobile') });
  await page.getByRole('button', { name: 'History' }).click();
  await expect(page.getByRole('heading', { name: 'History' })).toBeVisible();
  await page.screenshot({ path: out('history-mobile') });
  await phone.close();

  const dark = await browser.newContext({ ...devices['Pixel 7'], colorScheme: 'dark', locale: 'de-DE' });
  page = await dark.newPage();
  await page.goto('/');
  await page.getByRole('radio', { name: 'Serie' }).check();
  await page.getByRole('searchbox').fill('Harbor');
  await page.getByRole('button', { name: 'Suchen' }).click();
  await page.getByRole('button', { name: /Harbor Lights \(2019\)/ }).click();
  await expect(page.getByRole('heading', { name: 'Welche Staffeln?' })).toBeVisible();
  await page.screenshot({ path: out('seasons-dark-de') });
  await dark.close();
});
