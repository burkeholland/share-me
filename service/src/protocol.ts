export const ID = /^[a-f0-9]{32}$/;
export const MAX_MESSAGE_BYTES = 32 * 1024;
export const CHALLENGE_MS = 10_000;
export const GUEST_MS = 120_000;
export const HOST_MS = 12 * 60 * 60 * 1000;
export const MAX_GUESTS = 8;
export const MAX_PENDING = 8;
export const MAX_SOCKETS = 24;
export const HOST_BYTE_BUDGET = 1024 * 1024;
export const HOST_OFFER_BUDGET = 128;

export interface HostProof {
  type: "host";
  publicKey: string;
  signature: string;
}

export interface Envelope {
  type: "signal";
  sid: string;
  kid: string;
  kind: "offer" | "answer";
  iv: string;
  data: string;
}

export type ClientMessage = HostProof | { type: "guest" } | Envelope;

export function encode(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function decode(value: unknown, length?: number): Uint8Array | null {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]+$/.test(value)) return null;
  if (value.length % 4 === 1) return null;
  try {
    const binary = atob(value.replace(/-/g, "+").replace(/_/g, "/"));
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    if (length !== undefined && bytes.length !== length) return null;
    return encode(bytes) === value ? bytes : null;
  } catch {
    return null;
  }
}

export function randomId(): string {
  return hex(crypto.getRandomValues(new Uint8Array(16)));
}

export function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function fields(value: Record<string, unknown>, expected: string[]): boolean {
  const keys = Object.keys(value);
  return keys.length === expected.length &&
    expected.every((key) => Object.hasOwn(value, key) && typeof value[key] === "string");
}

export function parseMessage(raw: string | ArrayBuffer): ClientMessage | null {
  if (typeof raw !== "string" || new TextEncoder().encode(raw).byteLength >= MAX_MESSAGE_BYTES) {
    return null;
  }
  let value: Record<string, unknown>;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  if (value.type === "guest" && fields(value, ["type"])) return { type: "guest" };
  if (value.type === "host" && fields(value, ["type", "publicKey", "signature"])) {
    const key = decode(value.publicKey, 65);
    if (key?.[0] !== 4 || !decode(value.signature, 64)) return null;
    return value as unknown as HostProof;
  }
  if (value.type !== "signal" ||
    !fields(value, ["type", "sid", "kid", "kind", "iv", "data"]) ||
    !ID.test(value.sid as string) ||
    !(value.kid === "pair" || ID.test(value.kid as string)) ||
    !(value.kind === "offer" || value.kind === "answer") ||
    !decode(value.iv, 12)) return null;
  const ciphertext = decode(value.data);
  if (!ciphertext || ciphertext.byteLength < 16) return null;
  return value as unknown as Envelope;
}

export async function verifyHost(room: string, challenge: string, proof: HostProof): Promise<boolean> {
  const publicKey = decode(proof.publicKey, 65);
  const signature = decode(proof.signature, 64);
  if (!publicKey || publicKey[0] !== 4 || !signature) return false;
  try {
    const digest = await crypto.subtle.digest("SHA-256", publicKey);
    if (hex(new Uint8Array(digest)).slice(0, 32) !== room) return false;
    const key = await crypto.subtle.importKey(
      "raw", publicKey, { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"],
    );
    const message = new TextEncoder().encode(`ShareMe host v1\n${room}\n${challenge}`);
    return await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, key, signature, message);
  } catch {
    return false;
  }
}

export function allowedOrigin(request: Request): "host" | "guest" | null {
  const url = new URL(request.url);
  const loopback = url.hostname === "127.0.0.1" || url.hostname === "localhost";
  if (url.protocol !== "https:" && !(url.protocol === "http:" && loopback)) return null;
  const origin = request.headers.get("Origin");
  if (origin === null) return "host";
  return origin === url.origin ? "guest" : null;
}

export function signalRoom(request: Request): string | null {
  const url = new URL(request.url);
  if (url.search) return null;
  const match = /^\/signal\/([a-f0-9]{32})$/.exec(url.pathname);
  return match?.[1] ?? null;
}
