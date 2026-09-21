import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { resolve, extname, sep } from 'node:path';

const root = resolve(fileURLToPath(new URL('../', import.meta.url)));
const mime = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8', '.svg': 'image/svg+xml', '.png': 'image/png', '.txt': 'text/plain; charset=utf-8', '.json': 'application/json', '.ico': 'image/x-icon' };
export async function startServer(port = 0) {
  const server = createServer(async (request, response) => {
    const pathname = new URL(request.url, 'http://localhost').pathname;
    if (!['GET', 'HEAD'].includes(request.method)) {
      response.writeHead(405).end(); return;
    }
    let path;
    try { path = resolve(root, '.' + decodeURIComponent(pathname === '/' ? '/index.html' : pathname)); }
    catch { response.writeHead(400).end(); return; }
    if (!path.startsWith(root + sep) || path.startsWith(resolve(root, 'tests') + sep)) {
      response.writeHead(404).end(); return;
    }
    try {
      if (!(await stat(path)).isFile()) { response.writeHead(404).end(); return; }
      const body = await readFile(path);
      response.writeHead(200, { 'Content-Type': mime[extname(path)] || 'application/octet-stream', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
      response.end(request.method === 'HEAD' ? undefined : body);
    } catch (error) {
      if (['ENOENT', 'ENOTDIR'].includes(error.code)) { response.writeHead(404).end(); return; }
      console.error('Could not serve site asset:', error);
      response.writeHead(500).end();
    }
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', resolve);
  });
  return { url: `http://127.0.0.1:${server.address().port}/`, close: () => new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve())) };
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const local = await startServer(Number(process.argv[2] || 4173));
  console.log(`Share Me website: ${local.url}`);
  const stop = async () => { await local.close(); process.exit(0); };
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
}
