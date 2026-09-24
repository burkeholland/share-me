import { test as base, expect } from '@playwright/test';
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { serveDesktop } from './desktop-fixture.js';

const test = base.extend({
  decisionMode: ['accept', { option: true }],
  receiver: async ({ decisionMode }, use) => {
    const dir = await mkdtemp(join(tmpdir(), 'shareme-approval-e2e-'));
    const data = join(dir, 'data');
    const stateFile = join(dir, 'receiver.json');
    const child = spawn(resolve('..', '.tools', 'bin', 'ShareMe-test.exe'), [
      '--headless', '--ip', '127.0.0.1', '--port', '0', '--dev-loopback',
      '--control-stdio', '--data', data, '--state-file', stateFile,
    ], { stdio: 'pipe' });
    let output = '';
    let stream = '';
    let pending = [];
    let processError;
    const decisions = new Set();
    const proposals = new Map();
    const decide = (id, accept) => {
      decisions.add(id);
      child.stdin.write(`${JSON.stringify({ id, accept })}\n`);
    };
    child.stderr.on('data', chunk => { output += chunk; });
    child.on('error', error => { processError = error; });
    child.stdin.on('error', error => { processError = error; });
    child.stdout.on('data', chunk => {
      stream += chunk;
      while (stream.includes('\n')) {
        const end = stream.indexOf('\n');
        const line = stream.slice(0, end).trim();
        stream = stream.slice(end + 1);
        if (!line) continue;
        try {
          const frame = JSON.parse(line);
          if (frame.type !== 'pending') continue;
          pending = frame.pending || [];
          for (const item of pending) {
            proposals.set(item.id, item);
            if (item.state === 'pending' && decisionMode !== 'manual' && !decisions.has(item.id)) {
              decide(item.id, decisionMode === 'accept');
            }
          }
        } catch (error) {
          processError = error;
        }
      }
    });
    try {
      let state;
      await expect(async () => {
        if (processError) throw processError;
        if (child.exitCode != null) throw new Error(`Receiver exited: ${output}`);
        state = JSON.parse(await readFile(stateFile, 'utf8'));
        expect((await fetch(`${state.address}/healthz`)).ok).toBeTruthy();
      }).toPass({ timeout: 15000 });
      await use({ ...state, dir: data, pending: () => pending, proposals, decide });
      if (processError) throw processError;
    } finally {
      if (child.exitCode == null) {
        const exited = new Promise(resolveExit => child.once('exit', resolveExit));
        child.kill();
        await exited;
      }
      await rm(dir, { recursive: true, force: true });
    }
  },
});

async function openPhone(page, receiver) {
  await page.goto(receiver.address);
  await expect(page.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible();
  expect(new URL(page.url()).hash).toBe('');
}

async function receivedFiles(receiver) {
  return (await readdir(receiver.inboxDir, { withFileTypes: true })).filter(entry => entry.isFile()).map(entry => entry.name);
}

test('permanent URL opens immediately and sends text with Windows approval', async ({ page, receiver }) => {
  await openPhone(page, receiver);
  const text = 'From iPhone: café 日本語\n<script>alert("not HTML")</script>';
  await page.getByLabel('Text or a link').fill(text);
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.getByRole('status')).toHaveText('Received');
  await expect(page.getByLabel('Text or a link')).toHaveValue('');
  expect(receiver.proposals.size).toBe(1);
  const files = await readdir(receiver.dir);
  const metadata = await Promise.all(files.filter(name => name.endsWith('.json')).map(name => readFile(join(receiver.dir, name), 'utf8')));
  expect(metadata.some(value => value.includes('café 日本語'))).toBeTruthy();
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Send to PC', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Connect to PC' })).toHaveCount(0);
});

test('same-name files are approved separately, scanned, and saved without overwrite', async ({ page, receiver }) => {
  await openPhone(page, receiver);
  await page.getByLabel('Choose files', { exact: true }).setInputFiles([
    { name: 'same-name.txt', mimeType: 'text/plain', buffer: Buffer.from('first file') },
    { name: 'same-name.txt', mimeType: 'text/plain', buffer: Buffer.from('second file') },
  ]);
  await expect(page.getByText('Received', { exact: true })).toHaveCount(2);
  expect(receiver.proposals.size).toBe(2);
  const files = await receivedFiles(receiver);
  expect(files).toHaveLength(2);
  const values = await Promise.all(files.map(name => readFile(join(receiver.inboxDir, name), 'utf8')));
  expect(values.sort()).toEqual(['first file', 'second file']);
  for (const name of files) {
    expect(await readFile(join(receiver.inboxDir, name) + ':Zone.Identifier', 'utf8')).toContain('ZoneId=3');
  }
});

test('adding a file during an active batch still sends it', async ({ page, receiver }) => {
  await openPhone(page, receiver);
  let release;
  let first = true;
  const gate = new Promise(resolveGate => { release = resolveGate; });
  await page.route('**/api/upload', async route => {
    if (first) { first = false; await gate; }
    await route.continue();
  });
  await page.getByLabel('Choose files', { exact: true }).setInputFiles({ name: 'first.txt', mimeType: 'text/plain', buffer: Buffer.from('one') });
  await expect(page.getByRole('button', { name: 'Cancel first.txt' })).toBeVisible();
  await page.getByLabel('Choose files', { exact: true }).setInputFiles({ name: 'second.txt', mimeType: 'text/plain', buffer: Buffer.from('two') });
  release();
  await expect(page.getByText('Received', { exact: true })).toHaveCount(2);
});

for (const name of ['unsafe.exe', 'unsafe.py', 'unsafe.docm']) {
  test(`${name} is blocked before prompting`, async ({ page, receiver }) => {
    await openPhone(page, receiver);
    await page.getByLabel('Choose files', { exact: true }).setInputFiles({ name, mimeType: 'application/octet-stream', buffer: Buffer.from('harmless placeholder') });
    await expect(page.locator('.upload-error')).toBeVisible();
    expect(receiver.proposals.size).toBe(0);
    expect(await receivedFiles(receiver)).toHaveLength(0);
  });
}

test('renamed executable content is blocked after approval', async ({ page, receiver }) => {
  await openPhone(page, receiver);
  await page.getByLabel('Choose files', { exact: true }).setInputFiles({ name: 'pretend.jpg', mimeType: 'image/jpeg', buffer: Buffer.from('MZ harmless placeholder, not a program') });
  await expect(page.locator('.upload-error')).toContainText('the file was not saved');
  expect(receiver.proposals.size).toBe(1);
  expect(await receivedFiles(receiver)).toHaveLength(0);
});

test('phone UI is compact', async ({ page, receiver }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await openPhone(page, receiver);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
  const words = await page.locator('body').innerText();
  expect(words.trim().split(/\s+/).length).toBeLessThan(40);
  await page.screenshot({ path: testInfo.outputPath('phone.png'), fullPage: true });
});

test.describe('explicit approval', () => {
  test.use({ decisionMode: 'manual' });

  test('file stays out of inbox until Windows accepts it', async ({ page, receiver }) => {
    await openPhone(page, receiver);
    await page.getByLabel('Choose files', { exact: true }).setInputFiles({ name: 'waiting.txt', mimeType: 'text/plain', buffer: Buffer.from('waiting') });
    await expect.poll(() => receiver.pending().filter(item => item.state === 'pending').length).toBe(1);
    expect(await receivedFiles(receiver)).toHaveLength(0);
    const pending = receiver.pending()[0];
    expect(pending.name).toBe('waiting.txt');
    expect(pending.source).toBe('127.0.0.1');
    await expect(page.getByText('Waiting for PC approval', { exact: true })).toBeVisible();
    receiver.decide(pending.id, true);
    await expect(page.getByText('Received', { exact: true })).toBeVisible();
    expect(await receivedFiles(receiver)).toHaveLength(1);
  });

  test('declining text preserves the phone input', async ({ page, receiver }) => {
    await openPhone(page, receiver);
    await page.getByLabel('Text or a link').fill('Do not save this');
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    await expect.poll(() => receiver.pending().filter(item => item.state === 'pending').length).toBe(1);
    await expect(page.getByRole('status')).toHaveText('Waiting for PC approval');
    receiver.decide(receiver.pending()[0].id, false);
    await expect(page.getByRole('status')).toHaveText('Declined on PC');
    await expect(page.getByLabel('Text or a link')).toHaveValue('Do not save this');
  });

  test('declining a large photo reports the decision without uploading its body', async ({ page, receiver }, testInfo) => {
    let uploads = 0;
    page.on('request', request => {
      if (new URL(request.url()).pathname === '/api/upload') uploads++;
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await openPhone(page, receiver);
    await page.getByLabel('Choose photos or videos').setInputFiles({
      name: 'declined-photo.jpg', mimeType: 'image/jpeg', buffer: Buffer.alloc(8 << 20),
    });
    await expect(page.getByText('Waiting for PC approval', { exact: true })).toBeVisible();
    await expect.poll(() => receiver.pending().length).toBe(1);
    expect(uploads).toBe(0);
    await page.screenshot({ path: testInfo.outputPath('phone-waiting.png') });
    receiver.decide(receiver.pending()[0].id, false);
    await expect(page.getByText('Declined on PC', { exact: true })).toBeVisible();
    await expect(page.locator('.upload-error')).toHaveCount(0);
    expect(uploads).toBe(0);
    expect(await receivedFiles(receiver)).toHaveLength(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
    await page.screenshot({ path: testInfo.outputPath('phone-declined.png') });
  });

  test('cancelling approval withdraws the Windows prompt without uploading', async ({ page, receiver }) => {
    await openPhone(page, receiver);
    await page.getByLabel('Choose files', { exact: true }).setInputFiles({
      name: 'cancelled.txt', mimeType: 'text/plain', buffer: Buffer.from('not sent'),
    });
    await expect(page.getByText('Waiting for PC approval', { exact: true })).toBeVisible();
    await expect.poll(() => receiver.pending().length).toBe(1);
    await page.getByRole('button', { name: 'Cancel cancelled.txt', exact: true }).click();
    await expect(page.getByText('Cancelled', { exact: true })).toBeVisible();
    await expect.poll(() => receiver.pending().length).toBe(0);
    expect(await receivedFiles(receiver)).toHaveLength(0);
  });

  test('preflight polls, then uploads only after acceptance', async ({ page, receiver }) => {
    const propose = await page.request.post(`${receiver.address}/api/request`, {
      headers: { 'X-Share-Me': '1' },
      data: { kind: 'file', name: 'approved.txt', size: -1 },
    });
    expect(propose.status()).toBe(202);
    const grant = await propose.json();
    const headers = { 'X-Share-Me': '1', 'X-Share-Me-Request': grant.id, 'X-Share-Me-Token': grant.token };
    const statusURL = `${receiver.address}/api/request/${grant.id}`;
    expect((await (await page.request.get(statusURL, { headers })).json()).status).toBe('pending');
    await expect.poll(() => receiver.pending().some(item => item.id === grant.id)).toBeTruthy();
    receiver.decide(grant.id, true);
    await expect.poll(async () => (await (await page.request.get(statusURL, { headers })).json()).status).toBe('accepted');
    const result = await page.request.post(`${receiver.address}/api/upload`, {
      headers, multipart: { file: { name: 'approved.txt', mimeType: 'text/plain', buffer: Buffer.from('approved content') } },
    });
    expect(result.status()).toBe(201);
    expect(await result.json()).not.toHaveProperty('path');
    const replay = await page.request.post(`${receiver.address}/api/upload`, {
      headers, multipart: { file: { name: 'approved.txt', mimeType: 'text/plain', buffer: Buffer.from('approved content') } },
    });
    expect(replay.ok()).toBeFalsy();
    expect(await receivedFiles(receiver)).toHaveLength(1);
  });

  test('desktop prompt drives the real receiver decision', async ({ page, receiver }, testInfo) => {
    test.skip(testInfo.project.name !== 'chromium', 'Windows uses a Chromium-based WebView');
    await page.setViewportSize({ width: 960, height: 650 });
    await page.exposeFunction('desktopState', () => ({
      status: { running: true, address: receiver.address, inboxDir: receiver.inboxDir },
      networks: [{ name: 'Local test', ip: '127.0.0.1' }], items: [],
      pending: receiver.pending(), error: '',
      settings: { startWithWindows: false, startMinimized: false, minimizeToTray: true },
      trayAvailable: true,
      qr: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jWZkAAAAASUVORK5CYII=',
    }));
    await page.exposeFunction('desktopDecision', (id, accept) => receiver.decide(id, accept));
    await page.addInitScript(() => {
      window.go = { main: { App: {
        GetState: () => window.desktopState(),
        Decide: (id, accept) => window.desktopDecision(id, accept),
      } } };
    });

    await serveDesktop(page, `${receiver.address}/desktop-test`);
    await expect(page.getByRole('heading', { name: 'Inbox', exact: true })).toBeVisible();
    expect((await page.locator('body').innerText()).trim().split(/\s+/).length).toBeLessThan(40);
    const proposal = await page.request.post(`${receiver.address}/api/request`, {
      headers: { 'X-Share-Me': '1' },
      data: { kind: 'text', name: 'Text', size: -1, text: 'Approval dialog check' },
    });
    const grant = await proposal.json();
    await expect(page.getByRole('dialog', { name: 'Receive text?' })).toBeVisible();
    await expect(page.getByRole('dialog')).toContainText('Approval dialog check');
    await page.screenshot({ path: testInfo.outputPath('desktop-approval.png') });
    await page.getByRole('button', { name: 'Accept', exact: true }).click();
    const headers = { 'X-Share-Me': '1', 'X-Share-Me-Request': grant.id, 'X-Share-Me-Token': grant.token };
    await expect.poll(async () => (await (await page.request.get(`${receiver.address}/api/request/${grant.id}`, { headers })).json()).status).toBe('accepted');
    const send = await page.request.post(`${receiver.address}/api/text`, {
      headers, data: { text: 'Approval dialog check' },
    });
    expect(send.status()).toBe(201);
    await expect(page.getByRole('dialog')).toHaveCount(0);
  });

  test('desktop startup and tray options save, persist, and recover from errors', async ({ page, receiver }, testInfo) => {
    test.skip(testInfo.project.name !== 'chromium', 'Windows uses a Chromium-based WebView');
    await page.setViewportSize({ width: 960, height: 680 });
    const desktopSettings = { startWithWindows: false, startMinimized: false, minimizeToTray: true };
    let saves = 0;
    let failSave = false;
    let hidden = 0;
    let trayAvailable = true;
    await page.exposeFunction('desktopState', () => ({
      status: { running: true, address: receiver.address, inboxDir: receiver.inboxDir },
      networks: [{ name: 'Local test', ip: '127.0.0.1' }], items: [], pending: [], error: '',
      settings: desktopSettings, trayAvailable,
    }));
    await page.exposeFunction('saveDesktopSettings', async next => {
      saves++;
      await new Promise(resolve => setTimeout(resolve, 80));
      if (failSave) throw new Error('Windows startup setting could not be saved');
      Object.assign(desktopSettings, next);
    });
    await page.exposeFunction('hideDesktop', () => { hidden++; });
    await page.addInitScript(() => {
      window.go = { main: { App: {
        GetState: () => window.desktopState(),
        SetDesktopSettings: next => window.saveDesktopSettings(next),
        MinimizeToTray: () => window.hideDesktop(),
      } } };
    });
    await serveDesktop(page, `${receiver.address}/desktop-settings`);
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    await expect(page.getByLabel('Start with Windows', { exact: true })).not.toBeChecked();
    await expect(page.getByLabel('Start minimized to tray', { exact: true })).not.toBeChecked();
    await expect(page.getByLabel('Minimize button hides to tray', { exact: true })).toBeChecked();
    await page.getByRole('button', { name: 'Close settings', exact: true }).click();
    await expect(page.getByRole('region', { name: 'Settings', exact: true })).toBeHidden();
    await expect(page.getByRole('heading', { name: 'Inbox', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    await page.getByLabel('Start with Windows', { exact: true }).check();
    await expect.poll(() => desktopSettings.startWithWindows).toBe(true);
    await page.getByLabel('Start minimized to tray', { exact: true }).check();
    await expect.poll(() => desktopSettings.startMinimized).toBe(true);
    await page.getByLabel('Minimize button hides to tray', { exact: true }).uncheck();
    await expect.poll(() => desktopSettings.minimizeToTray).toBe(false);
    expect(saves).toBe(3);
    await page.reload();
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    await expect(page.getByLabel('Start with Windows', { exact: true })).toBeChecked();
    await expect(page.getByLabel('Start minimized to tray', { exact: true })).toBeChecked();
    await expect(page.getByLabel('Minimize button hides to tray', { exact: true })).not.toBeChecked();
    failSave = true;
    await page.getByLabel('Start with Windows', { exact: true }).uncheck();
    await expect(page.getByRole('alert')).toContainText('could not be saved');
    await expect(page.getByLabel('Start with Windows', { exact: true })).toBeChecked();
    expect(desktopSettings.startWithWindows).toBe(true);
    await page.getByRole('button', { name: 'Dismiss message' }).click();
    await page.screenshot({ path: testInfo.outputPath('desktop-settings.png') });
    await page.getByRole('button', { name: 'Minimize to tray', exact: true }).click();
    await expect(page.getByRole('region', { name: 'Settings', exact: true })).toBeHidden();
    expect(hidden).toBe(1);
    trayAvailable = false;
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Minimize to tray', exact: true })).toBeDisabled();
  });
});
