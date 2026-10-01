import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { runInNewContext } from 'node:vm';

test('shared theme bootstrap preserves valid choices and validates other values', async () => {
  const source = await readFile(new URL('../public/theme-init.js', import.meta.url), 'utf8');
  for (const [search, systemDark, want] of [
    ['?scoutTheme=light', true, 'light'],
    ['?scoutTheme=dark', false, 'dark'],
    ['', true, 'dark'],
    ['', false, 'light'],
    ['?scoutTheme=invalid', true, 'dark'],
    ['?scoutTheme=invalid', false, 'light'],
  ]) {
    let theme;
    runInNewContext(source, {
      URLSearchParams,
      window: { location: { search }, matchMedia: () => ({ matches: systemDark }) },
      document: { documentElement: { setAttribute: (name, value) => { assert.equal(name, 'data-theme'); theme = value; } } },
    });
    assert.equal(theme, want);
  }
});

test('all pages use the external bootstrap and desktop CSP matches the contract', async () => {
  for (const page of ['index.html', 'phone.html', 'secure.html']) {
    const html = await readFile(new URL(`../${page}`, import.meta.url), 'utf8');
    assert.match(html, /<script src="\/theme-init\.js"><\/script>/);
    assert.doesNotMatch(html, /<script(?![^>]*\bsrc=)[^>]*>/i);
  }
  const html = await readFile(new URL('../dist/index.html', import.meta.url), 'utf8');
  const expected = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'";
  assert.ok(html.includes(`http-equiv="Content-Security-Policy" content="${expected}"`));
});
