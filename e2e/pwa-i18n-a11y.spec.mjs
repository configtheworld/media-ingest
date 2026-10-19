// End-to-end: installable PWA, translated UI, automated accessibility checks.
import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

test('PWA: manifest, icons, service worker and offline app shell', async ({ page, context, request }) => {
  const manifest = await (await request.get('/manifest.webmanifest')).json();
  expect(manifest.display).toBe('standalone');
  for (const icon of manifest.icons) {
    const res = await request.get(icon.src);
    expect(res.ok(), icon.src).toBe(true);
  }
  expect(manifest.icons.some((i) => i.purpose === 'maskable' && i.sizes === '512x512')).toBe(true);

  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'What are you adding?' })).toBeVisible();
  const scope = await page.evaluate(async () => (await navigator.serviceWorker.ready).scope);
  expect(scope).toBe('http://127.0.0.1:18097/');
  // Load once more so the service worker (now in control) caches the shell.
  await page.reload();
  await expect(page.getByRole('heading', { name: 'What are you adding?' })).toBeVisible();

  await context.setOffline(true);
  await page.reload();
  // The app itself renders and explains the problem instead of a browser error page.
  await expect(page.getByText('The server cannot be reached. Check your connection.')).toBeVisible();
  await context.setOffline(false);
});

test('UI language follows the browser and can be switched', async ({ browser }) => {
  const ctx = await browser.newContext({ locale: 'de-DE' });
  const page = await ctx.newPage();
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Was möchtest du hinzufügen?' })).toBeVisible();
  await expect(page.locator('html')).toHaveAttribute('lang', 'de');
  await expect(page.getByRole('button', { name: 'Verlauf' })).toBeVisible();

  await page.getByLabel('Sprache der Oberfläche').selectOption('tr');
  await expect(page.getByRole('heading', { name: 'Ne ekliyorsunuz?' })).toBeVisible();
  await page.reload(); // the choice is remembered
  await expect(page.getByRole('heading', { name: 'Ne ekliyorsunuz?' })).toBeVisible();
  await page.getByLabel('Arayüz dili').selectOption('en');
  await expect(page.getByRole('heading', { name: 'What are you adding?' })).toBeVisible();
  await ctx.close();
});

async function expectAccessible(page, name) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
  const serious = results.violations.filter((v) => ['serious', 'critical'].includes(v.impact));
  expect(serious.map((v) => `${name}: ${v.id} – ${v.help} (${v.nodes.length}×) ${v.nodes[0]?.target}`)).toEqual([]);
}

test('accessibility: no serious WCAG 2.1 AA violations on the main screens', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'What are you adding?' })).toBeVisible();
  await expectAccessible(page, 'search');

  await page.getByRole('radio', { name: 'TV show' }).check();
  await page.getByRole('searchbox').fill('Quillmoor');
  await page.getByRole('button', { name: 'Search' }).click();
  await page.getByRole('button', { name: /Quillmoor Bay \(2019\)/ }).click();
  await expect(page.getByRole('heading', { name: 'Which seasons?' })).toBeVisible();
  await expectAccessible(page, 'seasons');

  await page.getByRole('checkbox', { name: /^Season 1/ }).check();
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await page.locator('input[type=file]').first().setInputFiles([
    { name: 'Quillmoor.Bay.S01E01.mkv', mimeType: 'video/x-matroska', buffer: Buffer.alloc(10) },
    { name: 'Quillmoor.Bay.S01E02.en.srt', mimeType: 'text/plain', buffer: Buffer.from('x') },
  ]);
  await expectAccessible(page, 'files');
  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Match files to episodes' })).toBeVisible();
  await expectAccessible(page, 'map');

  await page.getByRole('button', { name: 'Next', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  await expect(page.getByText(/tv\/Quillmoor Bay/).first()).toBeVisible();
  await expectAccessible(page, 'review');

  await page.getByRole('button', { name: 'History' }).click();
  await expect(page.getByRole('heading', { name: 'History' })).toBeVisible();
  await expectAccessible(page, 'history');

  await page.getByRole('button', { name: 'Settings' }).click();
  await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();
  await expectAccessible(page, 'settings');

  await page.getByRole('button', { name: 'Quick upload' }).first().click();
  await expect(page.getByRole('heading', { name: 'Where should the files go?' })).toBeVisible();
  await expectAccessible(page, 'quick');
});

test('accessibility: dark mode colours also meet WCAG AA', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'What are you adding?' })).toBeVisible();
  await page.getByRole('searchbox').fill('The Clockwork Garden');
  await page.getByRole('button', { name: 'Search' }).click();
  await expect(page.getByRole('button', { name: /The Clockwork Garden \(2015\)/ })).toBeVisible();
  await expectAccessible(page, 'dark search');
  await page.getByRole('button', { name: 'Settings' }).click();
  await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();
  await expectAccessible(page, 'dark settings');
});
