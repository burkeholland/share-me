import { DurableObject } from "cloudflare:workers";
import type { Env } from "./index";
import {
  allowedOrigin, CHALLENGE_MS, encode, GUEST_MS, HOST_BYTE_BUDGET, HOST_MS,
  HOST_OFFER_BUDGET, MAX_GUESTS, MAX_PENDING, MAX_SOCKETS, parseMessage,
  randomId, signalRoom, verifyHost,
  type ClientMessage, type Envelope,
} from "./protocol";

type Pending = {
  role: "pending";
  allowed: "host" | "guest";
  room: string;
  challenge: string;
  expires: number;
};
type Host = {
  role: "host";
  generation: string;
  expires: number;
  bytes: number;
  offers: number;
};
type Guest = {
  role: "guest";
  generation: string;
  sid: string;
  expires: number;
  kid?: string;
  offered: boolean;
  answered: boolean;
};
type Attachment = Pending | Host | Guest | { role: "verifying"; expires: number } | { role: "closed" };

export class SignalRoom extends DurableObject<Env> {
  private state(ws: WebSocket): Attachment {
    return ws.deserializeAttachment() ?? { role: "closed" };
  }

  private sockets(): WebSocket[] {
    return this.ctx.getWebSockets();
  }

  private host(): { ws: WebSocket; state: Host } | null {
    for (const ws of this.sockets()) {
      const state = this.state(ws);
      if (state.role === "host" && ws.readyState === WebSocket.OPEN) return { ws, state };
    }
    return null;
  }

  private closeOne(ws: WebSocket, message: string, code = 1008, notify = true): void {
    ws.serializeAttachment({ role: "closed" });
    try {
      if (notify && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "error", message }));
      }
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CLOSING) {
        ws.close(code, message);
      }
    } catch {
      // A peer may disappear between inspecting its state and sending a close.
    }
  }

  private remove(ws: WebSocket, message: string, code = 1008, notify = true): void {
    const previous = this.state(ws);
    if (previous.role === "host") {
      for (const guest of this.sockets()) {
        const state = this.state(guest);
        if (state.role === "guest" && state.generation === previous.generation) {
          this.closeOne(guest, "PC is offline", 1001);
        }
      }
    }
    this.closeOne(ws, message, code, notify);
  }

  private send(ws: WebSocket, message: object): boolean {
    try {
      if (ws.readyState !== WebSocket.OPEN) throw new Error("Socket closed");
      ws.send(JSON.stringify(message));
      return true;
    } catch {
      this.remove(ws, "Signaling connection closed", 1011, false);
      return false;
    }
  }

  private sweep(): void {
    const now = Date.now();
    for (const ws of this.sockets()) {
      const state = this.state(ws);
      if (state.role === "closed") continue;
      if (ws.readyState !== WebSocket.OPEN) {
        this.remove(ws, "Signaling connection closed", 1001, false);
      } else if (state.expires <= now) {
        const authenticating = state.role === "pending" || state.role === "verifying";
        this.remove(ws, authenticating ? "Authentication challenge expired" : "Signaling session expired");
      }
    }
    const host = this.host();
    for (const ws of this.sockets()) {
      const state = this.state(ws);
      if (state.role === "guest" && state.generation !== host?.state.generation) {
        this.closeOne(ws, "PC is offline", 1001);
      }
    }
  }

  private async schedule(): Promise<void> {
    let earliest = Infinity;
    for (const ws of this.sockets()) {
      const state = this.state(ws);
      if (state.role !== "closed") earliest = Math.min(earliest, state.expires);
    }
    if (Number.isFinite(earliest)) {
      await this.ctx.storage.setAlarm(earliest);
    } else {
      await this.ctx.storage.deleteAlarm();
    }
  }

  async fetch(request: Request): Promise<Response> {
    return this.ctx.blockConcurrencyWhile(async () => {
      const room = signalRoom(request);
      const allowed = allowedOrigin(request);
      if (!room || this.env.ROOMS.idFromName(room).toString() !== this.ctx.id.toString()) {
        return Response.json({ type: "error", message: "Not found" }, { status: 404 });
      }
      if (!allowed) return Response.json({ type: "error", message: "Origin not allowed" }, { status: 403 });
      if (request.method !== "GET" || request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
        return Response.json({ type: "error", message: "WebSocket upgrade required" }, { status: 426 });
      }
      this.sweep();
      const sockets = this.sockets();
      const pending = sockets.filter((ws) => {
        const role = this.state(ws).role;
        return role === "pending" || role === "verifying";
      }).length;
      if (pending >= MAX_PENDING || sockets.length >= MAX_SOCKETS) {
        await this.schedule();
        return Response.json({ type: "error", message: "Room connection limit exceeded" }, { status: 429 });
      }
      const pair = new WebSocketPair();
      const challenge = encode(crypto.getRandomValues(new Uint8Array(32)));
      this.ctx.acceptWebSocket(pair[1]);
      pair[1].serializeAttachment({
        role: "pending", allowed, room, challenge, expires: Date.now() + CHALLENGE_MS,
      } satisfies Pending);
      this.send(pair[1], { type: "challenge", challenge });
      await this.schedule();
      return new Response(null, { status: 101, webSocket: pair[0] });
    });
  }

  async webSocketMessage(ws: WebSocket, raw: string | ArrayBuffer): Promise<void> {
    this.sweep();
    const state = this.state(ws);
    if (state.role === "closed") return;
    const message = parseMessage(raw);
    if (!message) {
      this.remove(ws, "Invalid signaling message");
    } else if (state.role === "pending") {
      await this.authenticate(ws, state, message);
    } else if (state.role === "verifying" || message.type !== "signal") {
      this.remove(ws, "Already authenticated");
    } else {
      this.route(ws, state, message);
    }
    await this.schedule();
  }

  private async authenticate(ws: WebSocket, pending: Pending, message: ClientMessage): Promise<void> {
    // Consume the challenge before any await, including unsuccessful proofs.
    ws.serializeAttachment({ role: "verifying", expires: pending.expires });
    if (message.type === "host" && pending.allowed === "host") {
      const valid = await verifyHost(pending.room, pending.challenge, message);
      if (!valid || Date.now() >= pending.expires || ws.readyState !== WebSocket.OPEN ||
        this.state(ws).role !== "verifying") {
        this.closeOne(ws, "Host authentication failed");
        return;
      }
      const old = this.host();
      if (old) this.remove(old.ws, "Host replaced", 1001);
      ws.serializeAttachment({
        role: "host", generation: randomId(), expires: Date.now() + HOST_MS,
        bytes: 0, offers: 0,
      } satisfies Host);
      this.send(ws, { type: "host-ready" });
      return;
    }
    if (message.type !== "guest" || pending.allowed !== "guest") {
      this.closeOne(ws, "Authentication role not allowed");
      return;
    }
    const host = this.host();
    if (!host) {
      this.closeOne(ws, "PC is offline");
      return;
    }
    const guests = this.sockets().filter((socket) => this.state(socket).role === "guest");
    if (guests.length >= MAX_GUESTS) {
      this.closeOne(ws, "Room guest limit exceeded");
      return;
    }
    const sid = randomId();
    ws.serializeAttachment({
      role: "guest", generation: host.state.generation, sid,
      expires: Date.now() + GUEST_MS, offered: false, answered: false,
    } satisfies Guest);
    this.send(ws, { type: "guest-ready", sid });
  }

  private route(ws: WebSocket, state: Host | Guest, message: Envelope): void {
    if (state.role === "guest") {
      if (message.kind !== "offer" || message.sid !== state.sid || state.offered) {
        this.remove(ws, "Offer not allowed");
        return;
      }
      const host = this.host();
      if (!host || host.state.generation !== state.generation) {
        this.remove(ws, "PC is offline", 1001);
        return;
      }
      const bytes = new TextEncoder().encode(JSON.stringify(message)).byteLength;
      if (host.state.bytes + bytes > HOST_BYTE_BUDGET || host.state.offers >= HOST_OFFER_BUDGET) {
        this.remove(host.ws, "Host signaling budget exceeded; reconnect", 1013);
        return;
      }
      host.state.bytes += bytes;
      host.state.offers++;
      state.offered = true;
      state.kid = message.kid;
      ws.serializeAttachment(state);
      host.ws.serializeAttachment(host.state);
      this.send(host.ws, message);
      return;
    }
    if (message.kind !== "answer") {
      this.remove(ws, "Answer not allowed");
      return;
    }
    const guest = this.sockets().find((socket) => {
      const candidate = this.state(socket);
      return candidate.role === "guest" && candidate.generation === state.generation &&
        candidate.sid === message.sid;
    });
    const target = guest ? this.state(guest) : null;
    if (!guest || target?.role !== "guest" || !target.offered ||
      target.answered || message.kid !== target.kid) {
      this.remove(ws, "Answer session not allowed");
      return;
    }
    target.answered = true;
    guest.serializeAttachment(target);
    this.send(guest, message);
  }

  async webSocketClose(ws: WebSocket): Promise<void> {
    this.remove(ws, "Signaling connection closed", 1000, false);
    await this.schedule();
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    this.remove(ws, "Signaling connection failed", 1011, false);
    await this.schedule();
  }

  async alarm(): Promise<void> {
    this.sweep();
    await this.schedule();
  }
}
