import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';

const origin = process.env.SHAREME_TEST_ORIGIN || 'http://127.0.0.1:8787';

test('saves a streamed attachment without uploading it to the website', async ({ page, context }) => {
  context.on('console', message => { if (message.type() === 'error') console.error(message.text()); });
  await page.route('**/saving-test.js', route => route.fulfill({
    path: 'src\\save-transfer.js', contentType: 'text/javascript',
  }));
  await page.goto(origin);
  await page.evaluate(async () => {
    window.saving = await import('/saving-test.js');
    await window.saving.initializeSaving();
    window.addEventListener('peer:save-error', event => console.error(event.detail));
    const button = document.createElement('button');
    button.textContent = 'Save fixture';
    button.onclick = () => window.saving.acceptOutgoing({
      fetch: async () => {
        let remaining = 2 << 20;
        return new Response(new ReadableStream({
          pull(controller) {
            if (!remaining) { controller.close(); return; }
            const size = Math.min(16384, remaining);
            remaining -= size;
            controller.enqueue(new Uint8Array(size).fill(120));
          },
        }), { headers: { 'Content-Length': String(2 << 20) } });
      },
    }, { id: 'fixture', kind: 'file', name: 'fixture.txt', size: 2 << 20 });
    document.body.append(button);
  });
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Save fixture' }).click();
  const download = await downloadPromise;
  expect(await download.failure()).toBeNull();
  const saved = await readFile(await download.path());
  expect(saved.length).toBe(2 << 20);
  expect(saved.equals(Buffer.alloc(2 << 20, 'x'))).toBe(true);
});

test('prepares received images for the native share sheet', async ({ page }) => {
  await page.route('**/saving-test.js', route => route.fulfill({
    path: 'src\\save-transfer.js', contentType: 'text/javascript',
  }));
  await page.goto(origin);
  const result = await page.evaluate(async () => {
    const saving = await import('/saving-test.js');
    let shared;
    Object.defineProperty(navigator, 'canShare', {
      configurable: true, value: value => value.files?.[0]?.type === 'image/jpeg',
    });
    Object.defineProperty(navigator, 'share', {
      configurable: true, value: async value => { shared = value.files[0]; },
    });
    const bytes = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4]);
    const prepared = await saving.acceptOutgoing({
      fetch: async () => new Response(bytes, {
        headers: { 'Content-Length': String(bytes.length), 'Content-Type': 'image/jpeg' },
      }),
    }, { id: 'fixture', kind: 'file', name: 'received.jpg', size: bytes.length, mime: 'image/jpeg' });
    const available = saving.canShareImage(prepared.file);
    await saving.shareImage(prepared.file);
    Object.defineProperty(navigator, 'canShare', {
      configurable: true, value: () => false,
    });
    let unavailable;
    try {
      await saving.shareImage(prepared.file);
    } catch (error) {
      unavailable = error.message;
    }
    return {
      available,
      name: shared.name,
      type: shared.type,
      size: shared.size,
      bytes: [...new Uint8Array(await shared.arrayBuffer())],
      unavailable,
    };
  });
  expect(result).toEqual({
    available: true,
    name: 'received.jpg',
    type: 'image/jpeg',
    size: 8,
    bytes: [0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4],
    unavailable: 'Saving to Photos is unavailable in this browser. Use Download instead.',
  });
});

test('shows Save to Photos after accepting an image', async ({ page }, testInfo) => {
  const phoneSource = (await readFile('src/phone.js', 'utf8'))
    .replace("import { el, icon, brand, bytes, toast, errorMessage } from './shared.js';", '');
  const sharedSource = (await readFile('src/shared.js', 'utf8'))
    .replace(/^import '.\/(?:theme|style)\.css';\r?\n/gm, '');
  await page.route('**/phone-test.js', route => route.fulfill({
    body: `${sharedSource}\n${phoneSource}`, contentType: 'text/javascript',
  }));
  await page.goto(origin);
  await page.evaluate(async () => {
    document.body.innerHTML = '<main id="app" class="phone-shell"></main>';
    let shared;
    window.shareMePeer = {
      fetch: async path => {
        if (path === '/api/session') return Response.json({ maxFileBytes: 2 ** 31 });
        if (path === '/api/outbox') return Response.json({ items: [{
          id: 'photo', kind: 'file', name: 'vacation.jpg', size: 8, mime: 'image/jpeg',
        }] });
        throw new Error(`Unexpected path: ${path}`);
      },
      acceptOutgoing: async () => ({
        file: new File([new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4])], 'vacation.jpg', { type: 'image/jpeg' }),
      }),
      canShareImage: () => true,
      shareImage: async file => { shared = file; },
      downloadFile: () => {},
      activeSaves: () => false,
      get shared() { return shared; },
    };
    await import('/phone-test.js');
  });
  await expect(page.getByRole('button', { name: 'Accept Transfer' })).toBeVisible();
  await page.getByRole('button', { name: 'Accept Transfer' }).click();
  await expect(page.getByRole('button', { name: 'Save to Photos' })).toBeVisible();
  await expect(page.getByText('Choose Save Image')).toBeVisible();
  await page.getByRole('button', { name: 'Save to Photos' }).click();
  await expect(page.getByText('Share sheet closed', { exact: true })).toBeVisible();
  expect(await page.evaluate(() => ({
    name: window.shareMePeer.shared.name,
    type: window.shareMePeer.shared.type,
  }))).toEqual({ name: 'vacation.jpg', type: 'image/jpeg' });
  await page.screenshot({ path: testInfo.outputPath('save-to-photos.png'), fullPage: true });
});
