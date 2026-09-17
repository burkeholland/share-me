import './theme.css';
import './style.css';

export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (key.startsWith('on')) node.addEventListener(key.slice(2).toLowerCase(), value);
    else if (key === 'class') node.className = value;
    else if (key === 'text') node.textContent = value;
    else if (key === 'hidden') node.hidden = Boolean(value);
    else if (value !== false && value != null) node.setAttribute(key, String(value));
  }
  for (const child of children.flat()) {
    if (child != null) node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function icon(name, size = 20) {
  const paths = {
    transfer: '<path d="M4 8h14l-4-4M20 16H6l4 4"/>',
    file: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M8 15h8M8 18h5"/>',
    text: '<path d="M4 5h16M4 10h16M4 15h10M4 20h7"/>',
    photo: '<rect x="3" y="3" width="18" height="18" rx="3"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/>',
    copy: '<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/>',
    folder: '<path d="M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
    phone: '<rect x="6" y="2" width="12" height="20" rx="3"/><path d="M10 18h4M10 5h4"/>',
    arrow: '<path d="M12 19V5m-6 6 6-6 6 6"/>',
    check: '<path d="m5 12 4 4L19 6"/>',
    close: '<path d="m6 6 12 12M18 6 6 18"/>',
    chevron: '<path d="m6 9 6 6 6-6"/>',
    link: '<path d="m10 13 4-4m-6 7-1 1a4 4 0 0 1-6-6l5-5a4 4 0 0 1 6 0m0 2 1-1a4 4 0 0 1 6 6l-5 5a4 4 0 0 1-6 0" transform="translate(2 1)"/>',
  };
  const node = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  node.setAttribute('viewBox', '0 0 24 24');
  node.setAttribute('width', size);
  node.setAttribute('height', size);
  node.setAttribute('fill', 'none');
  node.setAttribute('stroke', 'currentColor');
  node.setAttribute('stroke-width', '1.6');
  node.setAttribute('stroke-linecap', 'round');
  node.setAttribute('stroke-linejoin', 'round');
  node.setAttribute('aria-hidden', 'true');
  node.innerHTML = paths[name] || paths.file;
  return node;
}

export function brand() {
  return el('div', { class: 'brand' },
    el('span', { class: 'brand-mark' }, icon('transfer', 24)),
    el('span', {}, 'Share Me'));
}

export function bytes(value) {
  if (!value) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const unit = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 3);
  return `${(value / 1024 ** unit).toLocaleString(undefined, { maximumFractionDigits: unit ? 1 : 0 })} ${units[unit]}`;
}

export function toast(message, danger = false) {
  document.querySelector('.toast')?.remove();
  const node = el('div', { class: `toast${danger ? ' error' : ''}`, role: danger ? 'alert' : 'status' },
    el('span', {}, message),
    el('button', { class: 'icon-button', 'aria-label': 'Dismiss message', onclick: () => node.remove() }, icon('close', 16)));
  const activeDialog = Array.from(document.querySelectorAll('dialog[open]')).at(-1);
  (activeDialog || document.body).append(node);
  if (!danger) setTimeout(() => node.remove(), 4500);
}

export function errorMessage(error) {
  return error instanceof Error ? error.message : String(error);
}
