import { env, exports } from "cloudflare:workers";
import { evictDurableObject, runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";
import {
  CHALLENGE_MS, decode, encode, hex, HOST_BYTE_BUDGET, HOST_OFFER_BUDGET,
  MAX_GUESTS, MAX_MESSAGE_BYTES, MAX_PENDING, randomId, type Envelope, type HostProof,
} from "../src/protocol";

const origin = "https://shareme.example";
let caller = 0;
const clients: Client[] = [];
type Message = Record<string, string>;

class Client {
  readonly messages: Message[] = [];
  readonly closed: Promise<CloseEvent>;
  private waiting?: (message: Message) => void;

  constructor(readonly ws: WebSocket) {
    this.closed = new Promise((resolve) => ws.addEventListener("close", resolve, { once: true }));
    ws.addEventListener("message", (event) => {
      const message = JSON.parse(event.data as string) as Message;
      if (this.waiting) {
        const waiting = this.waiting;
        this.waiting = undefined;
        waiting(message);
      } else {
        this.messages.push(message);
      }
    });
    ws.accept();
    clients.push(this);
  }

  send(message: object): void {
    this.ws.send(JSON.stringify(message));
  }

  next(): Promise<Message> {
    const queued = this.messages.shift();
    if (queued) return Promise.resolve(queued);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.waiting = undefined;
        reject(new Error("Timed out waiting for broker frame"));
      }, 4000);
      this.waiting = (message) => { clearTimeout(timer); resolve(message); };
    });
  }
}

function upgrade(room: string, guest = false, source?: string): Request {
  const headers: Record<string, string> = {
    Upgrade: "websocket",
    "CF-Connecting-IP": source ?? `198.51.${Math.floor(++caller / 250)}.${caller % 250 + 1}`,
  };
  if (guest) headers.Origin = origin;
  return new Request(`${origin}/signal/${room}`, { headers });
}

async function connect(room: string, guest = false): Promise<{ client: Client; challenge: string }> {
  const response = await exports.default.fetch(upgrade(room, guest));
  expect(response.status).toBe(101);
  const client = new Client(response.webSocket!);
  const message = await client.next();
  expect(message.type).toBe("challenge");
  expect(decode(message.challenge, 32)).not.toBeNull();
  return { client, challenge: message.challenge! };
}

async function identity() {
  const keys = await crypto.subtle.generateKey(
    { name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"],
  ) as CryptoKeyPair;
  const publicKey = new Uint8Array(await crypto.subtle.exportKey("raw", keys.publicKey) as ArrayBuffer);
  const digest = await crypto.subtle.digest("SHA-256", publicKey);
  return { keys, publicKey, room: hex(new Uint8Array(digest)).slice(0, 32) };
}

async function proof(id: Awaited<ReturnType<typeof identity>>, challenge: string, room = id.room): Promise<HostProof> {
  const signature = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" }, id.keys.privateKey,
    new TextEncoder().encode(`ShareMe host v1\n${room}\n${challenge}`),
  );
  return { type: "host", publicKey: encode(id.publicKey), signature: encode(new Uint8Array(signature)) };
}

async function host(existing?: Awaited<ReturnType<typeof identity>>) {
  const id = existing ?? await identity();
  const { client, challenge } = await connect(id.room);
  client.send(await proof(id, challenge));
  expect(await client.next()).toEqual({ type: "host-ready" });
  return { id, client };
}

async function guest(room: string) {
  const { client } = await connect(room, true);
  client.send({ type: "guest" });
  const ready = await client.next();
  expect(ready.type).toBe("guest-ready");
  expect(ready.sid).toMatch(/^[a-f0-9]{32}$/);
  return { client, sid: ready.sid! };
}

function envelope(sid: string, kind: "offer" | "answer" = "offer", kid = "pair"): Envelope {
  return {
    type: "signal", sid, kid, kind,
    iv: encode(crypto.getRandomValues(new Uint8Array(12))),
    data: encode(crypto.getRandomValues(new Uint8Array(32))),
  };
}

function stub(room: string) {
  return env.ROOMS.get(env.ROOMS.idFromName(room));
}

async function failure(client: Client, message: string) {
  expect(await client.next()).toEqual({ type: "error", message });
  const closed = await client.closed;
  expect([1001, 1008, 1013]).toContain(closed.code);
}

afterEach(async () => {
  const opened = clients.splice(0);
  for (const client of opened) {
    if (client.ws.readyState === WebSocket.OPEN) client.ws.close(1000, "Test finished");
  }
  await Promise.all(opened.map((client) => client.closed));
});

describe("public Worker boundary", () => {
  it("preserves the universal Shortcut download name without changing its bytes", async () => {
    const original = await env.ASSETS.fetch(new Request(origin + "/assets/ShareMe.shortcut"));
    expect(original.status).toBe(200);
    const bytes = new Uint8Array(await original.arrayBuffer());
    for (const method of ["GET", "HEAD"]) {
      const response = await exports.default.fetch(origin + "/assets/ShareMe.shortcut", { method });
      expect(response.status).toBe(200);
      expect(response.headers.get("Content-Type")).toBe("application/octet-stream");
      expect(response.headers.get("Content-Disposition")).toBe('attachment; filename="Share Me.shortcut"');
      expect(new Uint8Array(await response.arrayBuffer())).toEqual(method === "HEAD" ? new Uint8Array() : bytes);
    }
  });

  it("serves first-party assets and health with strict privacy headers", async () => {
    for (const path of ["/", "/index.html", "/app.js", "/healthz"]) {
      const response = await exports.default.fetch(origin + path);
      expect(response.status).toBe(200);
      expect(response.headers.get("Referrer-Policy")).toBe("no-referrer");
      expect(response.headers.get("Cache-Control")).toBe("no-store");
      expect(response.headers.get("Content-Security-Policy")).toContain("script-src 'self'");
      expect(response.headers.get("Content-Security-Policy")).not.toContain("unsafe-inline");
    }
    const response = await exports.default.fetch(origin + "/");
    expect(response.headers.get("Content-Security-Policy")).toContain(
      "connect-src 'self' wss://shareme.example",
    );
    expect(await response.text()).toContain("First-party phone app fixture");
    expect(await (await exports.default.fetch(origin + "/healthz")).json()).toEqual({ ok: true });
  });

  it.each([
    "/api", "/api/session", "/api/upload", "/api/outbox", "/api/outbox/file", "/api/shortcut/setup",
    "/assets/Share%20Me.shortcut",
    "/receive", "/receive/secret", "/receive/random",
    "/API/session", "//api/session", "/%61pi/session", "/receive%2fsecret",
    "/signal/nope", "/does-not-exist", "/signal/" + "a".repeat(32) + "?key=secret",
  ])("never provides file routes or an SPA fallback at %s", async (path) => {
    for (const method of ["GET", "HEAD", "POST", "PUT", "OPTIONS"]) {
      const response = await exports.default.fetch(origin + path, { method });
      expect(response.status).toBe(404);
      expect(await response.text()).not.toContain("deliberately deployed");
    }
  });

  it.each(["https://evil.example", "null", origin + "/", "http://shareme.example", origin + ":444"])(
    "rejects hostile or inexact Origin %s before room creation", async (badOrigin) => {
      const request = upgrade(randomId());
      request.headers.set("Origin", badOrigin);
      expect((await exports.default.fetch(request)).status).toBe(403);
    },
  );

  it("permits HTTP only for exact naturally loopback origins", async () => {
    for (const local of ["http://localhost:8787", "http://127.0.0.1:8787"]) {
      const response = await exports.default.fetch(new Request(`${local}/signal/${randomId()}`, {
        headers: { Upgrade: "websocket", Origin: local },
      }));
      expect(response.status).toBe(101);
      const client = new Client(response.webSocket!);
      expect((await client.next()).type).toBe("challenge");
    }
    const response = await exports.default.fetch(new Request(`http://192.168.1.3/signal/${randomId()}`, {
      headers: { Upgrade: "websocket", Origin: "http://192.168.1.3" },
    }));
    expect(response.status).toBe(403);
  });

  it("rejects non-upgrades and alternate subprotocols", async () => {
    expect((await exports.default.fetch(`${origin}/signal/${randomId()}`)).status).toBe(426);
    const request = upgrade(randomId());
    request.headers.set("Sec-WebSocket-Protocol", "anything");
    expect((await exports.default.fetch(request)).status).toBe(400);
  });

  it("redirects public static HTTP to HTTPS without redirecting transfer routes", async () => {
    const response = await exports.default.fetch("http://shareme.example/", { redirect: "manual" });
    expect(response.status).toBe(308);
    expect(response.headers.get("Location")).toBe("https://shareme.example/");
    expect((await exports.default.fetch("http://shareme.example/api/session")).status).toBe(404);
    expect((await exports.default.fetch("http://shareme.example/receive/secret")).status).toBe(404);
  });

  it("limits anonymous creation across random rooms using the real rate binding", async () => {
    const source = `192.0.2.${++caller}`;
    let limited = false;
    for (let index = 0; index < 65; index++) {
      const response = await exports.default.fetch(upgrade(randomId(), false, source));
      if (response.status === 429) {
        expect(await response.json()).toEqual({ type: "error", message: "Signaling rate limit exceeded" });
        expect(response.headers.get("Retry-After")).toBe("60");
        limited = true;
        break;
      }
      expect(response.status).toBe(101);
      const client = new Client(response.webSocket!);
      await client.next();
      client.ws.close();
    }
    expect(limited).toBe(true);
  });
});

describe("host authentication", () => {
  it("authenticates a P1363 P-256 proof bound to the public-key room and one-use challenge", async () => {
    const id = await identity();
    const { client, challenge } = await connect(id.room);
    const signed = await proof(id, challenge);
    expect(decode(signed.signature, 64)).not.toBeNull();
    client.send(signed);
    expect(await client.next()).toEqual({ type: "host-ready" });
    client.send(signed);
    await failure(client, "Already authenticated");
  });

  it("rejects captured proofs replayed on another socket", async () => {
    const id = await identity();
    const first = await connect(id.room);
    const signed = await proof(id, first.challenge);
    const second = await connect(id.room);
    second.client.send(signed);
    await failure(second.client, "Host authentication failed");
    first.client.send(signed);
    expect(await first.client.next()).toEqual({ type: "host-ready" });
  });

  it("rejects another identity's valid signature even when signed for the target room", async () => {
    const target = await identity();
    const attacker = await identity();
    const pending = await connect(target.room);
    pending.client.send(await proof(attacker, pending.challenge, target.room));
    await failure(pending.client, "Host authentication failed");
  });

  it("rejects a tampered signature without disturbing an online host", async () => {
    const active = await host();
    const pending = await connect(active.id.room);
    const signed = await proof(active.id, pending.challenge);
    const signature = decode(signed.signature, 64)!;
    signature[0] = signature[0]! ^ 1;
    pending.client.send({ ...signed, signature: encode(signature) });
    await failure(pending.client, "Host authentication failed");
    await guest(active.id.room);
    expect(active.client.ws.readyState).toBe(WebSocket.OPEN);
  });

  it.each(["compressed-key", "invalid-point", "short-signature", "extra-field"])(
    "rejects malformed host proof: %s", async (scenario) => {
      const id = await identity();
      const pending = await connect(id.room);
      const signed = await proof(id, pending.challenge);
      if (scenario === "compressed-key") signed.publicKey = encode(id.publicKey.slice(0, 33));
      if (scenario === "invalid-point") {
        const point = new Uint8Array(65);
        point[0] = 4;
        signed.publicKey = encode(point);
      }
      if (scenario === "short-signature") signed.signature = encode(new Uint8Array(63));
      pending.client.send(scenario === "extra-field" ? { ...signed, secret: "not-accepted" } : signed);
      await failure(pending.client, scenario === "invalid-point"
        ? "Host authentication failed" : "Invalid signaling message");
    },
  );

  it("requires absent Origin for hosts and matching Origin for guests", async () => {
    const id = await identity();
    const browser = await connect(id.room, true);
    browser.client.send(await proof(id, browser.challenge));
    await failure(browser.client, "Authentication role not allowed");
    const native = await connect(id.room);
    native.client.send({ type: "guest" });
    await failure(native.client, "Authentication role not allowed");
  });

  it("expires challenges after ten seconds and clears the idle alarm", async () => {
    const id = await identity();
    const pending = await connect(id.room);
    await runInDurableObject(stub(id.room), (_instance, state) => {
      for (const ws of state.getWebSockets()) {
        const attachment = ws.deserializeAttachment();
        expect(attachment.expires - Date.now()).toBeLessThanOrEqual(CHALLENGE_MS);
        attachment.expires = Date.now() - 1;
        ws.serializeAttachment(attachment);
      }
    });
    expect(await runDurableObjectAlarm(stub(id.room))).toBe(true);
    await failure(pending.client, "Authentication challenge expired");
    const alarm = await runInDurableObject(stub(id.room), (_instance, state) => state.storage.getAlarm());
    expect(alarm).toBeNull();
  });

  it("rejects an expired proof even before the alarm is delivered", async () => {
    const id = await identity();
    const pending = await connect(id.room);
    const signed = await proof(id, pending.challenge);
    await runInDurableObject(stub(id.room), (_instance, state) => {
      const ws = state.getWebSockets()[0]!;
      const attachment = ws.deserializeAttachment();
      attachment.expires = Date.now() - 1;
      ws.serializeAttachment(attachment);
    });
    pending.client.send(signed);
    await failure(pending.client, "Authentication challenge expired");
  });

  it("only replaces the old host and guests after successful authentication", async () => {
    const old = await host();
    const phone = await guest(old.id.room);
    const replacement = await host(old.id);
    await failure(old.client, "Host replaced");
    await failure(phone.client, "PC is offline");
    expect(replacement.client.ws.readyState).toBe(WebSocket.OPEN);
    await guest(old.id.room);
  });
});

describe("sealed, room-isolated routing", () => {
  it.each(["pair", "f".repeat(32)])("routes exactly one opaque offer and answer for kid %s", async (kid) => {
    const active = await host();
    const phone = await guest(active.id.room);
    const offer = envelope(phone.sid, "offer", kid);
    phone.client.send(offer);
    expect(await active.client.next()).toEqual(offer);
    const answer = envelope(phone.sid, "answer", kid);
    active.client.send(answer);
    expect(await phone.client.next()).toEqual(answer);
  });

  it("does not decrypt or validate GCM authenticity: only the peers hold the key", async () => {
    const active = await host();
    const phone = await guest(active.id.room);
    const key = await crypto.subtle.generateKey(
      { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"],
    ) as CryptoKey;
    const offer = envelope(phone.sid);
    const params = {
      name: "AES-GCM", iv: decode(offer.iv)!,
      additionalData: new TextEncoder().encode(`ShareMe signal v1\n${active.id.room}\n${phone.sid}\npair\noffer`),
    };
    const ciphertext = new Uint8Array(await crypto.subtle.encrypt(
      params, key, new TextEncoder().encode('{"sdp":"private","name":"Phone"}'),
    ));
    ciphertext[0] = ciphertext[0]! ^ 1;
    offer.data = encode(ciphertext);
    phone.client.send(offer);
    expect(await active.client.next()).toEqual(offer);
    await expect(crypto.subtle.decrypt(params, key, ciphertext)).rejects.toThrow();
  });

  it("does not broadcast offers to other guests or another room", async () => {
    const first = await host();
    const second = await host();
    const one = await guest(first.id.room);
    const two = await guest(first.id.room);
    const foreign = await guest(second.id.room);
    const offer = envelope(one.sid);
    one.client.send(offer);
    expect(await first.client.next()).toEqual(offer);
    expect(two.client.messages).toEqual([]);
    expect(second.client.messages).toEqual([]);
    expect(foreign.client.messages).toEqual([]);
    one.client.send(envelope(two.sid));
    await failure(one.client, "Offer not allowed");
    expect(two.client.messages).toEqual([]);
  });

  it("rejects a host attempting to answer a session in another room", async () => {
    const first = await host();
    const second = await host();
    const phone = await guest(second.id.room);
    first.client.send(envelope(phone.sid, "answer"));
    await failure(first.client, "Answer session not allowed");
    const offer = envelope(phone.sid);
    phone.client.send(offer);
    expect(await second.client.next()).toEqual(offer);
  });

  it("rejects cross-room requests made directly to a Durable Object", async () => {
    expect((await stub(randomId()).fetch(upgrade(randomId()))).status).toBe(404);
  });

  it("rejects duplicate guest offers", async () => {
    const active = await host();
    const phone = await guest(active.id.room);
    const offer = envelope(phone.sid);
    phone.client.send(offer);
    expect(await active.client.next()).toEqual(offer);
    phone.client.send(offer);
    await failure(phone.client, "Offer not allowed");
    expect(active.client.messages).toEqual([]);
  });

  it.each(["before-offer", "wrong-kid", "repeated"])("rejects host answer %s", async (scenario) => {
    const active = await host();
    const phone = await guest(active.id.room);
    if (scenario !== "before-offer") {
      phone.client.send(envelope(phone.sid));
      await active.client.next();
    }
    const answer = envelope(phone.sid, "answer");
    if (scenario === "repeated") {
      active.client.send(answer);
      expect(await phone.client.next()).toEqual(answer);
    }
    if (scenario === "wrong-kid") answer.kid = "a".repeat(32);
    active.client.send(answer);
    await failure(active.client, "Answer session not allowed");
    await failure(phone.client, "PC is offline");
  });

  it("guests cannot send answers and hosts cannot send offers", async () => {
    const active = await host();
    const phone = await guest(active.id.room);
    phone.client.send(envelope(phone.sid, "answer"));
    await failure(phone.client, "Offer not allowed");
    active.client.send(envelope(randomId()));
    await failure(active.client, "Answer not allowed");
  });

  it.each([
    ["unknown field", { extra: "secret" }],
    ["invalid session", { sid: "bad" }],
    ["invalid key identifier", { kid: "not-a-device-id" }],
    ["trickle candidate", { kind: "candidate" }],
    ["short nonce", { iv: encode(new Uint8Array(11)) }],
    ["padded nonce", { iv: "AAAAAAAAAAAAAAAA=" }],
    ["short tag", { data: encode(new Uint8Array(15)) }],
    ["invalid alphabet", { data: "***" }],
    ["noncanonical base64url", { data: "A".repeat(21) + "B" }],
    ["oversized ciphertext", { data: "A".repeat(MAX_MESSAGE_BYTES) }],
    ["upload action", { type: "upload" }],
  ] as const)("rejects malformed ciphertext envelope: %s", async (_name, change) => {
    const active = await host();
    const phone = await guest(active.id.room);
    phone.client.send({ ...envelope(phone.sid), ...change });
    await failure(phone.client, "Invalid signaling message");
    expect(active.client.messages).toEqual([]);
  });

  it.each(["null", "[]", "{}", '{"type":"guest","extra":1}', '{"type":"ping"}', "invalid"])(
    "rejects non-contract JSON %s", async (raw) => {
      const pending = await connect(randomId(), true);
      pending.client.ws.send(raw);
      await failure(pending.client, "Invalid signaling message");
    },
  );

  it("rejects binary WebSocket frames", async () => {
    const pending = await connect(randomId(), true);
    pending.client.ws.send(new Uint8Array([1, 2, 3]));
    await failure(pending.client, "Invalid signaling message");
  });

  it("rejects even valid JSON padded to exactly 32 KiB", async () => {
    const pending = await connect(randomId(), true);
    pending.client.ws.send('{"type":"guest"}'.padEnd(MAX_MESSAGE_BYTES, " "));
    await failure(pending.client, "Invalid signaling message");
  });
});

describe("bounded resources and transient state", () => {
  it("reports an offline PC without leaving an authenticated guest", async () => {
    const room = randomId();
    const pending = await connect(room, true);
    pending.client.send({ type: "guest" });
    await failure(pending.client, "PC is offline");
    const stored = await runInDurableObject(stub(room), async (_instance, state) => ({
      records: (await state.storage.list()).size,
      alarm: await state.storage.getAlarm(),
      live: state.getWebSockets().filter((ws) => ws.deserializeAttachment()?.role !== "closed").length,
    }));
    expect(stored).toEqual({ records: 0, alarm: null, live: 0 });
  });

  it("bounds unauthenticated sockets and admits again after alarm cleanup", async () => {
    const room = randomId();
    for (let index = 0; index < MAX_PENDING; index++) await connect(room, true);
    const response = await exports.default.fetch(upgrade(room));
    expect(response.status).toBe(429);
    expect(await response.json()).toEqual({ type: "error", message: "Room connection limit exceeded" });
    await runInDurableObject(stub(room), (_instance, state) => {
      for (const ws of state.getWebSockets()) {
        const attachment = ws.deserializeAttachment();
        attachment.expires = 0;
        ws.serializeAttachment(attachment);
      }
    });
    await runDurableObjectAlarm(stub(room));
    for (const client of clients) await client.closed;
    await connect(room);
  });

  it("allows eight guests and rejects a ninth", async () => {
    const active = await host();
    for (let index = 0; index < MAX_GUESTS; index++) await guest(active.id.room);
    const extra = await connect(active.id.room, true);
    extra.client.send({ type: "guest" });
    await failure(extra.client, "Room guest limit exceeded");
    expect(active.client.ws.readyState).toBe(WebSocket.OPEN);
  });

  it("releases the guest slot and its session on closure without disconnecting the host", async () => {
    const active = await host();
    const phones = [];
    for (let index = 0; index < MAX_GUESTS; index++) phones.push(await guest(active.id.room));
    phones[0]!.client.ws.close(1000, "Done");
    await phones[0]!.client.closed;
    const replacement = await guest(active.id.room);
    expect(replacement.sid).not.toBe(phones[0]!.sid);
    expect(active.client.ws.readyState).toBe(WebSocket.OPEN);
    const sessions = await runInDurableObject(stub(active.id.room), (_instance, state) =>
      state.getWebSockets().map((ws) => ws.deserializeAttachment()?.sid),
    );
    expect(sessions).not.toContain(phones[0]!.sid);
  });

  it.each(["bytes", "offers"])("enforces the host lifetime %s budget to bound queued output", async (budget) => {
    const active = await host();
    const phone = await guest(active.id.room);
    await runInDurableObject(stub(active.id.room), (_instance, state) => {
      for (const ws of state.getWebSockets()) {
        const attachment = ws.deserializeAttachment();
        if (attachment.role === "host") {
          attachment[budget] = budget === "bytes" ? HOST_BYTE_BUDGET : HOST_OFFER_BUDGET;
          ws.serializeAttachment(attachment);
        }
      }
    });
    phone.client.send(envelope(phone.sid));
    await failure(active.client, "Host signaling budget exceeded; reconnect");
    await failure(phone.client, "PC is offline");
  });

  it("expires guests while leaving the authenticated host online", async () => {
    const active = await host();
    const phone = await guest(active.id.room);
    await runInDurableObject(stub(active.id.room), (_instance, state) => {
      for (const ws of state.getWebSockets()) {
        const attachment = ws.deserializeAttachment();
        if (attachment.role === "guest") {
          attachment.expires = 0;
          ws.serializeAttachment(attachment);
        }
      }
    });
    await runDurableObjectAlarm(stub(active.id.room));
    await failure(phone.client, "Signaling session expired");
    expect(active.client.ws.readyState).toBe(WebSocket.OPEN);
  });

  it("expires the host and all guests, and deletes the final alarm", async () => {
    const active = await host();
    const phone = await guest(active.id.room);
    await runInDurableObject(stub(active.id.room), (_instance, state) => {
      for (const ws of state.getWebSockets()) {
        const attachment = ws.deserializeAttachment();
        if (attachment.role === "host") {
          attachment.expires = Date.now() - 1;
          ws.serializeAttachment(attachment);
        }
      }
    });
    await runDurableObjectAlarm(stub(active.id.room));
    await failure(active.client, "Signaling session expired");
    await failure(phone.client, "PC is offline");
    expect(await runInDurableObject(stub(active.id.room), (_instance, state) =>
      state.storage.getAlarm(),
    )).toBeNull();
  });

  it("clears guests, all attachments, alarm, and application storage on host closure", async () => {
    const active = await host();
    const phone = await guest(active.id.room);
    active.client.ws.close(1000, "Shutdown");
    await active.client.closed;
    await failure(phone.client, "PC is offline");
    const stored = await runInDurableObject(stub(active.id.room), async (_instance, state) => ({
      records: (await state.storage.list()).size,
      alarm: await state.storage.getAlarm(),
      attachments: state.getWebSockets().map((ws) => ws.deserializeAttachment()),
      tables: state.storage.sql.exec<{ name: string }>(
        "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE '_cf_%'",
      ).toArray(),
    }));
    expect(stored.records).toBe(0);
    expect(stored.alarm).toBeNull();
    expect(stored.attachments.every((attachment) => attachment.role === "closed")).toBe(true);
    expect(stored.tables).toEqual([]);
  });

  it("survives real hibernation with replay flags and pending authentication intact", async () => {
    const active = await host();
    const phone = await guest(active.id.room);
    const pending = await connect(active.id.room);
    const offer = envelope(phone.sid);
    phone.client.send(offer);
    expect(await active.client.next()).toEqual(offer);
    await evictDurableObject(stub(active.id.room));
    const answer = envelope(phone.sid, "answer");
    active.client.send(answer);
    expect(await phone.client.next()).toEqual(answer);
    phone.client.send(offer);
    await failure(phone.client, "Offer not allowed");
    pending.client.send(await proof(active.id, pending.challenge));
    expect(await pending.client.next()).toEqual({ type: "host-ready" });
    await failure(active.client, "Host replaced");
  });

  // Run last: this deliberately exhausts the real shared location budget.
  it("enforces the location-wide anonymous budget even with a fresh source and room", async () => {
    for (let index = 0; index < 300; index++) {
      if (!(await env.SIGNAL_BUDGET.limit({ key: "anonymous-signaling" })).success) break;
    }
    const response = await exports.default.fetch(upgrade(randomId()));
    expect(response.status).toBe(429);
    expect(await response.json()).toEqual({ type: "error", message: "Signaling rate limit exceeded" });
  });
});
