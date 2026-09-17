import { el, brand, errorMessage } from './shared.js';
import { connectPC, storedPCs } from './peer-client.js';
import { initializeSaving, acceptOutgoing, activeSaves } from './save-transfer.js';

const root = document.getElementById('app');
const params = new URLSearchParams(location.hash.slice(1));
let room = params.get('room');
const invitation = params.get('pair');
const status = el('p', { class: 'meta space-top', role: 'status' }, 'Connecting...');
root.append(brand(), status);
let transport;
let controller;

window.addEventListener('hashchange', () => location.reload());

async function connect() {
  root.replaceChildren(brand(), status);
  controller?.abort();
  controller = new AbortController();
  try {
    transport = await connectPC({
      room, invitation, signal: controller.signal,
      onStatus(message, connected) {
        status.textContent = message;
        if (!connected && window.shareMePeer) window.dispatchEvent(new CustomEvent('peer:status', { detail: message }));
      },
    });
    window.shareMePeer = {
      ...transport,
      acceptOutgoing: item => acceptOutgoing(transport, item),
      activeSaves,
    };
    initializeSaving().catch(error => {
      window.dispatchEvent(new CustomEvent('peer:save-error', { detail: errorMessage(error) }));
    });
    root.replaceChildren();
    await import('./phone.js');
  } catch (error) {
    status.textContent = errorMessage(error);
    root.append(el('button', { class: 'space-top', onclick: connect }, 'Retry'));
  }
}

if (room) connect();
else storedPCs().then(pcs => {
  if (pcs.length === 1) { room = pcs[0].room; connect(); }
  else if (pcs.length > 1) {
    status.textContent = 'Choose a PC';
    for (const pc of pcs) root.append(el('button', { class: 'space-top', onclick: () => { room = pc.room; connect(); } }, `PC ${pc.room.slice(0, 6)}`));
  } else status.textContent = 'Open Share Me on Windows and scan its QR code.';
}).catch(error => { status.textContent = errorMessage(error); });
