import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve, basename } from 'node:path';
import { chromium, expect } from '../../frontend/node_modules/@playwright/test/index.mjs';
import { serveDesktop } from '../../frontend/tests/desktop-fixture.js';

const root = fileURLToPath(new URL('../../', import.meta.url));
const asset = name => resolve(root, 'site', 'assets', name);
process.chdir(resolve(root, 'frontend'));
const qr = 'data:image/png;base64,' + (await readFile(asset('example-qr.png'))).toString('base64');
const state = {
  secure: true, serviceConnected: true, networkIP: '192.168.1.2', qr,
  status: { running: true, address: 'https://example.invalid/', inboxDir: 'C:\\Users\\Example\\Downloads\\Share Me' },
  networks: [{ name: 'Wi-Fi', ip: '192.168.1.2' }],
  items: [
    { id: 'example-photo', kind: 'file', name: 'Photo.jpg', size: 3145728, mime: 'image/jpeg', createdAt: '2026-09-01T10:42:00Z' },
    { id: 'example-file', kind: 'file', name: 'Notes.txt', size: 2048, mime: 'text/plain', createdAt: '2026-09-01T10:41:00Z' },
    { id: 'example-text', kind: 'text', name: 'Text', text: 'Pick up the prints on Friday.', size: 28, createdAt: '2026-09-01T10:40:00Z' },
  ],
  pending: [], pairRequests: [], outbox: [],
  devices: [{ id: 'example-phone', name: 'iPhone', connected: true }],
  settings: { startWithWindows: false, startMinimized: false, minimizeToTray: true },
  trayAvailable: true, shortcutEnabled: false, shortcutStatus: {},
};
const browser = await chromium.launch();
const images = {};
try {
  for (const mode of ['light', 'dark']) {
    const context = await browser.newContext({ colorScheme: mode, locale: 'en-US', timezoneId: 'UTC' });
    try {
      const desktop = await context.newPage();
      await desktop.setViewportSize({ width: 960, height: 640 });
      await desktop.exposeFunction('desktopState', () => state);
      await desktop.addInitScript(() => {
        window.go = { main: { App: { GetState: () => window.desktopState() } } };
      });
      await desktop.route('https://desktop-preview.test/theme-init.js', route => route.fulfill({ path: resolve('dist', 'theme-init.js') }));
      await serveDesktop(desktop, `https://desktop-preview.test/?scoutTheme=${mode}`);
      await expect(desktop.getByRole('status')).toHaveText('Ready');
      await expect(desktop.locator('.transfer-row')).toHaveCount(3);
      await expect(desktop.locator('html')).toHaveAttribute('data-theme', mode);
      await desktop.locator('body').click({ position: { x: 250, y: 10 } });
      await desktop.screenshot({ path: asset(`app-${mode}.png`), animations: 'disabled' });
      const phone = await context.newPage();
      await phone.setViewportSize({ width: 390, height: 700 });
      await phone.addInitScript(() => {
        window.shareMePeer = {
          activeSaves: () => false,
          fetch: async path => {
            let reply;
            if (path === '/api/session') reply = { maxFileBytes: 2 ** 31 };
            else if (path === '/api/outbox') reply = { items: [] };
            else if (path === '/api/request') reply = { id: 'a'.repeat(32), token: 'b'.repeat(43), status: 'pending' };
            else if (path === '/api/request/' + 'a'.repeat(32)) reply = { status: 'pending' };
            else throw new Error('Unexpected screenshot fixture request: ' + path);
            return new Response(JSON.stringify(reply));
          },
        };
      });
      await phone.route('https://phone-preview.test/assets/**', route => route.fulfill({ path: resolve('dist', 'assets', basename(new URL(route.request().url()).pathname)) }));
      await phone.route('https://phone-preview.test/theme-init.js', route => route.fulfill({ path: resolve('dist', 'theme-init.js') }));
      await phone.route('https://phone-preview.test/?*', route => route.fulfill({ contentType: 'text/html', path: resolve('dist', 'phone.html') }));
      await phone.goto(`https://phone-preview.test/?scoutTheme=${mode}`);
      await expect(phone.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible();
      await phone.getByRole('textbox', { name: 'Text or a link' }).fill('Pick up the prints on Friday.');
      await phone.getByRole('button', { name: 'Send', exact: true }).click();
      await expect(phone.getByRole('status')).toHaveText('Waiting for PC approval');
      await expect(phone.locator('html')).toHaveAttribute('data-theme', mode);
      await phone.screenshot({ path: asset(`phone-${mode}.png`), animations: 'disabled' });
      for (const name of [`app-${mode}.png`, `phone-${mode}.png`]) {
        images[name] = createHash('sha256').update(await readFile(asset(name))).digest('hex');
      }
    } finally { await context.close(); }
  }
} finally { await browser.close(); }
const manifest = JSON.parse(await readFile(resolve(root, 'frontend', 'dist', '.vite', 'manifest.json'), 'utf8'));
const sourcePaths = ['frontend/index.html', 'frontend/phone.html', 'frontend/src/desktop.js', 'frontend/src/phone.js', 'frontend/src/shared.js', 'frontend/src/install.js', 'frontend/src/theme.css', 'frontend/src/style.css', 'frontend/public/theme-init.js', 'frontend/vite.config.js', 'site/tests/capture.mjs', 'site/assets/example-qr.png'];
const sources = {};
for (const path of sourcePaths) {
  const bytes = await readFile(resolve(root, path));
  sources[path] = createHash('sha256').update(path.endsWith('.png') ? bytes : bytes.toString('utf8').replace(/\r\n/g, '\n')).digest('hex');
}
assert.ok(manifest['index.html'] && manifest['src/phone.js']);
await writeFile(asset('screenshots.json'), JSON.stringify({ exampleContent: true, qrURL: 'https://example.invalid/share-me-preview', dimensions: { app: [960, 640], phone: [390, 700] }, sources, images }, null, 2) + '\n');
console.log('Captured actual desktop and phone UI in light and dark themes with example data only.');
