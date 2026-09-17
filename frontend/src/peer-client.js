import { createPeerFetch } from './peer-http.js';

const encode = new TextEncoder();
const decode = new TextDecoder();
const idPattern = /^[a-f0-9]{32}$/;
const base64Pattern = /^[A-Za-z0-9_-]+$/;

export function unbase64(value, length) {
  if (typeof value !== 'string' || !base64Pattern.test(value)) throw new Error('Invalid connection credential');
  const data = Uint8Array.from(atob(value.replaceAll('-', '+').replaceAll('_', '/')), c => c.charCodeAt(0));
  if (length !== undefined && data.length !== length) throw new Error('Invalid connection credential');
  return data;
}

export function base64(data) {
  return btoa(Array.from(new Uint8Array(data), c => String.fromCharCode(c)).join('')).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
}

export function importSecret(raw) {
  return crypto.subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt', 'decrypt']);
}

function aad(room, sid, kid, kind) {
  return encode.encode(`ShareMe signal v1\n${room}\n${sid}\n${kid}\n${kind}`);
}

export async function seal(key, room, sid, kid, kind, payload) {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const data = await crypto.subtle.encrypt({ name: 'AES-GCM', iv, additionalData: aad(room, sid, kid, kind) }, key, encode.encode(JSON.stringify(payload)));
  return { type: 'signal', sid, kid, kind, iv: base64(iv), data: base64(data) };
}

export async function openEnvelope(key, room, sid, kid, message) {
  if (message.sid !== sid || message.kid !== kid || message.kind !== 'answer') throw new Error('Connection verification failed');
  const plain = await crypto.subtle.decrypt({
    name: 'AES-GCM', iv: unbase64(message.iv, 12), additionalData: aad(room, sid, kid, 'answer'),
  }, key, unbase64(message.data));
  const value = JSON.parse(decode.decode(plain));
  if (typeof value.sdp !== 'string' || value.sdp.length > 24000) throw new Error('Invalid PC connection response');
  return value;
}

async function database() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open('share-me-paired-pcs', 1);
    request.onupgradeneeded = () => request.result.createObjectStore('pcs', { keyPath: 'room' });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(new Error('Allow website storage to remember this phone.'));
  });
}

export async function storedPC(room) {
  const db = await database();
  try {
    return await new Promise((resolve, reject) => {
      const request = db.transaction('pcs').objectStore('pcs').get(room);
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(new Error('Could not read the saved connection.'));
    });
  } finally { db.close(); }
}

export async function storedPCs() {
  const db = await database();
  try {
    return await new Promise((resolve, reject) => {
      const request = db.transaction('pcs').objectStore('pcs').getAll();
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(new Error('Could not read saved PCs.'));
    });
  } finally { db.close(); }
}

async function savePC(value) {
  const db = await database();
  try {
    await new Promise((resolve, reject) => {
      const transaction = db.transaction('pcs', 'readwrite');
      transaction.objectStore('pcs').put(value);
      transaction.oncomplete = () => resolve();
      transaction.onabort = transaction.onerror = () => reject(new Error('Could not save pairing. Allow website storage and try again.'));
    });
  } finally { db.close(); }
}

export async function connectPC({ room, invitation, onStatus, signal }) {
  if (!idPattern.test(room) || !isSecureContext || !crypto.subtle || !window.RTCPeerConnection) {
    throw new Error('Open the secure Share Me link in a supported browser.');
  }
  const saved = invitation ? null : await storedPC(room);
  if (!invitation && (!saved || !idPattern.test(saved.id) || !(saved.key instanceof CryptoKey))) {
    throw new Error('Choose Add phone on your PC, then scan its QR code.');
  }
  const kid = invitation ? 'pair' : saved.id;
  const key = invitation ? await importSecret(unbase64(invitation, 32)) : saved.key;
  const connection = new RTCPeerConnection({ iceServers: [], iceTransportPolicy: 'all' });
  const control = connection.createDataChannel('shareme.control', { ordered: true });
  const socket = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/signal/${room}`);
  let sid;
  let settled = false;
  let authenticated = false;
  let closed = false;
  let answerReceived = false;
  let pairingSaved = false;
  let messageChain = Promise.resolve();
  let resolveReady;
  let rejectReady;
  const ready = new Promise((resolve, reject) => { resolveReady = resolve; rejectReady = reject; });
  const timeout = setTimeout(() => fail(new Error('Connection could not be verified. Keep the PC open, or pair again.')), 120000);
  const abort = () => fail(new DOMException('Cancelled.', 'AbortError'));
  signal?.addEventListener('abort', abort, { once: true });
  if (signal?.aborted) abort();
  function fail(error) {
    if (closed) return;
    closed = true;
    clearTimeout(timeout);
    socket.close();
    connection.close();
    signal?.removeEventListener('abort', abort);
    if (!settled) { settled = true; rejectReady(error); }
    else onStatus?.(error.message, false);
  }
  control.onmessage = event => {
    messageChain = messageChain.then(async () => {
      if (typeof event.data !== 'string' || event.data.length > 2048) throw new Error('Invalid PC handshake');
      const message = JSON.parse(event.data);
      if (message.type === 'paired' && kid === 'pair' && !authenticated && !pairingSaved) {
        if (!idPattern.test(message.id) || typeof message.name !== 'string') throw new Error('Invalid pairing response');
        const deviceKey = await importSecret(unbase64(message.secret, 32));
        await savePC({ room, id: message.id, name: message.name, key: deviceKey });
        pairingSaved = true;
        control.send(JSON.stringify({ type: 'paired-ack' }));
      } else if (message.type === 'ready' && !authenticated && (kid !== 'pair' || pairingSaved)) {
        authenticated = true;
        clearTimeout(timeout);
        socket.close();
        history.replaceState(null, '', `${location.pathname}#room=${room}`);
        settled = true;
        onStatus?.('Connected', true);
        resolveReady({
          fetch: createPeerFetch(connection),
          close: () => fail(new Error('Disconnected')),
          connection, room,
        });
      } else if (message.type === 'error') {
        throw new Error(typeof message.message === 'string' ? message.message : 'Connection declined on PC');
      } else throw new Error('Unexpected PC handshake');
    }).catch(fail);
  };
  control.onopen = () => onStatus?.(invitation ? 'Approve this phone on your PC' : 'Verifying connection...', false);
  control.onerror = () => fail(new Error('Direct connection failed. Reopen Share Me to reconnect.'));
  control.onclose = () => fail(new Error('PC disconnected. Reopen Share Me to reconnect.'));
  connection.onconnectionstatechange = () => {
    if (['failed', 'closed'].includes(connection.connectionState)) fail(new Error('Direct connection failed. Check that both devices are on the same network.'));
  };
  socket.onmessage = event => {
    messageChain = messageChain.then(async () => {
      if (authenticated || closed) return;
      if (typeof event.data !== 'string' || event.data.length > 32768) throw new Error('Invalid connection-service response');
      const message = JSON.parse(event.data);
      if (message.type === 'challenge') {
        socket.send(JSON.stringify({ type: 'guest' }));
      } else if (message.type === 'guest-ready' && !sid) {
        if (!idPattern.test(message.sid)) throw new Error('Invalid connection session');
        sid = message.sid;
        onStatus?.('Connecting directly...', false);
        const offer = await connection.createOffer();
        await connection.setLocalDescription(offer);
        await new Promise((resolve, reject) => {
          if (connection.iceGatheringState === 'complete') { resolve(); return; }
          const timer = setTimeout(() => { connection.removeEventListener('icegatheringstatechange', change); reject(new Error('Could not find a direct network route.')); }, 15000);
          const change = () => {
            if (connection.iceGatheringState === 'complete') {
              clearTimeout(timer);
              connection.removeEventListener('icegatheringstatechange', change);
              resolve();
            }
          };
          connection.addEventListener('icegatheringstatechange', change);
        });
        const name = /iPhone/.test(navigator.userAgent) ? 'iPhone' :
          /iPad/.test(navigator.userAgent) || navigator.maxTouchPoints > 1 && /Mac/.test(navigator.userAgent) ? 'iPad' : 'Browser';
        const envelope = await seal(key, room, sid, kid, 'offer', { sdp: connection.localDescription.sdp, name });
        if (socket.readyState !== WebSocket.OPEN) throw new Error('Connection service disconnected');
        socket.send(JSON.stringify(envelope));
      } else if (message.type === 'signal' && sid && !answerReceived) {
        const answer = await openEnvelope(key, room, sid, kid, message);
        answerReceived = true;
        await connection.setRemoteDescription({ type: 'answer', sdp: answer.sdp });
      } else if (message.type === 'error') {
        throw new Error(typeof message.message === 'string' ? message.message : 'PC unavailable');
      } else throw new Error('Unexpected connection-service response');
    }).catch(fail);
  };
  socket.onerror = () => { if (!authenticated) fail(new Error('Connection service is unavailable')); };
  socket.onclose = () => { if (!authenticated && !closed) fail(new Error('PC is offline or the connection was refused.')); };
  return ready;
}
