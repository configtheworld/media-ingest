// Renders the PWA PNG icons from the SVG sources: node scripts/render-icons.mjs
import { chromium } from '@playwright/test';
import fs from 'node:fs';

const dir = new URL('../web/static/icons/', import.meta.url);
const targets = [
  ['icon.svg', 'icon-192.png', 192],
  ['icon.svg', 'icon-512.png', 512],
  ['icon-maskable.svg', 'icon-maskable-512.png', 512],
  ['icon-maskable.svg', 'apple-touch-icon.png', 180],
];

const browser = await chromium.launch();
for (const [src, out, size] of targets) {
  const page = await browser.newPage({ viewport: { width: size, height: size } });
  const svg = fs.readFileSync(new URL(src, dir), 'utf8');
  await page.setContent(`<html><body style="margin:0;background:transparent">${svg.replace('<svg ', `<svg width="${size}" height="${size}" `)}</body></html>`);
  fs.writeFileSync(new URL(out, dir), await page.screenshot({ omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } }));
  await page.close();
  console.log(out, size);
}
await browser.close();
