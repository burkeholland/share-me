# ShareMe public signaling service

This Cloudflare Worker serves the first-party phone application and **encrypted
WebRTC signaling only**. It has no file-transfer, upload, download, proxy, TURN,
SDP-decryption, analytics, or credential-storage endpoint. See
`..\docs\secure-protocol.md` for the wire contract.

## Local validation

From `service\`, with Node.js 22 or newer:

```powershell
npm ci
npm run check
npm test
npm run dev
```

The frontend owner stages the phone application into `public\` (`index.html`,
its scripts/styles, and service worker). These static assets contain no
PC-specific configuration or credentials. Tests use separate assets under
`test\fixtures\public` and do not modify the production assets. Development uses
`http://127.0.0.1:8787`; the browser Origin must match that exact address and port.
HTTP is otherwise rejected for signaling. No login is required for local tests.
Public static HTTP requests redirect to HTTPS; transfer paths still return 404.

Tests run in real workerd with Cloudflare's Vitest plugin (the successor to
`vitest-pool-workers`), SQLite-backed Durable Objects, hibernating WebSockets,
WebCrypto, asset serving, and rate-limit bindings. They exercise the handshake,
replay/shape rejection, pairing/returning-device routing, isolation, host
replacement, quotas, alarms, socket cleanup, and actual hibernation eviction.
Ciphertext tampering with a valid envelope is intentionally opaque to the broker;
the tests demonstrate that only a peer possessing the AES-GCM key detects it.

## Routes and configuration

| Route | Behavior |
| --- | --- |
| `GET /`, `/index.html` | `ASSETS` phone app; strict first-party CSP |
| `GET /healthz` | `{"ok":true}` (HEAD also supported) |
| `GET /signal/{room}` | Same-origin WebSocket; 32 lowercase hexadecimal room |
| `/api`, `/api/*`, `/receive`, `/receive/*` | Always 404, for every method, even if an asset exists |
| Unknown static path | 404; no SPA fallback |

`src\index.ts` exports the default Worker and the `SignalRoom` Durable Object.
`wrangler.jsonc` binds `ROOMS`, `ASSETS`, `SIGNAL_LIMIT`, and `SIGNAL_BUDGET`.
Migration `v1` creates SQLite class `SignalRoom`. The asset binding is
`public\`, with `run_worker_first: true`, `html_handling: "none"`, and
`not_found_handling: "none"`. Do not disable Worker-first routing or configure an
SPA fallback: those settings enforce the transfer boundary before static serving.
Percent-encoded static paths are rejected rather than normalized into reserved
routes. Inline scripts/styles, remote scripts/fonts, and analytics are prohibited.
The CSP explicitly names the same-origin WebSocket endpoint for browsers that do
not include WebSockets in `connect-src 'self'`; same-origin frames are allowed for
service-worker-backed streamed downloads, but other sites cannot frame the app.

Native hosts must omit Origin; browser guests must supply the exact request
origin. No Origin is not host authentication: the single-use ten-second
challenge still requires the room-bound P-256/SHA-256/P1363 proof. An authenticated
replacement closes the old host and its guests; invalid proofs never replace it.
Guests only offer for their assigned session. Hosts only answer an existing
offered session, with the identical key identifier. Neither side can retry its
one permitted envelope on the same guest session.

HTTP admission/authentication failures have explicit JSON
`{"type":"error","message":"..."}` responses (400/403/404/405/426/429/503).
After an upgrade, protocol/authentication/quota errors send the same shape and
close the socket: 1008 for violations/expiry, 1001 for replacement/offline,
1013 for host-budget exhaustion. No private values are interpolated into errors.

## Resource and privacy limits

- One authenticated host, eight guests, eight pending proofs per room. At most
  24 runtime sockets, including sockets still completing a close handshake.
- Only text JSON strictly smaller than 32 KiB; exact message field sets,
  canonical unpadded base64url, 12-byte IV, and at least a 16-byte GCM tag.
- Pending challenges expire after ten seconds; guest signaling sessions after
  two minutes; a host connection after twelve hours. Clients reconnect
  explicitly. Expiration/closure affects signaling, never an established direct
  WebRTC transfer.
- Each host connection receives at most 128 offers or 1 MiB of serialized offer
  frames, whichever comes first. This lifetime budget bounds output even if the
  host does not read. A guest receives at most one answer. There are no
  application queues, retransmission buffers, timers, or keepalive messages.
- The Worker checks rate limits **before creating a room**: 30 connection
  attempts/minute/source and 300/minute across the service, per Cloudflare
  location. Source counters receive only a SHA-256 minute-scoped source key.
  The Worker never stores the IP or other request metadata; counter keys are
  short-lived, pseudonymous rate-limit identifiers, not device identities.
  Native headers cannot establish identity. Cloudflare controls the production
  `CF-Connecting-IP` header; local test callers can supply synthetic sources.
- Limits are intentionally approximate and per-location, not an account-wide
  billing cap or a DDoS guarantee. Shared-network callers share their admission
  quota; a distributed attack may exhaust the anonymous service-wide allowance.
  Configure account billing notifications/WAF separately if required.
- Hibernation attachments hold only transient routing flags, random session/
  generation IDs, challenge data until consumed, and expiry/budget numbers.
  They never contain proof keys/signatures, ciphertext, SDP, credentials,
  invitation secrets, names, or file content. SQLite has no application tables
  or records; its only stored value is the next cleanup alarm.
- Socket removal clears its attachment immediately. Host removal also clears
  its guests. The final socket removes the alarm. No frames, credentials,
  filenames, or SDP are logged; Workers Observability and preview URLs are off.
  Cloudflare infrastructure still handles ordinary connection metadata; this is
  not a promise of anonymity from the hosting provider.

## Deployment

The current deployment is `https://shareme-signaling.burkeholland.workers.dev`.
Configuration uses the account's default plan limits rather than requesting a
paid-only custom CPU limit. Public file routes still return 404. The generated
asset manifest is excluded from publication with `public\.assetsignore`.

No deployment or Cloudflare login is performed by tests. Choose an available
Worker name in `wrangler.jsonc` and ensure rate-limit namespace IDs `731001` and
`731002` are not shared with unrelated Workers in the Cloudflare account.

```powershell
Set-Location X:\burkeholland\share-me\service
.\node_modules\.bin\wrangler.cmd whoami
# Only when an operator approves authentication, if needed:
.\node_modules\.bin\wrangler.cmd login
npm run check
npm test
# Ensure the actual first-party phone assets have been staged first:
.\node_modules\.bin\wrangler.cmd deploy --dry-run
# Only after explicit deployment approval:
npm run deploy
```

The first deploy creates the `ROOMS` SQLite namespace through the migration.
No secrets, database setup, public relay, or external storage is needed. Use the
resulting `https://<worker>.<account>.workers.dev` URL as the native service URL;
guests use that exact origin. Test `/healthz`, phone loading, and 404 responses
for `/api/session` and `/receive/test`, then test LAN pairing with real devices.
Never log invitation URLs, frames, or payloads while performing those checks.

Implementation follows Cloudflare's current documentation for
[hibernating WebSockets](https://developers.cloudflare.com/durable-objects/best-practices/websockets/),
[rate limits](https://developers.cloudflare.com/workers/runtime-apis/bindings/rate-limit/),
and [Vitest integration](https://developers.cloudflare.com/workers/testing/vitest-integration/write-your-first-test/).
