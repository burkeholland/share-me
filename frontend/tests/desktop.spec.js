import { test as base, expect } from '@playwright/test';
import { serveDesktop } from './desktop-fixture.js';

const test = base.extend({
  desktop: async ({ page }, use) => {
    const state = {
      secure: true, serviceConnected: true, networkIP: '192.168.1.2',
      status: { running: true, address: 'https://example.workers.dev/#invitation', inboxDir: 'C:\\Users\\Example\\Downloads\\Share Me' },
      networks: [{ name: 'Ethernet', ip: '192.168.1.2' }, { name: 'Wi-Fi', ip: '192.168.1.3' }],
      items: [], pending: [], pairRequests: [], outbox: [],
      devices: [{ id: 'phone-1', name: 'iPhone', connected: false }, { id: 'phone-2', name: 'iPad', connected: true }],
      settings: { startWithWindows: false, startMinimized: false, closeToTray: true, minimizeToTray: true },
      trayAvailable: true,
    };
    const calls = [];
    const rename = { error: '', wait: null };
    await page.exposeFunction('desktopState', () => state);
    await page.exposeFunction('desktopAction', async (method, args) => {
      calls.push([method, ...args]);
      if (method === 'Pause') state.status.running = false;
      if (method === 'Start') {
        state.status.running = true;
        state.networkIP = args[0];
      }
      if (method === 'RenamePhone') {
        if (rename.wait) await rename.wait;
        if (rename.error) throw new Error(rename.error);
        const device = state.devices.find(device => device.id === args[0]);
        if (!device) throw new Error('This phone is no longer paired.');
        device.name = args[1];
      }
      if (method === 'RevokePhone') state.devices = state.devices.filter(device => device.id !== args[0]);
      if (method === 'Decide') state.pending = [];
      if (method === 'DecidePair') {
        if (args[1]) state.devices.push({ id: args[0], name: 'New phone', connected: true });
        state.pairRequests = [];
      }
      if (method === 'SetDesktopSettings') state.settings = args[0];
    });
    await page.addInitScript(() => {
      const App = { GetState: () => window.desktopState() };
      for (const method of ['Pause', 'Start', 'RenamePhone', 'RevokePhone', 'Decide', 'DecidePair',
        'SetDesktopSettings', 'MinimizeToTray', 'SendFiles', 'SendClipboardText', 'CopyLink', 'OpenInbox', 'ShowWindowMenu']) {
        App[method] = (...args) => window.desktopAction(method, args);
      }
      window.go = { main: { App } };
      let maximized = false;
      window.runtime = {
        WindowMinimise: () => window.desktopAction('WindowMinimise', []),
        WindowToggleMaximise: () => {
          maximized = !maximized;
          window.desktopAction('WindowToggleMaximise', []);
          window.dispatchEvent(new Event('resize'));
        },
        WindowIsMaximised: async () => maximized,
        Quit: () => window.desktopAction('Quit', []),
      };
    });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.setViewportSize({ width: 960, height: 640 });
    await serveDesktop(page, 'http://shareme.test/');
    await expect(page.getByRole('status')).toHaveText('Ready');
    await use({ state, calls, rename });
    expect(errors).toEqual([]);
  },
});

test.beforeEach(({}, testInfo) => {
  test.skip(testInfo.project.name !== 'chromium', 'Desktop app uses Windows WebView2');
});

async function expectAlignedSelect(page, label) {
  const select = page.getByRole('combobox', { name: label, exact: true });
  const geometry = await select.evaluate(node => {
    const box = node.getBoundingClientRect();
    const arrow = node.nextElementSibling.getBoundingClientRect();
    const styles = getComputedStyle(node);
    return {
      centerDelta: Math.abs(box.y + box.height / 2 - arrow.y - arrow.height / 2),
      inset: box.right - arrow.right,
      expectedInset: parseFloat(getComputedStyle(node.parentElement).getPropertyValue('--space-4')),
      padding: parseFloat(styles.paddingRight),
      arrowWidth: arrow.width,
      appearance: styles.appearance,
    };
  });
  expect(geometry.centerDelta).toBeLessThanOrEqual(0.5);
  expect(geometry.inset).toBeCloseTo(geometry.expectedInset, 0);
  expect(geometry.padding).toBeGreaterThan(geometry.inset + geometry.arrowWidth);
  expect(geometry.appearance).toBe('none');
}

async function expectUnclippedFocus(control) {
  await control.focus();
  await control.evaluate(node => node.scrollIntoView({ block: 'center' }));
  const geometry = await control.evaluate(node => {
    const style = getComputedStyle(node);
    const extent = parseFloat(style.outlineWidth) + parseFloat(style.outlineOffset);
    const box = node.getBoundingClientRect();
    const clippedBy = [];
    for (let ancestor = node.parentElement; ancestor; ancestor = ancestor.parentElement) {
      const css = getComputedStyle(ancestor);
      const clip = ancestor.getBoundingClientRect();
      const left = clip.left + ancestor.clientLeft;
      const top = clip.top + ancestor.clientTop;
      const clippedX = css.overflowX !== 'visible' &&
        (box.left - extent < left || box.right + extent > left + ancestor.clientWidth);
      const clippedY = css.overflowY !== 'visible' &&
        (box.top - extent < top || box.bottom + extent > top + ancestor.clientHeight);
      if (clippedX || clippedY) clippedBy.push(ancestor.className || ancestor.tagName);
    }
    return { focused: node.matches(':focus-visible'), extent, clippedBy };
  });
  expect(geometry.focused).toBe(true);
  expect(geometry.extent).toBeGreaterThan(0);
  expect(geometry.clippedBy).toEqual([]);
}

test.describe('dropdown focus outlines', () => {
  test.use({ deviceScaleFactor: 1.5 });

  for (const width of [800, 960]) {
    for (const queued of [0, 20]) {
      test(`remain visible at ${width}px with ${queued} queued transfers`, async ({ page, desktop }, testInfo) => {
        await page.setViewportSize({ width, height: 560 });
        desktop.state.outbox = Array.from({ length: queued }, (_, i) => ({
          id: `file-${i}`, deviceId: 'phone-1', name: `Queued file ${i + 1}.txt`, kind: 'file', size: 16,
        }));
        await page.getByRole('button', { name: 'Send', exact: true }).click();
        await expect(page.locator('.send-panel .transfer-row')).toHaveCount(queued);
        await page.keyboard.press('Tab');
        const recipient = page.getByRole('combobox', { name: 'Send to', exact: true });
        await expectUnclippedFocus(recipient);
        await expectAlignedSelect(page, 'Send to');
        expect((await recipient.boundingBox()).x).toBe((await page.getByRole('heading', { name: 'Send', exact: true }).boundingBox()).x);
        const send = page.getByRole('region', { name: 'Send to phone' });
        expect(await send.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
        await page.screenshot({ path: testInfo.outputPath('recipient-focus.png') });
        if (queued) {
          const last = page.getByRole('button', { name: `Remove Queued file ${queued}.txt`, exact: true });
          await last.scrollIntoViewIfNeeded();
          await expect(last).toBeInViewport();
          expect(await send.evaluate(node => node.scrollTop)).toBeGreaterThan(0);
        }
        await page.getByRole('button', { name: 'Settings', exact: true }).click();
        await page.getByRole('button', { name: 'Pause', exact: true }).click();
        await page.keyboard.press('Tab');
        const network = page.getByRole('combobox', { name: 'Network', exact: true });
        await expectUnclippedFocus(network);
        await expectAlignedSelect(page, 'Network');
        await page.screenshot({ path: testInfo.outputPath('network-focus.png') });
      });
    }
  }
});

test('hosting URL is hidden, copy link remains, and recipient arrow is inset and centered', async ({ page, desktop }, testInfo) => {
  await expect(page.locator('body')).not.toContainText('workers.dev');
  await page.getByRole('button', { name: 'Copy phone link' }).click();
  expect(desktop.calls).toContainEqual(['CopyLink']);
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expectAlignedSelect(page, 'Send to');
  const recipient = page.getByRole('combobox', { name: 'Send to' });
  await recipient.focus();
  await page.keyboard.press('ArrowDown');
  await expect(recipient).toHaveValue('phone-2');
  await page.getByRole('button', { name: 'Choose files', exact: true }).click();
  expect(desktop.calls).toContainEqual(['SendFiles', 'phone-2']);
  await page.screenshot({ path: testInfo.outputPath('desktop-send.png') });
});

for (const colorScheme of ['light', 'dark']) {
  test(`settings fills the workspace at minimum size in ${colorScheme} mode`, async ({ page, desktop }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    await page.reload();
    await page.setViewportSize({ width: 800, height: 560 });
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    await page.getByRole('combobox', { name: 'Send to' }).selectOption('phone-2');
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    const panel = page.getByRole('region', { name: 'Settings', exact: true });
    await expect(panel).toBeVisible();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeFocused();
    await expect(page.getByRole('region', { name: 'Send to phone' })).toBeHidden();
    expect(await panel.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
    expect(await panel.evaluate(node => node.clientWidth)).toBeGreaterThan(450);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`settings-${colorScheme}-top.png`) });
    await page.getByRole('combobox', { name: 'Network', exact: true }).scrollIntoViewIfNeeded();
    await expectAlignedSelect(page, 'Network');
    await page.getByText('Safety & connection help', { exact: true }).click();
    await expect(panel).toContainText('Defender scans before saving.');
    await page.getByRole('button', { name: 'Open save folder' }).click();
    expect(desktop.calls).toContainEqual(['OpenInbox']);
    await page.screenshot({ path: testInfo.outputPath(`settings-${colorScheme}-bottom.png`) });
    const close = page.getByRole('button', { name: 'Close settings', exact: true });
    await expect(close).toBeInViewport();
    await close.click();
    await expect(panel).toBeHidden();
    await expect(page.getByRole('combobox', { name: 'Send to' })).toHaveValue('phone-2');
    await expect(page.getByRole('button', { name: 'Settings', exact: true })).toBeFocused();
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    expect(await panel.evaluate(node => node.scrollTop)).toBe(0);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('heading', { name: 'Send', exact: true })).toBeVisible();
    await expect(panel).toBeHidden();
  });
}

// Windows moves the window, so this covers what the page owes Wails and the user: the drag
// marking, the caption buttons, the window menu request, and following a dialog.
test('the title bar marks its drag area and its buttons and menu work, even over a dialog', async ({ page, desktop }) => {
  await page.setViewportSize({ width: 800, height: 560 });
  const drag = node => getComputedStyle(node).getPropertyValue('--wails-draggable').trim();
  const titlebar = page.locator('.titlebar');
  const controls = page.getByRole('group', { name: 'Window', exact: true });
  expect(await titlebar.evaluate(drag)).toBe('drag');
  expect(await controls.evaluate(drag)).toBe('no-drag');
  expect(await controls.getByRole('button').evaluateAll(buttons => buttons.map(button => button.tabIndex))).toEqual([-1, -1, -1]);
  // The page's own buttons must sit clear of the caption buttons above them.
  const bar = await controls.boundingBox();
  expect(bar.x + bar.width).toBe(800);
  expect(bar.y).toBe(0);
  for (const name of ['Send', 'Open inbox folder']) {
    expect((await page.getByRole('button', { name, exact: true }).boundingBox()).y).toBeGreaterThanOrEqual(bar.height + 8);
  }
  await page.getByRole('heading', { name: 'Inbox', exact: true }).focus();
  await controls.getByRole('button', { name: 'Minimize', exact: true }).click();
  await controls.getByRole('button', { name: 'Maximize', exact: true }).click();
  await expect(controls.getByRole('button', { name: 'Restore', exact: true })).toHaveAttribute('title', 'Restore');
  await expect(page.getByRole('heading', { name: 'Inbox', exact: true })).toBeFocused();
  await titlebar.dblclick({ position: { x: 300, y: 12 } });
  await expect(controls.getByRole('button', { name: 'Maximize', exact: true })).toBeVisible();
  expect(desktop.calls).toEqual([['WindowMinimise'], ['WindowToggleMaximise'], ['WindowToggleMaximise']]);
  // A right-click asks Windows for the window menu instead of showing the browser's.
  expect(await titlebar.evaluate(node => node.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true })))).toBe(false);
  await controls.getByRole('button', { name: 'Minimize', exact: true }).click({ button: 'right' });
  expect(desktop.calls.slice(3)).toEqual([['ShowWindowMenu'], ['ShowWindowMenu']]);
  // Forced colors replace the hover tints, so a hovered caption button shows the system highlight.
  await page.emulateMedia({ forcedColors: 'active' });
  const highlight = await page.evaluate(() => {
    const probe = document.body.appendChild(document.createElement('div'));
    probe.style.cssText = 'forced-color-adjust: none; background: Highlight';
    const color = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return color;
  });
  for (const name of ['Minimize', 'Close']) {
    const button = controls.getByRole('button', { name, exact: true });
    await button.hover();
    await expect(button).toHaveCSS('background-color', highlight);
  }
  await page.emulateMedia({ forcedColors: 'none' });
  // A modal dialog makes the page inert, so the title bar has to follow the dialog.
  desktop.state.pending = [{ id: 'incoming-1', kind: 'text', state: 'pending', name: 'Text', size: 8, source: 'iPhone', preview: 'Hello PC' }];
  const dialog = page.getByRole('dialog', { name: 'Receive text?' });
  await expect(dialog).toBeVisible();
  expect(await titlebar.evaluate(drag)).toBe('drag');
  expect(await titlebar.evaluate(node => node.getBoundingClientRect().toJSON())).toMatchObject({ x: 0, y: 0, width: 800, height: 44 });
  await dialog.getByRole('button', { name: 'Close', exact: true }).click();
  expect(desktop.calls).toContainEqual(['Quit']);
  await dialog.getByRole('button', { name: 'Decline', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.locator('.desktop-shell > .titlebar')).toBeVisible();
  await controls.getByRole('button', { name: 'Minimize', exact: true }).click();
  expect(desktop.calls.filter(call => call[0] === 'WindowMinimise')).toHaveLength(2);
});

test('the Maximize button ignores a late answer about an earlier window size', async ({ page, desktop }) => {
  const button = page.getByRole('group', { name: 'Window', exact: true }).getByRole('button').nth(1);
  const resizeAndCount = () => page.evaluate(async () => {
    window.dispatchEvent(new Event('resize'));
    await new Promise(requestAnimationFrame);
    await new Promise(requestAnimationFrame);
    return window.answers.length;
  });
  await page.evaluate(() => {
    window.answers = [];
    window.runtime.WindowIsMaximised = () => new Promise(resolve => window.answers.push(resolve));
  });
  expect(await resizeAndCount()).toBe(1);
  expect(await resizeAndCount()).toBe(2);
  await page.evaluate(() => window.answers[1](true));
  await expect(button).toHaveAccessibleName('Restore');
  await page.evaluate(async () => {
    window.answers[0](false);
    await new Promise(requestAnimationFrame);
  });
  await expect(button).toHaveAccessibleName('Restore');
});

test('network controls track sidebar pause and paired phone actions remain available', async ({ page, desktop }) => {
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect(page.getByRole('combobox', { name: 'Network', exact: true })).toBeDisabled();
  await page.getByRole('button', { name: 'Pause', exact: true }).click();
  const network = page.getByRole('combobox', { name: 'Network', exact: true });
  await expect(network).toBeEnabled();
  await network.selectOption('192.168.1.3');
  await page.getByRole('button', { name: 'Start receiving', exact: true }).click();
  expect(desktop.calls).toContainEqual(['Start', '192.168.1.3']);
  await expect(network).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Pause to change network' })).toBeVisible();
  await page.getByRole('group', { name: 'iPhone', exact: true }).getByRole('button', { name: 'Rename', exact: true }).click();
  await page.getByRole('textbox', { name: 'Phone name' }).fill('My phone');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.getByRole('group', { name: 'My phone', exact: true })).toBeVisible();
  await page.getByRole('group', { name: 'My phone', exact: true }).getByRole('button', { name: 'Forget', exact: true }).click();
  await expect(page.getByRole('group', { name: 'My phone', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Forget', exact: true }).click();
  await expect(page.getByText('No phones paired.', { exact: true })).toBeVisible();
});

test('approval dialogs preserve a rename draft and add phones without leaving Settings', async ({ page, desktop }) => {
  // The title bar belongs to the dialog on top, and returns to the page when the last one closes.
  const barIn = name => page.getByRole('dialog', { name }).locator('> .titlebar');
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByRole('group', { name: 'iPhone', exact: true }).getByRole('button', { name: 'Rename', exact: true }).click();
  await page.getByRole('textbox', { name: 'Phone name' }).fill('Unfinished rename');
  desktop.state.pending = [{ id: 'incoming-1', kind: 'text', state: 'pending', name: 'Text', size: 8, source: 'iPhone', preview: 'Hello PC' }];
  await expect(page.getByRole('dialog', { name: 'Receive text?' })).toBeVisible();
  await expect(barIn('Receive text?')).toHaveCount(1);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog', { name: 'Receive text?' })).toHaveCount(0);
  expect(desktop.calls).toContainEqual(['Decide', 'incoming-1', false]);
  await expect(page.getByRole('dialog', { name: 'Rename phone' })).toBeVisible();
  await expect(barIn('Rename phone')).toHaveCount(1);
  desktop.state.pairRequests = [{ id: 'new-phone', name: 'New phone', source: '192.168.1.4' }];
  await expect(page.getByRole('dialog', { name: 'Connect phone?' })).toBeVisible();
  await expect(barIn('Connect phone?')).toHaveCount(1);
  await page.getByRole('button', { name: 'Connect', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'Connect phone?' })).toHaveCount(0);
  await expect(page.getByRole('textbox', { name: 'Phone name' })).toHaveValue('Unfinished rename');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.locator('.desktop-shell > .titlebar')).toHaveCount(1);
  await expect(page.getByRole('group', { name: 'New phone', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
});

for (const colorScheme of ['light', 'dark']) {
  test(`rename opens a focused dialog and supports cancellation in ${colorScheme} mode`, async ({ page, desktop }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    await page.reload();
    await page.setViewportSize({ width: 800, height: 560 });
    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    const button = page.getByRole('group', { name: 'iPhone', exact: true }).getByRole('button', { name: 'Rename', exact: true });
    await expect(page.getByRole('region', { name: 'Paired phones' }).getByRole('textbox')).toHaveCount(0);
    for (const dismiss of ['Cancel', 'Close rename', 'Escape']) {
      await button.click();
      const dialog = page.getByRole('dialog', { name: 'Rename phone', exact: true });
      const input = dialog.getByRole('textbox', { name: 'Phone name' });
      await expect(dialog).toBeVisible();
      await expect(input).toHaveValue('iPhone');
      await expect(input).toBeFocused();
      expect(await input.evaluate(node => [node.selectionStart, node.selectionEnd])).toEqual([0, 6]);
      await expect(dialog.getByRole('button', { name: 'Save', exact: true })).toBeDisabled();
      expect(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
      expect(await dialog.evaluate(node => getComputedStyle(node, '::backdrop').backgroundColor))
        .toBe(colorScheme === 'light' ? 'rgba(0, 0, 0, 0.45)' : 'rgba(0, 0, 0, 0.6)');
      if (dismiss === 'Cancel') await page.screenshot({ path: testInfo.outputPath(`rename-${colorScheme}.png`) });
      await input.fill('Discard this');
      if (dismiss === 'Escape') await page.keyboard.press('Escape');
      else await dialog.getByRole('button', { name: dismiss, exact: true }).click();
      await expect(dialog).toHaveCount(0);
      await expect(button).toBeFocused();
      await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
    }
    expect(desktop.calls.filter(([method]) => method === 'RenamePhone')).toEqual([]);
  });
}

test('rename saves with Enter, confirms success, and updates settings and Send', async ({ page, desktop }) => {
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await page.getByRole('combobox', { name: 'Send to' }).selectOption('phone-1');
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByRole('group', { name: 'iPhone', exact: true }).getByRole('button', { name: 'Rename' }).click();
  await page.getByRole('textbox', { name: 'Phone name' }).fill('  My phone  ');
  await page.getByRole('textbox', { name: 'Phone name' }).press('Enter');
  await expect(page.getByRole('dialog', { name: 'Rename phone' })).toHaveCount(0);
  await expect(page.getByText('Phone renamed', { exact: true })).toBeVisible();
  expect(desktop.calls.filter(([method]) => method === 'RenamePhone')).toEqual([['RenamePhone', 'phone-1', 'My phone']]);
  const renamed = page.getByRole('group', { name: 'My phone', exact: true });
  await expect(renamed).toBeVisible();
  await renamed.getByRole('button', { name: 'Rename' }).click();
  await expect(page.getByRole('textbox', { name: 'Phone name' })).toHaveValue('My phone');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await page.getByRole('button', { name: 'Close settings' }).click();
  await expect(page.getByRole('combobox', { name: 'Send to' })).toHaveValue('phone-1');
  await expect(page.locator('#recipient option:checked')).toHaveText('My phone / offline');
  await page.reload();
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await expect(page.getByRole('group', { name: 'My phone', exact: true })).toBeVisible();
});

test('rename validates input, keeps failed edits, and prevents duplicate saves', async ({ page, desktop }) => {
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByRole('group', { name: 'iPhone', exact: true }).getByRole('button', { name: 'Rename' }).click();
  const dialog = page.getByRole('dialog', { name: 'Rename phone' });
  const input = dialog.getByRole('textbox', { name: 'Phone name' });
  await input.fill('   ');
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog.getByRole('alert')).toHaveText('Enter a name.');
  await input.fill('\u754c'.repeat(27));
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog.getByRole('alert')).toContainText('too long');
  expect(desktop.calls.filter(([method]) => method === 'RenamePhone')).toHaveLength(0);
  await input.fill('My phone');
  desktop.rename.error = 'Could not save the phone name.';
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog.getByRole('alert')).toContainText('Could not save');
  await expect(input).toHaveValue('My phone');
  expect(desktop.state.devices[0].name).toBe('iPhone');
  desktop.rename.error = '';
  let release;
  desktop.rename.wait = new Promise(resolve => { release = resolve; });
  try {
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(dialog.getByRole('button', { name: 'Saving...', exact: true })).toBeDisabled();
    await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled();
    await expect(dialog.getByRole('button', { name: 'Close rename' })).toBeDisabled();
    await expect(input).toBeDisabled();
    await expect.poll(() => desktop.calls.filter(([method]) => method === 'RenamePhone').length).toBe(2);
    await dialog.locator('form').evaluate(node => node.requestSubmit());
    await page.keyboard.press('Escape');
    await expect(dialog).toBeVisible();
    expect(desktop.calls.filter(([method]) => method === 'RenamePhone')).toHaveLength(2);
  } finally {
    release();
  }
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole('group', { name: 'My phone', exact: true })).toBeVisible();
});

test('notifications have uniform borders without a left-side accent', async ({ page, desktop }) => {
  const expectUniformBorder = async locator => {
    await expect(locator).toBeVisible();
    const borders = await locator.evaluate(node => {
      const style = getComputedStyle(node);
      return ['Top', 'Right', 'Bottom', 'Left'].map(side => [
        style[`border${side}Width`], style[`border${side}Color`], style[`border${side}Style`],
      ]);
    });

    for (const border of borders) expect(border).toEqual(borders[0]);
  };
  await page.getByRole('button', { name: 'Copy phone link' }).click();
  await expectUniformBorder(page.locator('.toast'));
  await page.getByRole('button', { name: 'Dismiss message' }).click();
  await page.evaluate(() => {
    window.go.main.App.CopyLink = () => Promise.reject(new Error('Could not copy the phone link.'));
  });
  await page.getByRole('button', { name: 'Copy phone link' }).click();
  await expectUniformBorder(page.locator('.toast.error'));
  await page.getByRole('button', { name: 'Dismiss message' }).click();
  desktop.state.status.error = 'Connection unavailable.';
  await expectUniformBorder(page.locator('.error-banner'));
  desktop.state.status.error = '';
  desktop.rename.error = 'Could not save the phone name.';
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByRole('group', { name: 'iPhone', exact: true }).getByRole('button', { name: 'Rename' }).click();
  await page.getByRole('textbox', { name: 'Phone name' }).fill('New name');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expectUniformBorder(page.getByRole('dialog', { name: 'Rename phone' }).getByRole('alert'));
});
