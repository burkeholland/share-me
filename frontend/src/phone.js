import { el, icon, brand, bytes, toast, errorMessage } from './shared.js';

const root = document.getElementById('app');
if (location.hash && !window.shareMePeer) history.replaceState(null, '', location.pathname + location.search);
let session;
let queue = [];
let processing = false;
let currentUpload;
let textRequest;
let offersBusy = false;
let offersKey = '';
const acceptedOffers = new Map();
let knownOffers = new Set();
let receivedOffers = false;

root.append(el('header', { class: 'phone-header' }, brand()),
  el('div', { id: 'phone-content' }, el('p', { class: 'muted', role: 'status' }, 'Connecting...')));
const content = document.getElementById('phone-content');

async function request(path, { method = 'GET', body, signal, headers = {}, timeout = 15000 } = {}) {
  const controller = new AbortController();
  const abort = () => controller.abort();
  signal?.addEventListener('abort', abort, { once: true });
  if (signal?.aborted) abort();
  const timer = setTimeout(abort, timeout);
  try {
    let response;
    try {
      response = await (window.shareMePeer?.fetch || fetch)(path, {
        method, cache: 'no-store',
        headers: { 'X-Share-Me': '1', ...(body ? { 'Content-Type': 'application/json' } : {}), ...headers },
        body: body ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });
    } catch (error) {
      if (signal?.aborted) throw new DOMException('Cancelled.', 'AbortError');
      if (controller.signal.aborted) throw new Error('Timed out. Check the PC inbox before retrying.');
      throw new Error('Connection lost. Check the PC inbox before retrying.');
    }
    let data;
    try {
      data = await response.json();
    } catch {
      if (signal?.aborted) throw new DOMException('Cancelled.', 'AbortError');
      throw new Error('Unexpected response. Check the PC inbox before retrying.');
    }
    if (!response.ok) throw new Error(data.error || `Request failed (${response.status}).`);
    return data;
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', abort);
  }
}

class DeclinedError extends Error {
  constructor() { super('Declined on PC'); this.name = 'DeclinedError'; }
}

function waitForPoll(signal) {
  return new Promise((resolve, reject) => {
    const abort = () => {
      clearTimeout(timer);
      reject(new DOMException('Cancelled.', 'AbortError'));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort);
      resolve();
    }, 400);
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) abort();
  });
}

async function sendApproved(payload, signal, send) {
  signal.throwIfAborted();
  // Finish the small proposal even on cancellation so its token can withdraw the PC prompt.
  const grant = await request('/api/request', { method: 'POST', body: payload });
  if (!/^[a-f0-9]{32}$/.test(grant.id) || !/^[A-Za-z0-9_-]{43}$/.test(grant.token)) {
    throw new Error('Unexpected approval response. Try again.');
  }
  const path = `/api/request/${grant.id}`;
  const headers = { 'X-Share-Me-Request': grant.id, 'X-Share-Me-Token': grant.token };
  let terminal = false;
  try {
    let status = grant.status;
    const deadline = performance.now() + 3 * 60 * 1000;
    while (status === 'pending') {
      if (performance.now() >= deadline) throw new Error('Approval timed out. Send again.');
      await waitForPoll(signal);
      ({ status } = await request(path, { headers, signal }));
    }
    signal.throwIfAborted();
    terminal = ['declined', 'expired', 'cancelled'].includes(status);
    if (status === 'declined') throw new DeclinedError();
    if (status === 'expired') throw new Error('Approval timed out. Send again.');
    if (status === 'cancelled') throw new DOMException('Cancelled.', 'AbortError');
    if (status !== 'accepted') throw new Error('Unexpected approval response. Try again.');
    return await send(headers);
  } catch (error) {
    if (!terminal) {
      try {
        await request(path, { method: 'DELETE', headers, timeout: 5000 });
      } catch {
        throw new Error(`${signal.aborted ? 'Cancelled on this phone.' : errorMessage(error)} Could not dismiss the PC request; it will expire.`);
      }
    }
    throw error;
  }
}

async function initialize() {
  try {
    session = await request('/api/session');
    renderSend();
  } catch (error) {
    content.replaceChildren(el('h1', {}, 'PC unavailable'),
      el('p', { class: 'muted space-top' }, 'Keep Share Me open on the same network.'),
      el('button', { class: 'primary space-top', onclick: initialize }, 'Retry'));
  }
}

function renderSend() {
  const photoInput = el('input', { type: 'file', accept: 'image/*,video/*', multiple: true, 'aria-label': 'Choose photos or videos', onchange: event => addFiles(event.target) });
  const fileInput = el('input', { type: 'file', multiple: true, 'aria-label': 'Choose files', onchange: event => addFiles(event.target) });
  const textarea = el('textarea', { id: 'send-text', rows: 4, placeholder: 'Text or a link', 'aria-label': 'Text or a link' });
  const send = el('button', { class: 'primary', type: 'submit' }, 'Send', icon('arrow', 17));
  const cancel = el('button', { type: 'button', hidden: true, onclick: () => textRequest?.abort() }, 'Cancel');
  const textStatus = el('span', { class: 'meta', role: 'status' });
  const textForm = el('form', { class: 'text-compose', onsubmit: async event => {
    event.preventDefault();
    const text = textarea.value;
    if (!text.trim()) {
      textarea.focus();
      return;
    }
    if (new TextEncoder().encode(text).length > 65536) {
      toast('Text is limited to 64 KB. Send a file instead.', true);
      return;
    }
    textRequest = new AbortController();
    send.disabled = textarea.disabled = true;
    cancel.hidden = false;
    textStatus.textContent = 'Waiting for PC approval';
    try {
      const signal = textRequest.signal;
      await sendApproved({ kind: 'text', name: 'Text', size: -1, text }, signal, headers => {
        textStatus.textContent = 'Sending...';
        return request('/api/text', { method: 'POST', body: { text }, headers, signal });
      });
      textarea.value = '';
      textStatus.textContent = 'Received';
    } catch (error) {
      if (error.name === 'DeclinedError') textStatus.textContent = 'Declined on PC';
      else if (error.name === 'AbortError') textStatus.textContent = 'Cancelled';
      else {
        textStatus.textContent = 'Not received';
        toast(errorMessage(error), true);
      }
    } finally {
      textRequest = null;
      send.disabled = textarea.disabled = false;
      cancel.hidden = true;
    }
  } }, textarea, el('div', { class: 'send-actions' }, textStatus, el('div', { class: 'cluster' }, cancel, send)));
  content.replaceChildren(
    window.shareMePeer ? el('section', { id: 'receive-panel', class: 'receive-panel', 'aria-label': 'Transfers from PC', 'aria-live': 'polite' }) : null,
    el('div', { class: 'phone-heading' },
      el('h1', {}, 'Send to PC'),
      el('p', { class: 'mono muted address-caption' }, location.host)),
    el('section', { class: 'file-pickers', 'aria-label': 'Send files' },
      el('label', { class: 'send-picker' }, photoInput, icon('photo', 28), el('strong', {}, 'Photos')),
      el('label', { class: 'send-picker' }, fileInput, icon('file', 28), el('strong', {}, 'Files'))),
    el('div', { id: 'upload-queue', class: 'upload-queue', 'aria-live': 'polite' }),
    textForm,
    el('footer', { class: 'phone-footer minimal-phone-footer' },
      el('span', { class: 'meta' }, window.shareMePeer ? 'Direct / encrypted' : 'Approve transfers on your PC.')));
  renderQueue();
  if (window.shareMePeer) refreshOffers();
}

async function refreshOffers() {
  if (!window.shareMePeer || offersBusy || !document.getElementById('receive-panel')) return;
  offersBusy = true;
  try {
    const data = await request('/api/outbox');
    const signature = JSON.stringify(data.items);
    if (signature === offersKey) return;
    offersKey = signature;
    const items = data.items || [];
    const fresh = items.some(item => !knownOffers.has(item.id));
    knownOffers = new Set(items.map(item => item.id));
    document.title = items.length ? `(${items.length}) Share Me` : 'Share Me';
    if (fresh && receivedOffers && document.hidden && isSecureContext && 'Notification' in window && Notification.permission === 'granted') {
      try { new Notification('Share Me', { body: 'A transfer is ready.', tag: 'share-me-ready' }); }
      catch { toast('A transfer is ready. Notifications are unavailable in this browser.', true); }
    }
    receivedOffers = true;
    const panel = document.getElementById('receive-panel');
    panel.replaceChildren(...items.map(item => {
      const feedback = el('span', { class: 'meta', role: 'status' }, acceptedOffers.has(item.id) ? 'Accepted' : '');
      const output = el('div');
      const accept = el('button', { class: 'primary' }, 'Accept Transfer');
      accept.addEventListener('click', async () => {
        accept.disabled = true;
        try {
          const result = await window.shareMePeer.acceptOutgoing(item);
          acceptedOffers.set(item.id, true);
          feedback.textContent = 'Accepted';
          if (result.text !== undefined) {
            const copy = el('button', { onclick: async () => {
              try { await navigator.clipboard.writeText(result.text); toast('Copied'); }
              catch { toast('Select the text and use Copy.', true); }
            } }, icon('copy', 16), 'Copy');
            output.replaceChildren(el('pre', {}, result.text), copy);
          }
        } catch (error) {
          feedback.textContent = 'Not received';
          toast(errorMessage(error), true);
        } finally { accept.disabled = false; }
      });
      return el('article', { class: 'receive-card' },
        el('div', { class: 'cluster' }, icon(item.kind === 'text' ? 'text' : 'file', 22),
          el('div', { class: 'transfer-content' }, el('strong', { class: 'transfer-name' }, item.name), el('span', { class: 'meta' }, bytes(item.size)))),
        accept, feedback, output);
    }));
  } catch (error) {
    document.getElementById('receive-panel')?.replaceChildren(el('p', { class: 'notice error', role: 'alert' }, errorMessage(error)));
    offersKey = '';
  } finally { offersBusy = false; }
}

function addFiles(input) {
  const files = [...input.files];
  input.value = '';
  if (queue.filter(item => ['waiting', 'approving', 'sending'].includes(item.state)).length + files.length > 50) {
    toast('Send up to 50 files at a time.', true);
    return;
  }
  queue = queue.filter(item => item.state !== 'sent');
  for (const file of files) {
    const tooLarge = file.size > session.maxFileBytes;
    queue.push({
      file: tooLarge ? null : file, name: file.name, size: file.size,
      state: tooLarge ? 'failed' : 'waiting', progress: 0,
      error: tooLarge ? `Larger than ${bytes(session.maxFileBytes)}.` : '',
    });
  }
  renderQueue();
  processQueue();
}

function renderQueue() {
  const container = document.getElementById('upload-queue');
  if (!container) return;
  container.replaceChildren(...queue.map(item => {
    const status = { waiting: 'Queued', approving: 'Waiting for PC approval', sending: item.progress >= 100 ? 'Checking file...' : `${item.progress}%`, sent: 'Received', declined: 'Declined on PC', failed: 'Not received', cancelled: 'Cancelled' }[item.state];
    const cancel = ['sending', 'approving', 'waiting'].includes(item.state)
      ? el('button', { class: 'icon-button', 'aria-label': `Cancel ${item.name}`, onclick: () => {
        if (['sending', 'approving'].includes(item.state)) currentUpload?.abort();
        else { item.state = 'cancelled'; item.file = null; renderQueue(); }
      } }, icon('close', 14)) : null;
    return el('div', { class: 'upload-item' },
      el('span', { class: 'file-label', title: item.name }, item.name),
      el('span', { class: 'cluster meta' }, item.state === 'sent' ? icon('check', 15) : null, status, cancel),
      item.state === 'sending' ? el('progress', { max: 100, value: item.progress, 'aria-label': `Sending ${item.name}` }) : null,
      item.error ? el('p', { class: 'upload-error' }, item.error) : null);
  }));
}

async function processQueue() {
  if (processing) return;
  processing = true;
  try {
    while (queue.some(item => item.state === 'waiting')) {
      const item = queue.find(item => item.state === 'waiting');
      const controller = new AbortController();
      currentUpload = controller;
      item.state = 'approving';
      renderQueue();
      try {
        await sendApproved({ kind: 'file', name: item.name, size: item.size }, controller.signal, headers => {
          item.state = 'sending';
          renderQueue();
          return upload(item, headers, controller.signal);
        });
        item.state = 'sent';
      } catch (error) {
        item.state = error.name === 'DeclinedError' ? 'declined' :
          controller.signal.aborted || error.name === 'AbortError' ? 'cancelled' : 'failed';
        item.error = ['DeclinedError', 'AbortError'].includes(error.name) ? '' : errorMessage(error);
      } finally {
        item.file = null;
        currentUpload = null;
        renderQueue();
      }
    }
  } finally {
    processing = false;
  }
}

function upload(item, headers, signal) {
  if (window.shareMePeer) return uploadPeer(item, headers, signal);
  return new Promise((resolve, reject) => {
    if (signal.aborted) { reject(new DOMException('Cancelled.', 'AbortError')); return; }
    const xhr = new XMLHttpRequest();
    const abort = () => xhr.abort();
    signal.addEventListener('abort', abort, { once: true });
    xhr.onloadend = () => signal.removeEventListener('abort', abort);
    xhr.open('POST', '/api/upload');
    xhr.setRequestHeader('X-Share-Me', '1');
    xhr.setRequestHeader('X-Share-Me-Size', String(item.size));
    for (const [name, value] of Object.entries(headers)) xhr.setRequestHeader(name, value);
    xhr.timeout = 30 * 60 * 1000;
    xhr.upload.onprogress = event => {
      if (event.lengthComputable) {
        item.progress = Math.floor(event.loaded / event.total * 100);
        renderQueue();
      }
    };
    xhr.onload = () => {
      let response;
      try { response = JSON.parse(xhr.responseText); }
      catch { reject(new Error('Unexpected response. Check the PC inbox.')); return; }
      if (xhr.status >= 200 && xhr.status < 300) resolve(response);
      else reject(new Error(response.error || `Upload failed (${xhr.status}).`));
    };
    xhr.onerror = () => reject(new Error('Connection lost. Check the PC inbox before retrying.'));
    xhr.ontimeout = () => reject(new Error('Timed out. Check the PC inbox before retrying.'));
    xhr.onabort = () => reject(new DOMException('Cancelled.', 'AbortError'));
    const form = new FormData();
    form.append('file', item.file);
    xhr.send(form);
  });
}

async function uploadPeer(item, headers, signal) {
  const form = new FormData();
  form.append('file', item.file);
  const response = await window.shareMePeer.fetch('/api/upload', {
    method: 'POST', body: form, signal,
    headers: { 'X-Share-Me': '1', 'X-Share-Me-Size': String(item.size), ...headers },
    onProgress(sent) {
      item.progress = Math.min(99, Math.floor(sent / Math.max(1, item.size) * 100));
      renderQueue();
    },
  });
  item.progress = 100;
  renderQueue();
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || `Transfer failed (${response.status}).`);
  return result;
}

window.addEventListener('beforeunload', event => {
  if (processing || textRequest || window.shareMePeer?.activeSaves()) {
    event.preventDefault();
    event.returnValue = '';
  }
});

initialize();
if (window.shareMePeer) {
  setInterval(refreshOffers, 1500);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshOffers(); });
  window.addEventListener('peer:status', event => {
    let notice = document.getElementById('peer-disconnected');
    if (!notice) {
      notice = el('div', { id: 'peer-disconnected', class: 'notice error', role: 'alert' });
      content.prepend(notice);
    }
    notice.replaceChildren(el('p', {}, event.detail),
      el('button', { class: 'space-top', onclick: () => location.reload() }, 'Reconnect'));
  });
  window.addEventListener('peer:save-error', event => toast(event.detail, true));
}
