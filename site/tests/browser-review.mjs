import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, webkit } from '../../frontend/node_modules/@playwright/test/index.mjs';
import { startServer } from './serve.mjs';

const output = fileURLToPath(new URL('../../build/site-review/', import.meta.url));
await mkdir(output, { recursive: true });
const local = await startServer();
const results = [];
try {
  for (const [name, engine] of [['chromium', chromium], ['webkit', webkit]]) {
    const browser = await engine.launch();
    const context = await browser.newContext();
    const page = await context.newPage();
    const errors = [], failedAssets = [], invalidRequests = [];
    page.on('pageerror', error => errors.push(error.message));
    page.on('response', response => { if (response.status() >= 400) failedAssets.push(response.url()); });
    page.on('request', request => {
      const url = new URL(request.url());
      if (!['127.0.0.1', 'fonts.googleapis.com', 'fonts.gstatic.com'].includes(url.hostname) || ['fetch', 'xhr', 'websocket'].includes(request.resourceType())) invalidRequests.push(request.url());
    });
    async function imagesReady() {
      await page.waitForFunction(() => [...document.images].filter(image => image.getClientRects().length).every(image => image.complete && image.naturalWidth > 0));
      await page.evaluate(() => document.fonts.ready);
    }
    async function inspect(label, width) {
      await imagesReady();
      const state = await page.evaluate(() => {
        const visible = node => node.getClientRects().length > 0;
        const ids = [...document.querySelectorAll('[id]')].map(node => node.id);
        const screenshots = [...document.querySelectorAll('.app-screenshot, .phone-screenshot')].filter(visible);
        const preview = document.querySelector('#preview');
        const previewStyle = getComputedStyle(preview);
        return {
          previewDecoration: ['borderTopWidth', 'borderRightWidth', 'borderBottomWidth', 'borderLeftWidth', 'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft'].map(property => previewStyle[property]),
          previewWrapperContent: preview.querySelectorAll('h2, .preview-heading, .preview-note').length,
          mode: document.documentElement.dataset.mode,
          overflow: document.documentElement.scrollWidth > innerWidth,
          duplicates: ids.filter((id, i) => ids.indexOf(id) !== i),
          phoneVisible: visible(document.querySelector('.phone-panel')),
          clipped: [...document.querySelectorAll('button, img, h1')].filter(visible).filter(node => {
            const box = node.getBoundingClientRect(); return box.left < -1 || box.right > innerWidth + 1;
          }).map(node => node.className),
          images: screenshots.map(node => ({ src: node.getAttribute('src'), alt: node.alt, ratio: node.clientWidth / node.clientHeight })),
          previewControls: document.querySelector('#preview').querySelectorAll('button, a, iframe, input, textarea, select').length,
        };
      });
      assert.equal(state.overflow, false, label + ': page overflow');
      assert.deepEqual(state.clipped, [], label + ': clipped elements');
      assert.deepEqual(state.duplicates, [], label + ': duplicate IDs');
      assert.equal(state.phoneVisible, width >= 768, label + ': phone layout');
      assert.equal(state.previewControls, 0, label + ': previews remain static');
      assert.equal(state.previewWrapperContent, 0, label + ': no screenshot wrapper heading or footer');
      assert.deepEqual(state.previewDecoration, Array(8).fill('0px'), label + ': no outer screenshot border or padding');
      for (const image of state.images) {
        assert.match(image.src, new RegExp(`-${state.mode || 'light'}\\.png$`));
        assert.ok(image.alt.length > 20);
        assert.ok(Math.abs(image.ratio - (image.src.includes('/app-') ? 960 / 640 : 390 / 700)) < .015, label + ': image proportions');
      }
      results.push({ engine: name, label, width, ...state });
    }
    try {
      for (const width of [1440, 1024, 800, 768, 767, 390, 320]) {
        for (const mode of ['light', 'dark']) {
          await page.setViewportSize({ width, height: width < 768 ? 1000 : 1080 });
          await page.emulateMedia({ colorScheme: mode });
          await page.goto(local.url);
          await inspect(`${width}-${mode}`, width);
          if ([1440, 390].includes(width) && name === 'chromium') await page.screenshot({ path: resolve(output, `${width}-${mode}.png`), fullPage: true });
        }
      }
      await page.setViewportSize({ width: 1024, height: 900 });
      await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' });
      await page.goto(local.url);
      await page.getByRole('button', { name: 'Use dark theme' }).focus();
      await page.keyboard.press('Enter');
      await page.waitForFunction(() => document.documentElement.dataset.mode === 'dark');
      await page.emulateMedia({ colorScheme: 'light' });
      await inspect('keyboard-theme-and-reduced-motion', 1024);
      assert.equal(await page.locator('html').getAttribute('data-mode'), 'dark', 'explicit choice survives system changes');
      assert.equal(await page.getByRole('button', { name: 'Use light theme' }).count(), 1);
      await page.getByText('What goes through the cloud?', { exact: true }).click();
      assert.match(await page.locator('details[open]').innerText(), /Internet access is needed/);
      assert.equal(page.frames().length, 1, 'no app is embedded');
      const noJS = await browser.newContext({ javaScriptEnabled: false, colorScheme: 'dark', viewport: { width: 390, height: 900 } });
      try {
        const staticPage = await noJS.newPage();
        await staticPage.goto(local.url);
        assert.equal(await staticPage.locator('#theme').isVisible(), false);
        assert.equal(await staticPage.getByRole('link', { name: 'Download', exact: true }).isVisible(), true);
        assert.equal(await staticPage.locator('.app-screenshot.screenshot-dark').isVisible(), true);
      } finally { await noJS.close(); }
      assert.deepEqual(errors, [], name + ': runtime errors');
      assert.deepEqual(failedAssets, [], name + ': failed assets');
      assert.deepEqual(invalidRequests, [], name + ': no app traffic or analytics');
    } catch (error) {
      await page.screenshot({ path: resolve(output, `${name}-failure.png`), fullPage: true });
      throw error;
    } finally { await context.close(); await browser.close(); }
  }
} finally { await local.close(); }
await writeFile(resolve(output, 'review.json'), JSON.stringify(results, null, 2));
console.log(`Passed ${results.length} Chromium/WebKit responsive reviews, keyboard themes, reduced motion, no-JavaScript, asset loading, and static-traffic checks.`);
