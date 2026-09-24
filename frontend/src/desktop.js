import { el, icon, brand, bytes, toast, errorMessage } from './shared.js';

const root = document.getElementById('app');
let state;
let filter = 'all';
let refreshing = false;
let itemSignature = '';
let pendingSignature = '';
let approval;
let approvalID;
let networkIP;
let settingsSaving = false;
let settingsInputs = {};
let hideButton;
let networkButton;
let settingsPhones;
let settingsPhoneSignature = '';
let settingsReturnView = 'inbox';
let pairSignature = '';
let sendSignature = '';
let view = 'inbox';

const statusLabel = el('span', {}, 'Starting');
const statusLine = el('div', { class: 'status-line', role: 'status' }, el('span', { class: 'status-dot' }), statusLabel);
const qr = el('div', { class: 'qr-frame' });
const address = el('button', { class: 'quiet icon-button', title: 'Copy phone link', 'aria-label': 'Copy phone link', onclick: () => action(address, 'CopyLink', [], 'Link copied') }, icon('copy', 16));
const toggle = el('button', { class: 'quiet', onclick: () => action(toggle, state?.status.running ? 'Pause' : 'Start', state?.status.running ? [] : [selectedIP()]) }, 'Pause');
const connectionNote = el('span', { class: 'meta' }, 'Local network / not encrypted');
const addPhone = el('button', { class: 'quiet', hidden: true, onclick: () => action(addPhone, 'AddPhone') }, 'Add phone');
const banner = el('div', { class: 'notice error error-banner', role: 'alert', hidden: true });
const list = el('div', { class: 'transfer-list', 'aria-label': 'Received transfers' });
const active = el('div', { class: 'active-transfers', 'aria-live': 'polite' });
const empty = el('div', { class: 'empty-inbox' }, el('div', { class: 'empty-symbol' }, icon('transfer', 32)), el('h2', {}, 'Nothing yet'));
const count = el('span', { class: 'meta' });
const openFolder = el('button', { class: 'icon-button', title: 'Open inbox folder', 'aria-label': 'Open inbox folder', onclick: () => action(openFolder, 'OpenInbox') }, icon('folder'));
const heading = el('h1', { tabindex: -1 }, 'Inbox');
const settingsButton = el('button', { class: 'quiet', disabled: true, 'aria-pressed': false, onclick: showSettings }, 'Settings');
const closeSettingsButton = el('button', {
  class: 'icon-button', hidden: true, 'aria-label': 'Close settings', title: 'Back to transfers',
  onclick: closeSettings,
}, icon('close', 18));
const settingsPanel = el('section', { class: 'settings-panel', hidden: true, 'aria-label': 'Settings' });
const changeView = el('button', { class: 'quiet', hidden: true, onclick: () => {
  view = view === 'inbox' ? 'send' : 'inbox';
  renderView();
} }, 'Send');
const sendPanel = el('section', { class: 'send-panel', hidden: true, 'aria-label': 'Send to phone' });
const tabs = el('div', { class: 'tabs', 'aria-label': 'Filter transfers' });
for (const [value, label] of [['all', 'All'], ['file', 'Files'], ['text', 'Text']]) {
  tabs.append(el('button', {
    'aria-pressed': value === filter,
    onclick(event) {
      filter = value;
      for (const button of tabs.children) button.setAttribute('aria-pressed', button === event.currentTarget);
      renderItems();
    },
  }, label));
}

const inboxToolbar = el('div', { class: 'inbox-toolbar' }, tabs, count);
const inboxSurface = el('section', { class: 'inbox-surface' }, list, empty);
root.append(el('div', { class: 'desktop-shell minimal-desktop' },
  el('aside', { class: 'sidebar' },
    brand(),
    el('section', { class: 'pair-panel', 'aria-label': 'Open on your phone' }, qr,
      el('div', { class: 'pair-caption' }, el('p', { class: 'meta scan-caption' }, 'Scan with Camera'), address), addPhone),
    el('div', { class: 'sidebar-footer' },
      statusLine,
      el('div', { class: 'cluster' }, toggle, settingsButton),
      connectionNote)),
  el('main', { class: 'workspace' },
    el('header', { class: 'workspace-header' }, heading, el('div', { class: 'cluster' }, changeView, openFolder, closeSettingsButton)),
    banner, active,
    inboxToolbar, inboxSurface, sendPanel, settingsPanel)));

function api() {
  if (!window.go?.main?.App) throw new Error('Open ShareMe.exe to use the desktop inbox.');
  return window.go.main.App;
}

function selectedIP() {
  return document.getElementById('network')?.value ||
    networkIP || state?.networks?.[0]?.ip;
}

async function action(button, method, args = [], message) {
  button.disabled = true;
  try {
    await api()[method](...args);
    if (message) toast(message);
    await refresh();
  } catch (error) {
    toast(errorMessage(error), true);
  } finally {
    button.disabled = false;
    if (state) renderStatus();
  }
}

async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    state = await api().GetState();
    renderStatus();
    const items = JSON.stringify(state.items);
    if (items !== itemSignature) {
      itemSignature = items;
      renderItems();
    }
    const pending = JSON.stringify(state.pending);
    if (pending !== pendingSignature) {
      pendingSignature = pending;
      renderPending();
    }
    const pairs = JSON.stringify(state.pairRequests || []);
    if (pairs !== pairSignature) {
      pairSignature = pairs;
      renderPairRequest();
    }
    if (!approval) {
      renderPending();
      renderPairRequest();
    }
    const sendState = JSON.stringify([state.devices, state.outbox]);
    if (sendState !== sendSignature) {
      sendSignature = sendState;
      renderSend();
    }
  } catch (error) {
    banner.hidden = false;
    banner.textContent = errorMessage(error);
  } finally {
    refreshing = false;
  }
}

function renderStatus() {
  const { status, error, qr: image } = state;
  if (state.secure && state.networkIP) networkIP = state.networkIP;
  else if (status.running && status.address) networkIP = new URL(status.address).hostname;
  statusLine.classList.toggle('live', status.running);
  statusLabel.textContent = status.running ? state.secure && !state.serviceConnected ? 'Connecting' : 'Ready' : 'Paused';
  connectionNote.textContent = state.secure ? 'Direct / encrypted' : 'Local network / not encrypted';
  addPhone.hidden = !state.secure;
  settingsButton.disabled = false;
  addPhone.disabled = !status.running;
  if (image && status.running) {
    if (qr.firstElementChild?.getAttribute('src') !== image) {
      qr.replaceChildren(el('img', { src: image, alt: 'Open Share Me on your phone', draggable: false }));
    }
  } else {
    qr.replaceChildren(el('div', { class: 'qr-placeholder' }, icon('phone', 32), 'Paused'));
  }
  address.disabled = !status.running;
  toggle.textContent = status.running ? 'Pause' : 'Resume';
  toggle.disabled = !state.networks?.length;
  openFolder.disabled = !status.inboxDir;
  const peerError = state.secure && status.running && !state.serviceConnected ? state.peerMessage : '';
  banner.hidden = !error && !status.error && !peerError;
  banner.textContent = error || status.error || peerError || '';
  const select = document.getElementById('network');
  if (select) select.disabled = status.running;
  renderSettings();
  renderView();
}

function renderView() {
  const configuring = view === 'settings';
  const sending = view === 'send' && state?.secure;
  heading.textContent = configuring ? 'Settings' : sending ? 'Send' : 'Inbox';
  changeView.textContent = sending ? 'Inbox' : 'Send';
  changeView.hidden = configuring || !state?.secure;
  inboxToolbar.hidden = inboxSurface.hidden = sending || configuring;
  sendPanel.hidden = !sending;
  openFolder.hidden = sending || configuring;
  settingsPanel.hidden = closeSettingsButton.hidden = !configuring;
  settingsButton.setAttribute('aria-pressed', configuring);
}

function selectControl(select) {
  return el('div', { class: 'select-control' }, select, icon('chevron', 16));
}

function renderSend() {
  const previous = document.getElementById('recipient')?.value;
  const devices = state.devices || [];
  const recipient = el('select', { id: 'recipient', 'aria-label': 'Send to', disabled: !devices.length },
    ...devices.map(device => el('option', { value: device.id }, `${device.name}${device.connected ? '' : ' / offline'}`)));
  if (devices.some(device => device.id === previous)) recipient.value = previous;
  const files = el('button', { class: 'primary', disabled: !devices.length, onclick: async () => {
    files.textContent = 'Preparing...';
    await action(files, 'SendFiles', [recipient.value]);
    files.textContent = 'Choose files';
  } }, icon('file', 17), 'Choose files');
  const clipboard = el('button', { disabled: !devices.length, onclick: () => action(clipboard, 'SendClipboardText', [recipient.value]) }, 'Send clipboard text');
  const rows = (state.outbox || []).map(item => {
    const device = devices.find(device => device.id === item.deviceId);
    const remove = el('button', { class: 'icon-button', 'aria-label': `Remove ${item.name}`, onclick: () => action(remove, 'RemoveOutgoing', [item.id]) }, icon('close', 16));
    return el('article', { class: 'transfer-row' }, icon(item.kind === 'text' ? 'text' : 'file'),
      el('div', { class: 'transfer-content stack compact-stack' },
        el('span', { class: 'transfer-name' }, item.name),
        el('span', { class: 'transfer-meta' }, `${device?.name || 'Removed phone'} / ${bytes(item.size)}`)), remove);
  });
  sendPanel.replaceChildren(
    el('div', { class: 'stack' },
      el('label', { for: 'recipient' }, 'Send to'), selectControl(recipient),
      el('div', { class: 'cluster' }, files, clipboard),
      !devices.length ? el('p', { class: 'meta' }, 'Add a phone to send files.') : null),
    el('div', { class: 'transfer-list space-top' }, ...rows));
}

function renderPairRequest() {
  const request = state.pairRequests?.[0];
  if (approvalID?.startsWith('pair:') && approvalID !== `pair:${request?.id}`) {
    approval?.close();
    approval = null;
    approvalID = null;
  }
  if (!request || approval) return;
  const decline = el('button', { autofocus: true }, 'Decline');
  const accept = el('button', { class: 'primary' }, 'Connect');
  const feedback = el('p', { class: 'notice error', role: 'alert', hidden: true });
  const decide = async allow => {
    if (decline.disabled) return;
    decline.disabled = accept.disabled = true;
    try {
      await api().DecidePair(request.id, allow);
      approval?.close();
      approval = null;
      approvalID = null;
      await refresh();
    } catch (error) {
      feedback.textContent = errorMessage(error);
      feedback.hidden = false;
      decline.disabled = accept.disabled = false;
    }
  };
  decline.addEventListener('click', () => decide(false));
  accept.addEventListener('click', () => decide(true));
  approvalID = `pair:${request.id}`;
  approval = dialog('Connect phone?',
    el('div', { class: 'approval-file' }, icon('phone', 28),
      el('div', { class: 'stack compact-stack' }, el('strong', {}, request.name), el('span', { class: 'meta' }, request.source))),
    feedback, el('div', { class: 'cluster dialog-actions' }, decline, accept));
  approval.addEventListener('cancel', event => { event.preventDefault(); decide(false); });
}
function renderSettings() {
  for (const [key, input] of Object.entries(settingsInputs)) {
    if (!settingsSaving) input.checked = Boolean(state.settings?.[key]);
    input.disabled = settingsSaving;
  }
  if (hideButton) hideButton.disabled = !state.trayAvailable;
  if (networkButton) networkButton.textContent = state.status.running ? 'Pause to change network' : 'Start receiving';
  renderSettingsPhones();
}

function renderPending() {
  const pending = state.pending || [];
  active.replaceChildren(...pending.filter(item => item.state !== 'pending').map(item =>
    el('div', { class: 'active-transfer' }, icon(item.kind === 'text' ? 'text' : 'file', 18),
      el('span', { class: 'transfer-name' }, item.name),
      el('span', { class: 'meta' }, item.state === 'scanning' ? 'Checking file...' : 'Receiving...'))));
  const next = pending.find(item => item.state === 'pending');
  if (approvalID && !approvalID.startsWith('pair:') && next?.id !== approvalID) {
    approval?.close();
    approval = null;
    approvalID = null;
  }
  if (!next || approval) return;
  approvalID = next.id;
  const decline = el('button', { autofocus: true }, 'Decline');
  const accept = el('button', { class: 'primary' }, 'Accept');
  const feedback = el('p', { class: 'notice error', role: 'alert', hidden: true });
  const request = next;
  async function decide(allow) {
    if (decline.disabled || accept.disabled) return;
    decline.disabled = accept.disabled = true;
    try {
      await api().Decide(request.id, allow);
      approval?.close();
      approval = null;
      approvalID = null;
      await refresh();
    } catch (error) {
      feedback.textContent = errorMessage(error);
      feedback.hidden = false;
      decline.disabled = accept.disabled = false;
    }
  }
  decline.addEventListener('click', () => decide(false));
  accept.addEventListener('click', () => decide(true));
  approval = dialog(next.kind === 'text' ? 'Receive text?' : 'Receive file?',
    el('div', { class: 'approval-file' },
      el('div', { class: 'transfer-symbol' }, icon(next.kind === 'text' ? 'text' : 'file', 28)),
      el('div', { class: 'stack compact-stack' },
        el('strong', { class: 'transfer-name', title: next.name }, next.name),
        el('span', { class: 'meta' }, [next.size >= 0 ? bytes(next.size) : null, `From ${next.source}`].filter(Boolean).join(' / ')))),
    next.preview ? el('pre', { class: 'approval-preview' }, next.preview) : null,
    feedback, el('div', { class: 'cluster dialog-actions' }, decline, accept));
  approval.addEventListener('cancel', event => {
    event.preventDefault();
    decide(false);
  });
}

function renderItems() {
  const items = (state?.items || []).filter(item => filter === 'all' || item.kind === filter);
  list.replaceChildren(...items.map(item => {
    const isText = item.kind === 'text';
    const type = isText ? 'text' : /^(image|video)\//.test(item.mime) ? 'photo' : 'file';
    const button = el('button', { class: 'icon-button', 'aria-label': isText ? 'Copy received text' : `Details for ${item.name}`, title: isText ? 'Copy' : 'Details' }, icon(isText ? 'copy' : 'file', 17));
    button.addEventListener('click', () => isText ? action(button, 'CopyText', [item.id], 'Copied') : showItem(item));
    const content = el('button', { class: 'transfer-content item-content', onclick: () => showItem(item), 'aria-label': isText ? 'Read received text' : `Read details for ${item.name}` },
      el('span', { class: 'transfer-name' }, item.name),
      isText ? el('span', { class: 'transfer-preview' }, item.text) : null,
      el('span', { class: 'transfer-meta' }, bytes(item.size),
        el('time', { datetime: item.createdAt }, new Date(item.createdAt).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }))));
    return el('article', { class: `transfer-row kind-${item.kind}` }, el('div', { class: 'transfer-symbol' }, icon(type)), content, button);
  }));
  empty.hidden = items.length > 0;
  count.textContent = items.length ? `${items.length}${state.items.length === 100 ? ' latest' : ''}` : '';
}

function dialog(title, ...content) {
  const node = el('dialog', { 'aria-label': title },
    el('header', { class: 'dialog-heading' }, el('h2', {}, title)), ...content.filter(Boolean));
  node.addEventListener('close', () => node.remove());
  document.body.append(node);
  node.showModal();
  return node;
}

function showItem(item) {
  const close = el('button', {}, 'Done');
  const node = dialog(item.kind === 'text' ? 'Text' : item.name,
    item.kind === 'text' ? el('pre', {}, item.text) : el('p', { class: 'mono space-top' }, item.path),
    el('div', { class: 'cluster dialog-actions' }, close));
  close.addEventListener('click', () => node.close());
}

function showRenamePhone(id) {
  const device = state.devices?.find(phone => phone.id === id);
  if (!device) {
    toast('This phone is no longer paired.', true);
    return;
  }
  let saving = false;
  const name = el('input', { id: 'phone-name', value: device.name, maxlength: 80, autofocus: true, autocomplete: 'off' });
  const feedback = el('p', { id: 'rename-error', class: 'notice error', role: 'alert', hidden: true });
  const cancel = el('button', { type: 'button', onclick: () => node.close() }, 'Cancel');
  const save = el('button', { type: 'submit', class: 'primary', disabled: true }, 'Save');
  const close = el('button', {
    class: 'icon-button', 'aria-label': 'Close rename', title: 'Close',
    onclick: () => node.close(),
  }, icon('close', 18));
  const form = el('form', { class: 'space-top', novalidate: true },
    el('div', { class: 'stack' }, el('label', { for: 'phone-name' }, 'Phone name'), name, feedback),
    el('div', { class: 'cluster dialog-actions' }, cancel, save));
  const node = dialog('Rename phone', form);
  node.classList.add('rename-dialog');
  node.querySelector('.dialog-heading').append(close);
  name.addEventListener('input', () => {
    feedback.hidden = true;
    name.removeAttribute('aria-invalid');
    name.removeAttribute('aria-describedby');
    save.disabled = saving || name.value.trim() === device.name;
  });
  node.addEventListener('cancel', event => {
    if (saving) event.preventDefault();
  });
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (saving || name.value.trim() === device.name) return;
    const value = name.value.trim();
    const validationError = !value ? 'Enter a name.' :
      new TextEncoder().encode(value).length > 80 ? 'That name is too long. Try a shorter one.' : '';
    if (validationError) {
      feedback.textContent = validationError;
      feedback.hidden = false;
      name.setAttribute('aria-invalid', 'true');
      name.setAttribute('aria-describedby', 'rename-error');
      name.focus();
      return;
    }
    saving = true;
    name.disabled = cancel.disabled = close.disabled = save.disabled = true;
    save.textContent = 'Saving...';
    node.setAttribute('aria-busy', 'true');
    feedback.hidden = true;
    try {
      await api().RenamePhone(id, value);
      await refresh();
      node.close();
      toast('Phone renamed');
    } catch (error) {
      feedback.textContent = errorMessage(error);
      feedback.hidden = false;
    } finally {
      saving = false;
      name.disabled = cancel.disabled = close.disabled = false;
      save.disabled = name.value.trim() === device.name;
      save.textContent = 'Save';
      node.removeAttribute('aria-busy');
    }
  });
  name.focus();
  name.select();
}

function showSettings() {
  if (view === 'settings') return;
  settingsReturnView = view;
  settingsInputs = {};
  const preferenceRows = [
    ['startWithWindows', 'Start with Windows'],
    ['startMinimized', 'Start minimized to tray'],
    ['minimizeToTray', 'Minimize button hides to tray'],
  ].map(([key, label]) => {
    const input = el('input', { type: 'checkbox', checked: Boolean(state.settings?.[key]) });
    settingsInputs[key] = input;
    input.addEventListener('change', async () => {
      const previous = { ...state.settings };
      const next = Object.fromEntries(Object.entries(settingsInputs).map(([name, control]) => [name, control.checked]));
      settingsSaving = true;
      renderSettings();
      try {
        await api().SetDesktopSettings(next);
        state.settings = next;
      } catch (error) {
        state.settings = previous;
        toast(errorMessage(error), true);
      } finally {
        settingsSaving = false;
        renderSettings();
      }
    });
    return el('label', { class: 'setting-toggle' }, el('span', {}, label), input);
  });
  hideButton = el('button', { disabled: !state.trayAvailable, onclick: async () => {
    try {
      await api().MinimizeToTray();
      closeSettings();
    } catch (error) {
      toast(errorMessage(error), true);
    }
  } }, 'Minimize to tray');
  const select = el('select', { id: 'network', disabled: state.status.running, 'aria-label': 'Network' },
    ...(state.networks || []).map(network => el('option', { value: network.ip }, `${network.name} / ${network.ip}`)));
  if (networkIP) select.value = networkIP;
  networkButton = el('button', {}, state.status.running ? 'Pause to change network' : 'Start receiving');
  networkButton.addEventListener('click', () => {
    action(networkButton, state.status.running ? 'Pause' : 'Start', state.status.running ? [] : [select.value]);
  });
  settingsPhoneSignature = '';
  settingsPhones = el('div', { class: 'settings-phones' });
  settingsPanel.replaceChildren(
    el('section', { class: 'settings-group', 'aria-label': 'Window' },
      el('h2', {}, 'Window'),
      el('div', { class: 'settings-preferences' }, ...preferenceRows),
      el('div', { class: 'settings-tray' },
        el('p', { class: 'meta' }, 'Close keeps receiving. Quit from the tray.'), hideButton)),
    state.secure ? el('section', { class: 'settings-group', 'aria-label': 'Paired phones' }, el('h2', {}, 'Phones'), settingsPhones) : null,
    el('section', { class: 'settings-group', 'aria-label': 'Network' },
      el('h2', {}, el('label', { for: 'network' }, 'Network')), selectControl(select),
      el('div', { class: 'cluster' }, networkButton)),
    el('section', { class: 'settings-group', 'aria-label': 'Received files' },
      el('h2', {}, 'Save to'),
      el('div', { class: 'settings-folder' }, el('span', { class: 'mono' }, state.status.inboxDir),
        el('button', { class: 'icon-button', 'aria-label': 'Open save folder', title: 'Open folder', onclick: event => action(event.currentTarget, 'OpenInbox') }, icon('folder'))),
      el('details', { class: 'settings-details' },
        el('summary', {}, 'Safety & connection help'),
        el('div', { class: 'stack meta' },
          el('p', {}, 'Windows approval required. Executable files blocked. Defender scans before saving.'),
          el('p', {}, state.secure ? '2 GB per file. Direct encrypted transfers.' : '2 GB per file. Trusted networks only; HTTP is not encrypted.'),
          el('p', {}, 'Cannot connect? Allow Share Me through Windows Firewall on Private networks.')))));
  view = 'settings';
  renderSettings();
  renderView();
  settingsPanel.scrollTop = 0;
  heading.focus({ preventScroll: true });
}

function renderSettingsPhones() {
  if (!settingsPhones) return;
  const devices = state.devices || [];
  const signature = JSON.stringify(devices.map(({ id, name }) => [id, name]));
  if (signature === settingsPhoneSignature) return;
  settingsPhoneSignature = signature;
  const existing = new Map(Array.from(settingsPhones.children, row => [row.dataset.deviceId, row]));
  settingsPhones.replaceChildren(...devices.map(device => {
    let row = existing.get(device.id);
    if (!row) {
      const name = el('span', { class: 'settings-phone-name' });
      const rename = el('button', { 'aria-haspopup': 'dialog', onclick: () => showRenamePhone(device.id) }, 'Rename');
      const forget = el('button', { class: 'quiet', onclick: () => action(forget, 'RevokePhone', [device.id]) }, 'Forget');
      row = el('div', { class: 'settings-phone', role: 'group', 'data-device-id': device.id }, name, el('div', { class: 'cluster' }, rename, forget));
    }
    const name = row.querySelector('.settings-phone-name');
    name.textContent = name.title = device.name;
    row.setAttribute('aria-label', device.name);
    return row;
  }));
  if (!devices.length) settingsPhones.append(el('p', { class: 'meta' }, 'No phones paired.'));
}

function closeSettings() {
  view = settingsReturnView;
  renderView();
  settingsButton.focus({ preventScroll: true });
}

document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && view === 'settings' && !document.querySelector('dialog[open]')) {
    event.preventDefault();
    closeSettings();
  }
});

window.runtime?.EventsOnMultiple?.('inbox:changed', refresh, -1);
window.runtime?.EventsOnMultiple?.('transfer:error', message => toast(message, true), -1);
refresh();
setInterval(refresh, 1000);
