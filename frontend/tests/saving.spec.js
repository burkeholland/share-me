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
