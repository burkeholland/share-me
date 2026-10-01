import assert from 'node:assert/strict';
import test from 'node:test';
import { createPeerFetch } from '../src/peer-http.js';
import { acceptOutgoing } from '../src/save-transfer.js';

const encoder = new TextEncoder();
const path = '/api/outbox/' + 'a'.repeat(32);

class Channel {
  readyState = 'open';
  credit = 65536;
  offset = 0;
  scheduled = false;
  started = false;

  constructor(status, body) {
    this.bytes = encoder.encode(`HTTP/1.1 ${status} Response\r\nContent-Length: ${body.length}\r\n\r\n${body}`);
  }

  send(value) {
    if (typeof value === 'string') {
      if (value.startsWith('credit:')) this.credit += Number(value.slice(7));
    } else {
      this.started = true;
    }
    if (!this.started || this.scheduled || this.readyState !== 'open') return;
    this.scheduled = true;
    queueMicrotask(() => {
      this.scheduled = false;
      while (this.readyState === 'open' && this.credit && this.offset < this.bytes.length) {
        const length = Math.min(16384, this.credit, this.bytes.length - this.offset);
        const data = this.bytes.slice(this.offset, this.offset + length);
        this.offset += length;
        this.credit -= length;
        this.onmessage({ data: data.buffer });
      }
      if (this.readyState === 'open' && this.offset === this.bytes.length) {
        this.onmessage({ data: 'fin' });
        this.close();
      }
    });
  }

  close() {
    if (this.readyState === 'closed') return;
    this.readyState = 'closed';
    this.onclose?.();
  }
}

function connection(reply = () => [200, 'hello']) {
  return {
    connectionState: 'connected',
    channels: [],
    createDataChannel() {
      const channel = new Channel(...reply(this.channels.length));
      this.channels.push(channel);
      return channel;
    },
  };
}

test('four rejected text downloads release slots before the fifth succeeds', async () => {
  const pc = connection(index => index < 4 ? [404, '{}'] : [200, 'hello']);
  const transport = { fetch: createPeerFetch(pc) };
  const item = { id: 'a'.repeat(32), kind: 'text' };
  for (let n = 0; n < 4; n++) {
    await assert.rejects(acceptOutgoing(transport, item), /no longer available/);
  }
  assert.deepEqual(await acceptOutgoing(transport, item), { text: 'hello' });
  assert.ok(pc.channels.every(channel => channel.readyState === 'closed'));
});

test('abort after headers releases unread slots exactly once', async () => {
  const pc = connection();
  const fetch = createPeerFetch(pc);
  const abort = new AbortController();
  for (let n = 0; n < 4; n++) await fetch(path, { signal: abort.signal });
  abort.abort();
  for (const channel of pc.channels) channel.onerror();
  const responses = [];
  for (let n = 0; n < 4; n++) responses.push(await fetch(path));
  await assert.rejects(fetch(path), /Four requests/);
  for (const response of responses) assert.equal(await response.text(), 'hello');
  assert.equal(await (await fetch(path)).text(), 'hello');
});

test('deadline releases unread response slots', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const fetch = createPeerFetch(connection());
  for (let n = 0; n < 4; n++) await fetch(path);
  t.mock.timers.tick(30 * 60 * 1000);
  assert.equal(await (await fetch(path)).text(), 'hello');
});

test('channel errors after headers release unread slots', async () => {
  const pc = connection();
  const fetch = createPeerFetch(pc);
  for (let n = 0; n < 4; n++) await fetch(path);
  for (const channel of pc.channels) channel.onerror();
  assert.equal(await (await fetch(path)).text(), 'hello');
});

test('header, body, and channel setup failures release reservations', async () => {
  const pc = connection();
  const fetch = createPeerFetch(pc);
  for (let n = 0; n < 4; n++) {
    await assert.rejects(fetch(path, { headers: { 'bad\nheader': 'value' } }));
    await assert.rejects(fetch(path, { body: { arrayBuffer: async () => { throw new Error('body preparation'); } } }), /body preparation/);
  }
  assert.ok(pc.channels.every(channel => channel.readyState === 'closed'));
  const create = pc.createDataChannel;
  pc.createDataChannel = () => { throw new Error('channel creation'); };
  for (let n = 0; n < 4; n++) await assert.rejects(fetch(path), /channel creation/);
  pc.createDataChannel = create;
  assert.equal(await (await fetch(path)).text(), 'hello');
});

test('an unread response holds the PC to one window until it is read', async () => {
  const settle = () => new Promise(resolve => setImmediate(resolve));
  const body = 'x'.repeat(2 * 1024 * 1024);
  const pc = connection(() => [200, body]);
  const response = await createPeerFetch(pc)(path);
  const reader = response.body.getReader();
  try {
    await settle();
    const channel = pc.channels[0];
    // The header parser holds one packet, so one more than a full window can be outstanding.
    const limit = 65536 + 16384;
    assert.equal(channel.offset, limit);
    let read = channel.bytes.length - body.length;
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      assert.ok(value.every(byte => byte === 120));
      read += value.length;
      await settle();
      assert.ok(channel.offset - read <= limit);
    }
    assert.equal(read, channel.bytes.length);
  } finally {
    // A failed assertion must not leave the request's 30-minute deadline keeping the run alive.
    await reader.cancel();
  }
});
