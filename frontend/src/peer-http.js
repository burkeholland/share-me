const WINDOW = 65536;
const CHUNK = 16384;
const encoder = new TextEncoder();
const decoder = new TextDecoder();

export class PeerStream {
  constructor(channel) {
    this.channel = channel;
    this.credit = WINDOW;
    this.budget = WINDOW;
    this.queue = [];
    this.waiters = new Set();
    this.remoteFin = false;
    this.error = null;
    channel.binaryType = 'arraybuffer';
    channel.onopen = () => this.wake();
    channel.onmessage = event => {
      try {
        if (typeof event.data === 'string') {
          if (event.data === 'fin') {
            if (this.remoteFin) throw new Error('Duplicate stream ending');
            this.remoteFin = true;
            channel.send('fin-ack');
          } else if (event.data === 'fin-ack') {
            this.finAcknowledged = true;
          } else if (/^credit:[1-9]\d{0,5}$/.test(event.data)) {
            const credit = Number(event.data.slice(7));
            if (this.credit + credit > WINDOW) throw new Error('Invalid stream credit');
            this.credit += credit;
          } else throw new Error('Invalid stream control');
        } else {
          const bytes = new Uint8Array(event.data);
          if (!bytes.length || bytes.length > CHUNK || bytes.length > this.budget || this.remoteFin) {
            throw new Error('Invalid stream data');
          }
          this.budget -= bytes.length;
          this.queue.push(bytes);
        }
        this.wake();
      } catch (error) { this.fail(error); }
    };
    channel.onerror = () => this.fail(new Error('Direct connection failed'));
    channel.onclose = () => {
      if (!this.remoteFin) this.fail(new Error('Connection closed. Check the PC inbox before retrying.'));
      this.wake();
    };
  }

  wake() {
    for (const wake of this.waiters) wake();
    this.waiters.clear();
  }

  wait() { return new Promise(resolve => this.waiters.add(resolve)); }

  fail(error) {
    if (!this.error) this.error = error;
    this.channel.close();
    this.wake();
  }

  async write(bytes) {
    let offset = 0;
    while (offset < bytes.length) {
      if (this.error) throw this.error;
      if (['closing', 'closed'].includes(this.channel.readyState) || this.remoteFin) {
        throw new Error('Request stream closed');
      }
      if (this.channel.readyState !== 'open' || this.credit === 0) {
        await this.wait();
        continue;
      }
      const length = Math.min(CHUNK, this.credit, bytes.length - offset);
      this.credit -= length;
      this.channel.send(bytes.subarray(offset, offset + length));
      offset += length;
    }
  }

  async read() {
    while (!this.queue.length) {
      if (this.error) throw this.error;
      if (this.remoteFin) return null;
      await this.wait();
    }
    const bytes = this.queue.shift();
    this.budget += bytes.length;
    if (!this.remoteFin && this.channel.readyState === 'open') this.channel.send(`credit:${bytes.length}`);
    return bytes;
  }

  endWrite() {
    if (this.channel.readyState === 'open' && !this.sentFin) {
      this.sentFin = true;
      this.channel.send('fin');
    }
  }
}

class Reader {
  constructor(stream) { this.stream = stream; this.buffer = new Uint8Array(); }

  async take(size) {
    if (!this.buffer.length) {
      this.buffer = await this.stream.read();
      if (this.buffer === null) { this.buffer = new Uint8Array(); return null; }
    }
    const part = this.buffer.subarray(0, size);
    this.buffer = this.buffer.subarray(part.length);
    return part;
  }

  async line(limit = 16384) {
    const bytes = [];
    while (bytes.length <= limit) {
      const next = await this.take(1);
      if (next === null) throw new Error('Incomplete response from PC');
      bytes.push(next[0]);
      if (bytes.length >= 2 && bytes.at(-2) === 13 && bytes.at(-1) === 10) {
        return decoder.decode(new Uint8Array(bytes.slice(0, -2)));
      }
    }
    throw new Error('Response headers are too large');
  }
}

export function createPeerFetch(connection) {
  let active = 0;
  return async function peerFetch(path, { method = 'GET', headers: inputHeaders, body, signal, onProgress } = {}) {
    if (connection.connectionState !== 'connected') throw new Error('PC is disconnected');
    if (active >= 4) throw new Error('Four requests are already active. Try again shortly.');
    if (!/^\/(?:api\/[a-zA-Z0-9/_-]+|healthz)$/.test(path) || !['GET', 'HEAD', 'POST', 'DELETE'].includes(method)) {
      throw new Error('Invalid peer request');
    }
    signal?.throwIfAborted();
    active++;
    const id = crypto.randomUUID().replaceAll('-', '');
    const stream = new PeerStream(connection.createDataChannel(`shareme.http.${id}`, { ordered: true }));
    const abort = () => stream.fail(new DOMException('Cancelled.', 'AbortError'));
    signal?.addEventListener('abort', abort, { once: true });
    const deadline = setTimeout(() => stream.fail(new Error('Transfer timed out. Check the PC inbox.')), 30 * 60 * 1000);
    let cleaned = false;
    const cleanup = () => {
      if (cleaned) return;
      cleaned = true;
      active--;
      clearTimeout(deadline);
      signal?.removeEventListener('abort', abort);
    };
    const headers = new Headers(inputHeaders);
    for (const name of ['host', 'connection', 'content-length', 'transfer-encoding', 'origin']) headers.delete(name);
    let source;
    let chunked = false;
    let length = 0;
    if (body instanceof FormData) {
      const encoded = new Response(body);
      source = encoded.body.getReader();
      headers.set('Content-Type', encoded.headers.get('Content-Type'));
      chunked = true;
    } else if (body != null) {
      const bytes = typeof body === 'string' ? encoder.encode(body) :
        body instanceof Uint8Array ? body : new Uint8Array(await body.arrayBuffer());
      length = bytes.length;
      source = new ReadableStream({ start(controller) { controller.enqueue(bytes); controller.close(); } }).getReader();
    }
    let request = `${method} ${path} HTTP/1.1\r\nHost: peer.shareme\r\nConnection: close\r\n`;
    request += chunked ? 'Transfer-Encoding: chunked\r\n' : `Content-Length: ${length}\r\n`;
    for (const [name, value] of headers) {
      if (/[\r\n]/.test(name + value)) { cleanup(); stream.fail(new Error('Invalid request header')); throw stream.error; }
      request += `${name}: ${value}\r\n`;
    }
    request += '\r\n';
    const write = async () => {
      await stream.write(encoder.encode(request));
      let sent = 0;
      try {
        while (source) {
          const { value, done } = await source.read();
          if (done) break;
          if (chunked) await stream.write(encoder.encode(`${value.length.toString(16)}\r\n`));
          await stream.write(value);
          if (chunked) await stream.write(encoder.encode('\r\n'));
          sent += value.length;
          onProgress?.(sent);
        }
        if (chunked) await stream.write(encoder.encode('0\r\n\r\n'));
        // HTTP framing ends the request. A transport FIN here would cancel
        // Go's request context while approval/scanning is still running.
      } finally {
        source?.releaseLock();
      }
    };
    write().catch(error => { if (!stream.remoteFin) stream.fail(error); });
    try {
      const reader = new Reader(stream);
      let status;
      let responseHeaders;
      let headerBytes = 0;
      do {
        const first = await reader.line();
        const match = /^HTTP\/1\.[01] (\d{3})(?: .*)?$/.exec(first);
        if (!match) throw new Error('Invalid response from PC');
        status = Number(match[1]);
        responseHeaders = new Headers();
        for (;;) {
          const line = await reader.line();
          headerBytes += line.length + 2;
          if (headerBytes > 16384) throw new Error('Response headers are too large');
          if (!line) break;
          const colon = line.indexOf(':');
          if (colon <= 0) throw new Error('Invalid response header');
          responseHeaders.append(line.slice(0, colon), line.slice(colon + 1).trim());
        }
      } while (status >= 100 && status < 200);
      if (method === 'HEAD' || [204, 304].includes(status)) {
        cleanup();
        return new Response(null, { status, headers: responseHeaders });
      }
      const isChunked = responseHeaders.get('transfer-encoding')?.toLowerCase() === 'chunked';
      let remaining = responseHeaders.has('content-length') ? Number(responseHeaders.get('content-length')) : null;
      if (remaining !== null && (!Number.isSafeInteger(remaining) || remaining < 0 || remaining > 2 ** 31)) {
        throw new Error('Invalid response size');
      }
      let chunkRemaining = 0;
      let finished = false;
      const finish = controller => {
        finished = true;
        controller.close();
        cleanup();
      };
      const responseBody = new ReadableStream({
        async pull(controller) {
          if (finished) return;
          try {
            if (isChunked) {
              if (chunkRemaining === 0) {
                const line = await reader.line(128);
                if (!/^[0-9a-fA-F]+(?:;[^\r\n]*)?$/.test(line)) throw new Error('Invalid response chunk');
                chunkRemaining = Number.parseInt(line, 16);
                if (!Number.isSafeInteger(chunkRemaining) || chunkRemaining > 2 ** 31) throw new Error('Response chunk is too large');
                if (chunkRemaining === 0) {
                  let trailers = 0;
                  while ((await reader.line()).length) {
                    if (++trailers > 16) throw new Error('Too many response trailers');
                  }
                  finish(controller);
                  return;
                }
              }
              const bytes = await reader.take(Math.min(CHUNK, chunkRemaining));
              if (!bytes) throw new Error('Incomplete response from PC');
              chunkRemaining -= bytes.length;
              if (!chunkRemaining && await reader.line(2) !== '') throw new Error('Invalid response chunk ending');
              controller.enqueue(bytes);
            } else {
              if (remaining === 0) { finish(controller); return; }
              const bytes = await reader.take(Math.min(CHUNK, remaining ?? CHUNK));
              if (!bytes) {
                if (remaining !== null && remaining !== 0) throw new Error('Incomplete response from PC');
                finish(controller);
                return;
              }
              if (remaining !== null) remaining -= bytes.length;
              controller.enqueue(bytes);
            }
          } catch (error) {
            finished = true;
            stream.fail(error);
            controller.error(error);
            cleanup();
          }
        },
        cancel() {
          finished = true;
          stream.fail(new DOMException('Cancelled.', 'AbortError'));
          cleanup();
        },
      }, { highWaterMark: 0 });
      return new Response(responseBody, { status, headers: responseHeaders });
    } catch (error) {
      stream.fail(error);
      cleanup();
      throw error;
    }
  };
}
