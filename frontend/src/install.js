import { el, icon, errorMessage } from './shared.js';

function setupLink(setup) {
  const parts = typeof setup?.host === 'string' ? setup.host.split('.') : [];
  const validIP = parts.length === 4 && parts.every(part => /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255) &&
    (parts[0] === '10' || parts[0] === '192' && parts[1] === '168' || parts[0] === '172' && Number(parts[1]) >= 16 && Number(parts[1]) <= 31);
  if (!validIP || setup.version !== 1 || setup.port !== 49322 ||
    typeof setup.name !== 'string' || !setup.name.trim() || setup.name.length > 80 ||
    !/^[A-Za-z0-9_-]{43}$/.test(setup.enrollment) ||
    !/^SHA256:[A-Za-z0-9+/]{43}$/.test(setup.fingerprint)) {
    throw new Error('The PC returned an invalid Shortcut configuration.');
  }
  const link = new URL('shortcuts://run-shortcut');
  link.searchParams.set('name', 'Share Me');
  link.searchParams.set('input', 'text');
  const { version, host, port, name, enrollment, fingerprint } = setup;
  link.searchParams.set('text', 'shareme-setup-v1:' + JSON.stringify({ version, host, port, name, enrollment, fingerprint }));
  return link.href;
}

export async function installShortcut() {
  const response = await fetch('/assets/shortcut-install.json', { cache: 'no-store' });
  if (!response.ok) throw new Error('Shortcut installer is unavailable.');
  const manifest = await response.json();
  if (manifest.schemaVersion !== 2 || manifest.transport !== 'ssh-v1' || manifest.name !== 'Share Me') {
    throw new Error('This Shortcut installer is out of date. Reload Share Me.');
  }
  const node = el('dialog', { class: 'shortcut-dialog', 'aria-label': 'Share sheet' });
  let pending;
  const done = el('button', { class: 'icon-button', 'aria-label': 'Close share sheet setup', onclick: () => node.close() }, icon('close', 18));
  node.append(el('header', { class: 'dialog-heading' }, el('h2', {}, 'Share sheet'), done));
  if (manifest.state === 'published' || (manifest.state === 'unpublished' && window.shareMePeer)) {
    const published = manifest.state === 'published';
    const appleLink = /^https:\/\/www\.icloud\.com\/shortcuts\/[a-f0-9]{32}$/i.test(manifest.url);
    const signedFile = manifest.url === '/assets/ShareMe.shortcut';
    if (published && !appleLink && !signedFile) throw new Error('Invalid Shortcut installation link.');
    if (!window.shareMePeer) {
      node.append(el('p', { class: 'space-top' }, 'Open the secure phone link and pair with your PC first.'));
    } else {
      const connectLabel = published ? 'Connect Shortcut' : 'Connect installed Shortcut';
      const feedback = el('p', { class: 'notice error', role: 'alert', hidden: true });
      const handoff = el('div', { class: 'stack space-top', hidden: true });
      const connect = el('button', { onclick: async () => {
        connect.disabled = true;
        connect.textContent = 'Connecting...';
        feedback.hidden = true;
        handoff.hidden = true;
        handoff.replaceChildren();
        const controller = new AbortController();
        pending = controller;
        const timeout = setTimeout(() => controller.abort(new Error('Connecting timed out. Check Share Me on your PC and try again.')), 15000);
        try {
          const response = await window.shareMePeer.fetch('/api/shortcut/setup', {
            method: 'POST', headers: { 'X-Share-Me': '1' }, signal: controller.signal,
          });
          const setup = await response.json();
          controller.signal.throwIfAborted();
          if (!response.ok) throw new Error(setup.error || 'Could not connect the Shortcut. Check Share Me on your PC.');
          const href = setupLink(setup);
          handoff.replaceChildren(
            el('p', {}, `Connect to ${setup.name}.`),
            el('p', { class: 'meta' }, 'If Shortcuts asks, confirm this PC key:'),
            el('code', { class: 'shortcut-fingerprint' }, setup.fingerprint),
            el('div', { class: 'install-actions' }, el('a', { href, rel: 'noreferrer' }, 'Open Shortcuts')));
          handoff.hidden = false;
        } catch (error) {
          if (node.open) {
            feedback.textContent = errorMessage(controller.signal.reason || error);
            feedback.hidden = false;
          }
        } finally {
          clearTimeout(timeout);
          pending = undefined;
          connect.disabled = false;
          connect.textContent = connectLabel;
        }
      } }, connectLabel);
      node.append(
        el('p', { class: 'space-top' }, published ? 'Install once. Then connect to this PC.' : 'The Shortcut is not published yet.'),
        el('div', { class: 'install-actions' }, published ? el('a', {
          href: manifest.url, rel: 'noreferrer', ...(signedFile ? { download: 'Share Me.shortcut' } : {}),
        }, 'Add Shortcut') : null, connect),
        el('p', { class: 'meta space-top' }, published ? 'No editing actions. Files stay encrypted on your network.' :
          'Connect only if you already installed a signed test copy.'),
        feedback, handoff);
    }
  } else if (manifest.state === 'unpublished') {
    node.append(el('p', { class: 'muted space-top' }, 'The Shortcut needs one-time Apple publishing before it can be installed.'),
      el('p', { class: 'meta space-top' }, 'Photos, files, and text work from this page now.'));
  } else {
    throw new Error('Invalid Shortcut installation state.');
  }
  node.addEventListener('close', () => {
    pending?.abort();
    node.remove();
  });
  document.body.append(node);
  node.showModal();
}
