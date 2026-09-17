import { test as base, expect } from '@playwright/test';
import { basename, resolve } from 'node:path';

const test = base.extend({
  installer: async ({ page }, use) => {
    const scenario = {
      manifest: { schemaVersion: 2, transport: 'ssh-v1', name: 'Share Me', state: 'published', url: 'https://www.icloud.com/shortcuts/' + 'a'.repeat(32) },
      setup: { version: 1, host: '192.168.1.2', port: 49322, name: 'Example PC', enrollment: 'a'.repeat(43), fingerprint: 'SHA256:' + 'A'.repeat(43) },
      status: 200,
      calls: [],
      publicSetupRequests: [],
    };
    await page.exposeFunction('installerPeer', (path, options) => {
      scenario.calls.push({ path, method: options?.method || 'GET' });
      if (path === '/api/session') return { status: 200, body: { maxFileBytes: 2 ** 31 } };
      if (path === '/api/outbox') return { status: 200, body: { items: [] } };
      if (path === '/api/shortcut/setup') return { status: scenario.status, body: scenario.setup };
      throw new Error('Unexpected peer request: ' + path);
    });
    await page.addInitScript(() => {
      window.shareMePeer = {
        activeSaves: () => false,
        fetch: async (path, options) => {
          const reply = await window.installerPeer(path, options);
          return new Response(JSON.stringify(reply.body), { status: reply.status });
        },
      };
    });
    page.on('request', request => {
      if (new URL(request.url()).pathname === '/api/shortcut/setup') scenario.publicSetupRequests.push(request.url());
    });
    await page.route('https://shortcut.test/assets/**', route => route.fulfill({
      path: resolve('dist', 'assets', basename(new URL(route.request().url()).pathname)),
    }));
    await page.route('https://shortcut.test/assets/shortcut-install.json', route => route.fulfill({
      contentType: 'application/json', body: JSON.stringify(scenario.manifest),
    }));
    await page.route('https://shortcut.test/', route => route.fulfill({
      contentType: 'text/html', path: resolve('dist', 'phone.html'),
    }));
    await page.goto('https://shortcut.test/');
    await expect(page.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible();
    await use(scenario);
    expect(scenario.publicSetupRequests).toEqual([]);
  },
});

test('one generic installation link configures the installed Shortcut through the private peer', async ({ page, installer }, testInfo) => {
  await page.getByRole('button', { name: 'Add to share sheet' }).click();
  const dialog = page.getByRole('dialog', { name: 'Share sheet' });
  await expect(dialog.getByRole('link', { name: 'Add Shortcut' })).toHaveAttribute('href', installer.manifest.url);
  expect(installer.calls.filter(call => call.path === '/api/shortcut/setup')).toHaveLength(0);
  await dialog.getByRole('button', { name: 'Connect Shortcut' }).click();
  const open = dialog.getByRole('link', { name: 'Open Shortcuts' });
  await expect(open).toBeVisible();
  const link = new URL(await open.getAttribute('href'));
  expect(link.protocol).toBe('shortcuts:');
  expect(link.hostname).toBe('run-shortcut');
  expect(link.searchParams.get('name')).toBe('Share Me');
  expect(link.searchParams.get('input')).toBe('text');
  const input = link.searchParams.get('text');
  expect(input).toMatch(/^shareme-setup-v1:/);
  expect(JSON.parse(input.slice('shareme-setup-v1:'.length))).toEqual(installer.setup);
  expect(installer.calls.filter(call => call.path === '/api/shortcut/setup')).toEqual([{ path: '/api/shortcut/setup', method: 'POST' }]);
  await expect(dialog).toContainText(installer.setup.fingerprint);
  expect(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
  expect(page.url()).not.toContain(installer.setup.enrollment);
  expect(await page.evaluate(() => localStorage.length)).toBe(0);
  await page.screenshot({ path: testInfo.outputPath('shortcut-setup.png'), fullPage: true });
  await dialog.getByRole('button', { name: 'Close share sheet setup' }).click();
  await expect(dialog).toHaveCount(0);
});

test('signed downloads preserve the installed Shortcut name', async ({ page, installer }) => {
  installer.manifest.url = '/assets/ShareMe.shortcut';
  await page.getByRole('button', { name: 'Add to share sheet' }).click();
  const link = page.getByRole('link', { name: 'Add Shortcut' });
  await expect(link).toHaveAttribute('href', '/assets/ShareMe.shortcut');
  await expect(link).toHaveAttribute('download', 'Share Me.shortcut');
});

test('unpublished template never advertises installation or creates an enrollment', async ({ page, installer }) => {
  installer.manifest.state = 'unpublished';
  installer.manifest.url = '';
  await page.getByRole('button', { name: 'Add to share sheet' }).click();
  const dialog = page.getByRole('dialog', { name: 'Share sheet' });
  await expect(dialog).toContainText('not published');
  await expect(dialog.getByRole('link', { name: 'Add Shortcut' })).toHaveCount(0);
  await expect(dialog.getByRole('button', { name: 'Connect Shortcut' })).toHaveCount(0);
  expect(installer.calls.filter(call => call.path === '/api/shortcut/setup')).toHaveLength(0);
});

test('publisher can connect a manually installed copy without advertising an unverified release', async ({ page, installer }) => {
  installer.manifest.state = 'unpublished';
  installer.manifest.url = '';
  await page.getByRole('button', { name: 'Add to share sheet' }).click();
  const dialog = page.getByRole('dialog', { name: 'Share sheet' });
  await expect(dialog.getByRole('link', { name: 'Add Shortcut' })).toHaveCount(0);
  expect(installer.calls.filter(call => call.path === '/api/shortcut/setup')).toHaveLength(0);
  await dialog.getByRole('button', { name: 'Connect installed Shortcut' }).click();
  await expect(dialog.getByRole('link', { name: 'Open Shortcuts' })).toBeVisible();
  expect(installer.manifest.state).toBe('unpublished');
  expect(installer.calls.filter(call => call.path === '/api/shortcut/setup')).toHaveLength(1);
});

test('legacy and off-site installation links fail explicitly', async ({ page, installer }) => {
  installer.manifest.transport = 'http';
  await page.getByRole('button', { name: 'Add to share sheet' }).click();
  await expect(page.getByRole('alert')).toContainText('out of date');
  await page.getByRole('button', { name: 'Dismiss message' }).click();
  installer.manifest.transport = 'ssh-v1';
  installer.manifest.url = 'https://example.com/shortcut';
  await page.getByRole('button', { name: 'Add to share sheet' }).click();
  await expect(page.getByRole('alert')).toContainText('Invalid Shortcut installation link');
  await expect(page.getByRole('dialog')).toHaveCount(0);
});

for (const [field, value] of [['host', '8.8.8.8'], ['host', '127.0.0.1'], ['host', '192.168.1.2.evil.test'], ['port', 22], ['enrollment', 'bad'], ['fingerprint', 'untrusted']]) {
  test(`rejects invalid setup ${field}: ${value}`, async ({ page, installer }) => {
    installer.setup[field] = value;
    await page.getByRole('button', { name: 'Add to share sheet' }).click();
    await page.getByRole('button', { name: 'Connect Shortcut' }).click();
    await expect(page.getByRole('dialog').getByRole('alert')).toContainText('invalid Shortcut configuration');
    await expect(page.getByRole('link', { name: 'Open Shortcuts' })).toHaveCount(0);
  });
}

test('setup failure stays visible and can be retried without an HTTP fallback', async ({ page, installer }) => {
  installer.status = 503;
  const config = installer.setup;
  installer.setup = { error: 'Start receiving on your PC first.' };
  await page.getByRole('button', { name: 'Add to share sheet' }).click();
  await page.getByRole('button', { name: 'Connect Shortcut' }).click();
  await expect(page.getByRole('dialog').getByRole('alert')).toHaveText('Start receiving on your PC first.');
  await expect(page.getByRole('button', { name: 'Connect Shortcut' })).toBeEnabled();
  installer.status = 200;
  installer.setup = config;
  await page.getByRole('button', { name: 'Connect Shortcut' }).click();
  await expect(page.getByRole('link', { name: 'Open Shortcuts' })).toBeVisible();
});

for (const close of [false, true]) {
  test(`pending setup ${close ? 'is cancelled when the dialog closes' : 'times out with a retry'}`, async ({ page, installer }) => {
    await page.clock.install();
    await page.evaluate(() => {
      const original = window.shareMePeer.fetch;
      window.setupAborted = false;
      window.shareMePeer.fetch = (path, options) => path !== '/api/shortcut/setup' ? original(path, options) :
        new Promise((resolve, reject) => {
          options.signal.addEventListener('abort', () => {
            window.setupAborted = true;
            reject(options.signal.reason);
          }, { once: true });
        });
    });
    await page.getByRole('button', { name: 'Add to share sheet' }).click();
    await page.getByRole('button', { name: 'Connect Shortcut' }).click();
    await expect(page.getByRole('button', { name: 'Connecting...' })).toBeDisabled();
    if (close) {
      await page.getByRole('button', { name: 'Close share sheet setup' }).click();
      await expect(page.getByRole('dialog')).toHaveCount(0);
    } else {
      await page.clock.fastForward(16000);
      await expect(page.getByRole('dialog').getByRole('alert')).toContainText('timed out');
      await expect(page.getByRole('button', { name: 'Connect Shortcut' })).toBeEnabled();
    }
    await expect.poll(() => page.evaluate(() => window.setupAborted)).toBe(true);
  });
}
