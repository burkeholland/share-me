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
  maxOutstanding = 0;

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
        this.maxOutstanding = Math.max(this.maxOutstanding, 65536 - this.credit);
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

test('successful multi-window streaming keeps credit bounded', async () => {
  const body = 'x'.repeat(2 * 1024 * 1024);
  const pc = connection(() => [200, body]);
  const fetch = createPeerFetch(pc);
  assert.equal(await (await fetch(path)).text(), body);
  assert.ok(pc.channels[0].maxOutstanding <= 65536);
  assert.equal(await (await fetch(path)).text(), body);
});
