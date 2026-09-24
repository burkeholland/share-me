import { allowedOrigin, hex, signalRoom } from "./protocol";
import type { SignalRoom } from "./room";

export { SignalRoom } from "./room";

export interface Env {
  ASSETS: Fetcher;
  ROOMS: DurableObjectNamespace<SignalRoom>;
  SIGNAL_LIMIT: RateLimit;
  SIGNAL_BUDGET: RateLimit;
}

const CSP = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self' blob: data:",
  "font-src 'self'",
  "connect-src 'self'",
  "worker-src 'self'",
  "frame-src 'self'",
  "manifest-src 'self'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-ancestors 'none'",
  "object-src 'none'",
].join("; ");

function secure(response: Response, origin?: string): Response {
  const headers = new Headers(response.headers);
  const policy = origin
    ? CSP.replace("connect-src 'self'", `connect-src 'self' ${origin.replace(/^http/, "ws")}`)
    : CSP;
  headers.set("Content-Security-Policy", policy);
  headers.set("Referrer-Policy", "no-referrer");
  headers.set("X-Content-Type-Options", "nosniff");
  headers.set("X-Frame-Options", "DENY");
  headers.set("Permissions-Policy", "camera=(), microphone=(), geolocation=()");
  headers.set("Cross-Origin-Resource-Policy", "same-origin");
  headers.set("Strict-Transport-Security", "max-age=31536000");
  headers.set("Cache-Control", "no-store");
  return new Response(response.body, { status: response.status, headers });
}

function errorResponse(message: string, status: number): Response {
  return secure(Response.json({ type: "error", message }, { status }));
}

async function rateAllowed(request: Request, env: Env): Promise<boolean> {
  // Only an opaque, minute-scoped counter key leaves this invocation. No IP,
  // room, Origin, challenge, or other request metadata is written to storage.
  const source = request.headers.get("CF-Connecting-IP") ?? "local";
  const minute = Math.floor(Date.now() / 60_000);
  const digest = await crypto.subtle.digest(
    "SHA-256", new TextEncoder().encode(`${minute}\n${source}`),
  );
  const caller = await env.SIGNAL_LIMIT.limit({ key: hex(new Uint8Array(digest)) });
  if (!caller.success) return false;
  return (await env.SIGNAL_BUDGET.limit({ key: "anonymous-signaling" })).success;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const path = url.pathname.replace(/\\/g, "/").replace(/\/+/g, "/");
    // Never let an asset, redirect, or SPA fallback claim peer-only endpoints.
    if (path.includes("%") || /^\/(?:api|receive)(?:\/|$)/i.test(path)) {
      return errorResponse("Not found", 404);
    }
    if (path === "/healthz" && (request.method === "GET" || request.method === "HEAD")) {
      return secure(new Response(request.method === "HEAD" ? null : '{"ok":true}', {
        headers: { "Content-Type": "application/json; charset=utf-8" },
      }));
    }
    if (/^\/signal(?:\/|$)/i.test(path)) {
      const room = signalRoom(request);
      if (!room) return errorResponse("Not found", 404);
      if (request.method !== "GET") return errorResponse("Method not allowed", 405);
      if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
        return errorResponse("WebSocket upgrade required", 426);
      }
      if (!allowedOrigin(request)) return errorResponse("Origin not allowed", 403);
      if (request.headers.has("Sec-WebSocket-Protocol")) {
        return errorResponse("WebSocket subprotocols are not supported", 400);
      }
      try {
        if (!await rateAllowed(request, env)) {
          const response = errorResponse("Signaling rate limit exceeded", 429);
          response.headers.set("Retry-After", "60");
          return response;
        }
      } catch {
        return errorResponse("Signaling admission unavailable", 503);
      }
      return env.ROOMS.get(env.ROOMS.idFromName(room)).fetch(request);
    }
    if (request.method !== "GET" && request.method !== "HEAD") {
      return errorResponse("Not found", 404);
    }
    if (request.headers.has("Upgrade")) return errorResponse("Not found", 404);
    if (url.protocol === "http:" && url.hostname !== "127.0.0.1" && url.hostname !== "localhost") {
      url.protocol = "https:";
      return secure(Response.redirect(url.toString(), 308));
    }
    const assetURL = new URL(request.url);
    if (assetURL.pathname === "/") assetURL.pathname = "/index.html";
    const response = await env.ASSETS.fetch(new Request(assetURL, request));
    return secure(response, url.origin);
  },
} satisfies ExportedHandler<Env>;
