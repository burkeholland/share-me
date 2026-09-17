import { basename, resolve } from 'node:path';

export async function serveDesktop(page, url) {
  const origin = new URL(url).origin;
  await page.route(`${origin}/assets/**`, route => route.fulfill({
    path: resolve('dist', 'assets', basename(new URL(route.request().url()).pathname)),
  }));
  await page.route(url, route => route.fulfill({
    contentType: 'text/html', path: resolve('dist', 'index.html'),
  }));
  await page.goto(url);
}
