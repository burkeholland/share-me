import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { startServer } from './serve.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const html = await readFile(resolve(root, 'site', 'index.html'), 'utf8');
const release = JSON.parse(await readFile(resolve(root, 'site', 'assets', 'release.json'), 'utf8'));

test('the landing page is static, accessible, and separate from the transfer app', () => {
  assert.match(html, /lang="en"/);
  assert.match(html, /name="viewport"/);
  assert.match(html, /Skip to content/);
  assert.match(html, /<figcaption>Desktop<\/figcaption>/);
  assert.match(html, /<figcaption>Mobile<\/figcaption>/);
  assert.match(html, /with example photos/);
  assert.doesNotMatch(html, /<iframe|<input|<textarea|<form|onload=|onclick=/i);
  assert.doesNotMatch(html, /shareme-signaling|window\.shareMePeer|pair=|enrollment|data:image/i);
  assert.equal((html.match(/<h1\b/g) || []).length, 1);
  const ids = [...html.matchAll(/\bid="([^"]+)"/g)].map(match => match[1]);
  assert.equal(new Set(ids).size, ids.length);
  for (const [, hash] of html.matchAll(/href="#([^"]+)"/g)) assert.ok(ids.includes(hash), hash);
  for (const [, tag] of html.matchAll(/<(img\b[^>]+)>/g)) assert.match(tag, /alt="[^"]*"/);
});

test('page copy describes the application without slogans', () => {
  assert.match(html, /<h1 id="headline">Transfer files between<br>iPhone and Windows\.<\/h1>/);
  assert.ok(html.includes(`<p class="lead">No mobile app required. Tiny ${Number((release.bytes / 1000000).toFixed(1))}MB download.</p>`));
  assert.equal((html.match(/class="lead"/g) || []).length, 1);
  assert.doesNotMatch(html, /platform-note/);
  assert.doesNotMatch(html, /preview-title|preview-heading|preview-note/);
  assert.match(html, /<section class="preview-desk" id="preview" aria-label="App screenshots">/);
  assert.match(html, /<h2 id="setup-title">Setup<\/h2>/);
  assert.match(html, /<h2 id="boundaries-title">How transfers work<\/h2>/);
  assert.doesNotMatch(html, /Your iPhone files|Scan\. Send\. Accept\.|That's the setup|take the direct route|It goes both ways|without the extra app/i);
});

test('download points to the published Windows preview', () => {
  assert.equal(release.downloadURL, `https://github.com/burkeholland/share-me/releases/download/${release.tag}/${release.fileName}`);
  assert.equal(release.releaseURL, `https://github.com/burkeholland/share-me/releases/tag/${release.tag}`);
  assert.ok(html.includes(`href="${release.downloadURL}">Download<svg`));
  assert.ok(html.includes(`href="${release.releaseURL}"`));
  assert.match(release.sha256, /^[a-f0-9]{64}$/);
  assert.match(release.sourceCommit, /^[a-f0-9]{40}$/);
  assert.ok(release.bytes > 0 && release.executableBytes > release.bytes);
  assert.match(html, /Unsigned preview/);
  assert.doesNotMatch(html, /Build for Windows|Windows download not published yet/);
  assert.match(html, /#boundaries/);
});

test('local stylesheet and image assets exist and images have matching dimensions', async () => {
  for (const [, src] of html.matchAll(/(?:src|href)="(\.\/[^"#]+)"/g)) {
    if (src === './') continue;
    await readFile(resolve(root, 'site', src));
  }
  const screenshot = JSON.parse(await readFile(resolve(root, 'site', 'assets', 'screenshots.json'), 'utf8'));
  assert.equal(screenshot.exampleContent, true);
  assert.equal(screenshot.qrURL, 'https://example.invalid/share-me-preview');
  for (const [name, expected] of Object.entries(screenshot.images)) {
    const bytes = await readFile(resolve(root, 'site', 'assets', name));
    assert.equal(createHash('sha256').update(bytes).digest('hex'), expected, name);
    assert.equal(bytes.subarray(1, 4).toString(), 'PNG');
    assert.deepEqual([bytes.readUInt32BE(16), bytes.readUInt32BE(20)], screenshot.dimensions[name.split('-')[0]], name);
  }
  for (const [path, expected] of Object.entries(screenshot.sources)) {
    const bytes = await readFile(resolve(root, path));
    const canonical = path.endsWith('.png') ? bytes : bytes.toString('utf8').replace(/\r\n/g, '\n');
    assert.equal(createHash('sha256').update(canonical).digest('hex'), expected, `Recapture stale screenshot source: ${path}`);
  }
});

test('the vendored Postrboard stylesheet retains its exact license and hash', async () => {
  const css = await readFile(resolve(root, 'site', 'assets', 'postrboard.css'));
  assert.equal(createHash('sha256').update(css).digest('hex'), 'd5e7a983b3b35413223b6eebc564c79fd7d50e8e693c13b9d7e22eabeccacade');
  const notice = await readFile(resolve(root, 'site', 'assets', 'NOTICE.txt'), 'utf8');
  assert.match(notice, /MIT License/);
  assert.match(notice, /ISC License/);
  assert.match(notice, /Copyright \(c\) 2026 Burke Holland/);
});

test('preview server serves assets without exposing tests, the receiver, or workspace files', async () => {
  const server = await startServer();
  try {
    assert.equal((await fetch(server.url)).status, 200);
    assert.equal((await fetch(server.url + 'style.css')).headers.get('content-type'), 'text/css; charset=utf-8');
    assert.equal((await fetch(server.url + 'assets/app-light.png')).status, 200);
    assert.equal((await fetch(server.url + 'index.html', { method: 'HEAD' })).status, 200);
    for (const path of ['api/session', 'api/upload', 'tests/capture.mjs', '.git/config', '..%5cREADME.md', '%zz']) {
      assert.ok([400, 404].includes((await fetch(server.url + path)).status), path);
    }
    assert.equal((await fetch(server.url, { method: 'POST' })).status, 405);
  } finally { await server.close(); }
});
