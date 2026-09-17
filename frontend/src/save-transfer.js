let setup;
const active = new Set();
export function activeSaves() { return active.size > 0; }

export function initializeSaving() {
  if (!setup) setup = prepareSaving().catch(error => { setup = null; throw error; });
  return setup;
}

async function prepareSaving() {
  if (!('serviceWorker' in navigator)) throw new Error('This browser cannot save streamed transfers.');
  await navigator.serviceWorker.register('/save-worker.js', { scope: '/' });
  await navigator.serviceWorker.ready;
  if (navigator.serviceWorker.controller) return;
  await new Promise((resolve, reject) => {
    const changed = () => {
      if (navigator.serviceWorker.controller) {
        clearTimeout(timer);
        navigator.serviceWorker.removeEventListener('controllerchange', changed);
        resolve();
      }
    };
    const timer = setTimeout(() => {
      navigator.serviceWorker.removeEventListener('controllerchange', changed);
      reject(new Error('Reload Share Me to enable saving.'));
    }, 10000);
    navigator.serviceWorker.addEventListener('controllerchange', changed);
    changed();
  });
}

export async function acceptOutgoing(transport, item) {
  if (item.kind === 'text') {
    const response = await transport.fetch(`/api/outbox/${item.id}`);
    if (!response.ok) throw new Error('This transfer is no longer available.');
    if (Number(response.headers.get('Content-Length')) > 65536) {
      await response.body.cancel();
      throw new Error('Text transfer is too large.');
    }
    return { text: await response.text() };
  }
  await initializeSaving();
  const channel = new MessageChannel();
  const token = crypto.randomUUID().replaceAll('-', '');
  active.add(token);
  const abort = new AbortController();
  let reader;
  let frame;
  let stopped = false;
  let chain = Promise.resolve();
  let resolveReady;
  let rejectReady;
  const ready = new Promise((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
  const idle = setTimeout(() => fail(new Error('Transfer expired before saving started. Accept it again.')), 60000);
  function fail(error) {
    if (stopped) return;
    stopped = true;
    active.delete(token);
    clearTimeout(idle);
    frame?.remove();
    abort.abort();
    reader?.cancel().catch(error => console.warn('Transfer cleanup failed:', error));
    channel.port1.postMessage({ type: 'error', message: error.message });
    rejectReady(error);
    window.dispatchEvent(new CustomEvent('peer:save-error', { detail: error.message }));
  }
  channel.port1.onmessage = event => {
    chain = chain.then(async () => {
      const message = event.data;
      if (message.type === 'prepared') {
        resolveReady();
      } else if (message.type === 'start') {
        clearTimeout(idle);
        const response = await transport.fetch(`/api/outbox/${item.id}`, { signal: abort.signal });
        if (!response.ok || Number(response.headers.get('Content-Length')) !== item.size) {
          await response.body?.cancel();
          throw new Error('This transfer is unavailable or changed. Accept it again.');
        }
        reader = response.body.getReader();
        channel.port1.postMessage({ type: 'started' });
      } else if (message.type === 'pull') {
        if (!reader || stopped) throw new Error('Transfer is not ready');
        const { value, done } = await reader.read();
        if (done) {
          stopped = true;
          active.delete(token);
          reader.releaseLock();
          channel.port1.postMessage({ type: 'end' });
          channel.port1.close();
          frame?.remove();
        } else {
          const bytes = value.slice();
          channel.port1.postMessage({ type: 'chunk', bytes }, [bytes.buffer]);
        }
      } else if (message.type === 'cancel') {
        fail(new Error('Saving was cancelled. The transfer is still available on your PC.'));
      } else if (message.type === 'error') {
        fail(new Error(message.message || 'Saving failed. Accept the transfer again.'));
      }
    }).catch(fail);
  };
  navigator.serviceWorker.controller.postMessage({
    type: 'prepare', token, name: item.name, size: item.size,
  }, [channel.port2]);
  await ready;
  // Navigation reaches the service worker; an anchor's download attribute can
  // bypass it and fetch the (deliberately nonexistent) public server route.
  frame = document.createElement('iframe');
  frame.hidden = true;
  frame.src = `/receive/${token}/${encodeURIComponent(item.name)}`;
  document.body.append(frame);
  return { accepted: true };
}
