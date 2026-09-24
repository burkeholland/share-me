import { test as base, expect } from '@playwright/test';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, readdir, rm } from 'node:fs/promises';
import { tmpdir, networkInterfaces } from 'node:os';
import { join, resolve } from 'node:path';

const origin = process.env.SHAREME_TEST_ORIGIN || 'http://127.0.0.1:8787';
const localIP = process.env.SHAREME_TEST_IP || Object.entries(networkInterfaces())
  .filter(([name]) => !/vethernet|virtual|vpn|loopback/i.test(name)).flatMap(([, addresses]) => addresses).find(
  address => address.family === 'IPv4' && !address.internal &&
    /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(address.address),
)?.address;
const test = base.extend({
  peer: async ({}, use, testInfo) => {
    const dir = await mkdtemp(join(tmpdir(), 'shareme-secure-test-'));
    const bootstrap = join(dir, 'bootstrap.json');
    const child = spawn(resolve(process.env.SHAREME_TEST_EXE || join('..', '.tools', 'bin', 'ShareMe-test.exe')), [
      '--headless-peer', '--ip', localIP || '127.0.0.1', '--service-url', origin,
      '--data', dir, '--state-file', bootstrap, '--dev-loopback', '--control-stdio',
    ], { stdio: 'pipe' });
    let stderr = '';
    let stream = '';
    let state = {};
    let processError;
    child.stderr.on('data', chunk => { stderr += chunk; });
    child.on('error', error => { processError = error; });
    child.stdin.on('error', error => { processError = error; });
    child.stdout.on('data', chunk => {
      stream += chunk;
      while (stream.includes('\n')) {
        const end = stream.indexOf('\n');
        const line = stream.slice(0, end).trim();
        stream = stream.slice(end + 1);
        if (line) {
          try { state = JSON.parse(line); }
          catch (error) { processError = error; }
        }
      }
    });
    const check = () => {
      if (processError) throw processError;
      if (child.exitCode !== null) throw new Error(`Peer stopped: ${stderr}`);
    };
    const command = value => { check(); child.stdin.write(`${JSON.stringify(value)}\n`); };
    try {
      let info;
      await expect(async () => {
        check();
        info = JSON.parse(await readFile(bootstrap, 'utf8'));
        expect(state.connected, state.message || stderr).toBe(true);
      }).toPass({ timeout: 20000 });
      await use({ ...info, dir, state: () => { check(); return state; }, command });
      check();
    } finally {
      if (testInfo.status !== testInfo.expectedStatus) console.error('Native peer:', state.message, stderr);
      await testInfo.attach('peer-diagnostics', { body: Buffer.from(JSON.stringify({ state, stderr })), contentType: 'application/json' });
      if (child.exitCode === null) {
        const exit = new Promise(resolve => child.once('exit', resolve));
        child.stdin.end();
        const timeout = setTimeout(() => child.kill(), 5000);
        await exit;
        clearTimeout(timeout);
      }
      await rm(dir, { recursive: true, force: true });
    }
  },
});

test.describe('encrypted browser transfers', () => {
  test.setTimeout(90000);
  // Playwright's Windows WebKit build does not implement RTCPeerConnection.
  // Its saving flow is covered separately without mocking file contents.
  test.skip(({ browserName }) => browserName === 'webkit', 'Windows WebKit lacks WebRTC; verify this connection on physical Safari');

  test('pairs, transfers both ways, remembers browser, and revokes access', async ({ page, peer, browser }, testInfo) => {
    const errors = [];
    let signalingClosed = 0;
    page.on('websocket', socket => socket.on('close', () => { signalingClosed++; }));
    page.on('pageerror', error => errors.push(error.message));
    page.on('console', message => { if (message.type() === 'error') console.error(message.text()); });
    await page.goto(peer.pairURL);
    await expect.poll(() => peer.state().pairs?.length, { timeout: 30000 }).toBe(1);
    await expect(page.getByRole('status')).toContainText('Approve this phone');
    peer.command({ pair: peer.state().pairs[0].id, accept: true });
    await expect(page.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible({ timeout: 15000 });
    await expect.poll(() => peer.state().devices?.[0]?.connected).toBe(true);
    await expect.poll(() => signalingClosed).toBe(1);
    const device = peer.state().devices[0];
    expect(page.url()).not.toContain('pair=');

    await page.getByLabel('Text or a link').fill('Encrypted iPhone note');
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    await expect.poll(() => peer.state().pending?.length).toBe(1);
    await expect(page.getByRole('status')).toHaveText('Waiting for PC approval');
    peer.command({ id: peer.state().pending[0].id, accept: false });
    await expect(page.getByRole('status')).toHaveText('Declined on PC');
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    await expect.poll(() => peer.state().pending?.length).toBe(1);
    peer.command({ id: peer.state().pending[0].id, accept: true });
    await expect(page.getByRole('status')).toHaveText('Received');
    await expect.poll(() => peer.state().pending?.length).toBe(0);

    await page.getByLabel('Choose files', { exact: true }).setInputFiles({
      name: 'encrypted.txt', mimeType: 'text/plain', buffer: Buffer.from('Encrypted incoming file'),
    });
    await expect.poll(() => peer.state().pending?.[0]?.name).toBe('encrypted.txt');
    peer.command({ id: peer.state().pending[0].id, accept: true });
    await expect(page.getByText('Received', { exact: true })).toHaveCount(2);
    const files = await readdir(peer.inboxDir);
    const saved = files.find(name => name.endsWith('-encrypted.txt'));
    expect(saved).toBeTruthy();
    expect(await readFile(join(peer.inboxDir, saved), 'utf8')).toBe('Encrypted incoming file');
    expect(await readFile(join(peer.inboxDir, saved) + ':Zone.Identifier', 'utf8')).toContain('ZoneId=3');

    const outgoing = join(peer.dir, 'outgoing.txt');
    const bytes = Buffer.alloc(2 << 20, 'x');
    await writeFile(outgoing, bytes);
    peer.command({ device: device.id, file: outgoing });
    await expect(page.getByRole('button', { name: 'Accept Transfer', exact: true })).toBeVisible();
    const downloadEvent = page.waitForEvent('download');
    await page.getByRole('button', { name: 'Accept Transfer', exact: true }).click();
    const download = await downloadEvent;
    expect(download.suggestedFilename()).toBe('outgoing.txt');
    const downloaded = await download.path();
    expect((await readFile(downloaded)).equals(bytes)).toBe(true);

    await page.reload();
    await expect(page.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible({ timeout: 20000 });
    expect(peer.state().pairs).toHaveLength(0);
    await expect(page.getByRole('button', { name: 'Accept Transfer', exact: true })).toBeVisible();
    const stranger = await browser.newContext();
    try {
      const guest = await stranger.newPage();
      await guest.goto(`${origin}/#room=${peer.room}`);
      await expect(guest.getByRole('status')).toContainText('Add phone');
      await expect(guest.getByRole('button', { name: 'Accept Transfer', exact: true })).toHaveCount(0);
    } finally { await stranger.close(); }
    await page.screenshot({ path: testInfo.outputPath('secure-phone.png'), fullPage: true });
    peer.command({ revoke: device.id });
    await expect.poll(() => peer.state().devices?.length).toBe(0);
    await expect(page.getByRole('alert').first()).toBeVisible();
    await expect.poll(() => peer.state().outbox?.length).toBe(0);
    await expect.poll(() => peer.state().pairURL).toContain('&pair=');
    const replacementURL = peer.state().pairURL;
    expect(replacementURL).not.toBe(peer.pairURL);
    await page.evaluate(url => { location.hash = new URL(url).hash; }, replacementURL);
    await expect.poll(() => peer.state().pairs?.length, { timeout: 30000 }).toBe(1);
    await expect(page.getByRole('status')).toContainText('Approve this phone');
    peer.command({ pair: peer.state().pairs[0].id, accept: true });
    await expect(page.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible({ timeout: 15000 });
    await expect.poll(() => peer.state().devices?.[0]?.connected).toBe(true);
    expect(peer.state().devices[0].id).not.toBe(device.id);
    await expect.poll(() => peer.state().pairURL).toBe('');
    await page.getByLabel('Text or a link').fill('Reconnected after removal');
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    await expect.poll(() => peer.state().pending?.length).toBe(1);
    peer.command({ id: peer.state().pending[0].id, accept: true });
    await expect(page.getByRole('status')).toHaveText('Received');
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible({ timeout: 20000 });
    expect(peer.state().pairs).toHaveLength(0);
    expect(errors).toEqual([]);
  });
});
