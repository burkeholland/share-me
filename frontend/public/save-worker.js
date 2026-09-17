const waiting = new Map();
const validToken = /^[a-f0-9]{32}$/;
const maxSize = 2 ** 31;

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));

self.addEventListener('message', event => {
  const data = event.data;
  const port = event.ports[0];
  if (!port || !event.source?.id || data?.type !== 'prepare') return;
  const source = new URL(event.source.url);
  if (source.origin !== self.location.origin || !validToken.test(data.token) ||
      typeof data.name !== 'string' || !data.name.trim() || data.name.length > 1024 ||
      /[\r\n\0\\/]/.test(data.name) || !Number.isSafeInteger(data.size) || data.size < 0 || data.size > maxSize) {
    port.postMessage({ type: 'error', message: 'Invalid transfer.' });
    return;
  }
  for (const [token, entry] of waiting) {
    if (entry.expires < Date.now()) {
      waiting.delete(token);
      entry.port.postMessage({ type: 'error', message: 'Transfer expired. Accept it again.' });
      entry.port.close();
    }
  }
  if (waiting.size >= 4 || waiting.has(data.token)) {
    port.postMessage({ type: 'error', message: 'Finish an active transfer first.' });
    return;
  }
  waiting.set(data.token, { ...data, port, client: event.source.id, expires: Date.now() + 60000 });
  port.postMessage({ type: 'prepared' });
});

self.addEventListener('fetch', event => {
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin || !url.pathname.startsWith('/receive/')) return;
  event.respondWith(receive(event, url));
});

async function receive(event, url) {
  const token = url.pathname.split('/')[2];
  const entry = waiting.get(token);
  if (!entry || entry.expires < Date.now() || !['GET', 'HEAD'].includes(event.request.method) ||
      (event.clientId && event.clientId !== entry.client)) {
    return new Response('Transfer expired. Open Share Me and accept it again.', { status: 410 });
  }
  const name = encodeURIComponent(entry.name).replace(/[!'()*]/g, c => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
  const headers = {
    'Content-Type': 'application/octet-stream',
    'Content-Disposition': `attachment; filename*=UTF-8''${name}`,
    'Content-Length': String(entry.size),
    'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff',
    'Content-Security-Policy': "default-src 'none'; sandbox",
  };
  if (event.request.method === 'HEAD') return new Response(null, { headers });
  waiting.delete(token);
  const port = entry.port;
  let startedResolve;
  let startedReject;
  const started = new Promise((resolve, reject) => { startedResolve = resolve; startedReject = reject; });
  let controller;
  let pullResolve;
  let pullReject;
  let total = 0;
  let ended = false;
  let finish;
  event.waitUntil(new Promise(resolve => { finish = resolve; }));
  const timer = setTimeout(() => fail(new Error('PC did not start the transfer.')), 30000);
  function fail(error) {
    if (ended) return;
    ended = true;
    clearTimeout(timer);
    startedReject(error);
    pullReject?.(error);
    controller?.error(error);
    port.postMessage({ type: 'cancel' });
    port.close();
    finish();
  }
  port.onmessage = event => {
    const message = event.data;
    if (ended) return;
    if (message.type === 'started') {
      clearTimeout(timer);
      startedResolve();
    } else if (message.type === 'chunk') {
      if (!pullResolve || !(message.bytes instanceof Uint8Array) || message.bytes.length > 16384 ||
          !message.bytes.length || total + message.bytes.length > entry.size) {
        fail(new Error('Invalid transfer stream.'));
        return;
      }
      total += message.bytes.length;
      controller.enqueue(message.bytes);
      const resolve = pullResolve;
      pullResolve = pullReject = null;
      resolve();
    } else if (message.type === 'end') {
      if (total !== entry.size) { fail(new Error('Transfer was incomplete.')); return; }
      ended = true;
      controller.close();
      pullResolve?.();
      port.close();
      finish();
    } else if (message.type === 'error') {
      fail(new Error(message.message || 'Transfer failed.'));
    }
  };
  const stream = new ReadableStream({
    start(value) { controller = value; },
    async pull() {
      await started;
      if (ended) return;
      await new Promise((resolve, reject) => {
        pullResolve = resolve;
        pullReject = reject;
        port.postMessage({ type: 'pull' });
      });
    },
    cancel() { fail(new Error('Saving was cancelled.')); },
  }, { highWaterMark: 0 });
  port.postMessage({ type: 'start' });
  try {
    await started;
    return new Response(stream, { headers });
  } catch {
    return new Response('Transfer failed. Open Share Me and accept it again.', { status: 503, headers: { 'Cache-Control': 'no-store' } });
  }
}
