import { readFile, mkdir, copyFile, writeFile, unlink } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dist = join(root, 'frontend', 'dist');
const output = join(root, 'service', 'public');
const manifest = JSON.parse(await readFile(join(dist, '.vite', 'manifest.json'), 'utf8'));
const files = new Set(['save-worker.js', 'theme-init.js']);
const visited = new Set();
function visit(key) {
  if (visited.has(key)) return;
  const chunk = manifest[key];
  if (!chunk) throw new Error(`Missing hosted frontend entry: ${key}`);
  visited.add(key);
  files.add(chunk.file);
  for (const path of chunk.css || []) files.add(path);
  for (const path of chunk.assets || []) files.add(path);
  for (const key of [...chunk.imports || [], ...chunk.dynamicImports || []]) visit(key);
}
visit('secure.html');
await mkdir(output, { recursive: true });
let previous = [];
try { previous = JSON.parse(await readFile(join(output, '.generated-files.json'), 'utf8')); }
catch (error) { if (error.code !== 'ENOENT') throw error; }
for (const path of files) {
  const source = resolve(dist, path);
  const target = resolve(output, path);
  if (!source.startsWith(dist + sep) || !target.startsWith(output + sep)) throw new Error('Invalid asset path');
  await mkdir(dirname(target), { recursive: true });
  await copyFile(source, target);
}
const html = await readFile(join(dist, 'secure.html'), 'utf8');
await writeFile(join(output, 'index.html'), html);
files.add('index.html');
for (const path of previous) {
  if (files.has(path)) continue;
  const target = resolve(output, path);
  if (!target.startsWith(output + sep)) throw new Error('Invalid stale asset path');
  try { await unlink(target); } catch (error) { if (error.code !== 'ENOENT') throw error; }
}
await writeFile(join(output, '.generated-files.json'), JSON.stringify([...files], null, 2));
const hashes = [...html.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/g)]
  .map(match => `sha256-${createHash('sha256').update(match[1]).digest('base64')}`);
console.log(`Staged ${files.size} hosted files. Inline CSP hashes: ${hashes.join(' ')}`);
