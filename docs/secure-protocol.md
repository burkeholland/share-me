# Secure transfer transports

The hosted HTTPS site serves browser code and a bounded Cloudflare Durable Object
signaling service. It does not serve, store, or relay file contents. File and text
requests use HTTP/1.1 streams carried inside authenticated WebRTC data channels.
No STUN or TURN servers are configured. Production peers must use the selected
private IPv4 interface and reject a selected remote address outside that subnet.
Loopback and an HTTP signaling origin are permitted only by explicit test options.

## Signaling

The socket URL is `/signal/{room}`. IDs are 32 lowercase hexadecimal characters.
Binary WebSocket messages are prohibited. JSON messages are at most 32 KiB.
A room has one Windows host and at most eight guests.

The host has a persistent P-256 identity key. `room` is the first 32 hexadecimal
characters of SHA-256 of its 65-byte uncompressed public key. Private state is
protected with Windows DPAPI, not stored in plaintext configuration.

1. The broker sends every new socket
   `{"type":"challenge","challenge":"<32 random bytes, base64url>"}`.
2. The Windows host replies with
   `{"type":"host","publicKey":"<65 bytes, base64url>","signature":"<64 bytes, base64url>"}`.
   The signature is ECDSA/P-256/SHA-256, fixed-width P1363 `r || s`, over UTF-8
   `ShareMe host v1\n{room}\n{challenge}`. The broker validates the room and proof,
   then replies `{"type":"host-ready"}`. Challenges expire after ten seconds.
   A successfully authenticated replacement host closes the previous host and
   its signaling guests.
3. A phone replies `{"type":"guest"}`. With a host online, the broker assigns a
   fresh session and sends `{"type":"guest-ready","sid":"<id>"}`. Without a host,
   it sends `{"type":"error","message":"PC is offline"}` and closes.
4. The phone gathers a complete, non-trickle WebRTC offer and sends one envelope:
   `{"type":"signal","sid":"...","kid":"pair or device ID","kind":"offer","iv":"...","data":"..."}`.
   The host replies with the same envelope shape and `kind:"answer"`.
   Only a guest may send an offer, and only its room's authenticated host may
   send that guest's answer. Only one offer and one answer are allowed per guest.

`iv` is a fresh 12-byte nonce, base64url. `data` is AES-256-GCM ciphertext plus its
16-byte tag, base64url. Additional authenticated data is the UTF-8 string
`ShareMe signal v1\n{room}\n{sid}\n{kid}\n{kind}`.

The plaintext offer is `{"sdp":"...","name":"iPhone"}`; the answer is
`{"sdp":"..."}`. SDP is never sent unencrypted through the broker. Encrypting the
entire offer/answer authenticates their DTLS fingerprints, not just device names.
The host must reject duplicate session IDs, unknown/revoked device IDs, malformed
envelopes, and offers that cannot use the permitted network.

The browser verifies the HTTPS origin. Production must not disable certificate
validation. A signaling interruption must not abort an already working direct
file transfer. Reconnection failures must be visible.

## Pairing

Windows explicitly creates a one-use 32-byte invitation, valid until consumed,
cancelled, replaced, or the app stops; there is no countdown or code to type.
The QR URL is `{origin}/#room={room}&pair={base64url invitation}`.
The invitation is the AES-GCM signaling key for `kid:"pair"`. URL fragments are
not transmitted to the web server. The hosted site must use no analytics,
third-party scripts, remote fonts, or referrer leakage.

The phone creates an ordered, reliable `shareme.control` channel before its offer.
Once connected, Windows displays a native pairing approval showing the proposed
name and actual selected remote IP. The name is not proof of identity.

After approval, Windows generates and durably saves a separate device ID and
32-byte device secret, consumes the invitation, and sends over the DTLS channel:
`{"type":"paired","id":"<device ID>","name":"<name>","secret":"<base64url secret>"}`.
The device secret is NOT derived from or sent through the invitation/signaling
channel. The browser imports it as a non-extractable AES-GCM CryptoKey and stores
it with the device and room IDs in IndexedDB, then replies `{"type":"paired-ack"}`.
Only then is that peer authorized; Windows sends `{"type":"ready"}`.
Declined pairing sends `{"type":"error","message":"Connection declined on PC"}`.

Returning browsers encrypt signaling using their device secret and device ID.
Their control channel receives `{"type":"ready"}` after authentication. Pairing
does not approve future uploads: each incoming file/text still requires the
existing Windows transfer decision.

Revocation deletes the credential before closing all that device's peers and
request streams. Device credentials identify a browser, not uncloneable hardware.
Windows can rename and revoke devices. A site-data reset requires pairing again.

## HTTP streams over data channels

Only authorized peers may create ordered, reliable channels named
`shareme.http.<32-hex request ID>`. Each represents one HTTP/1.1 connection with
`Host: peer.shareme`, `Connection: close`, and no pipelining. The connection's
device ID and remote IP come from the authenticated peer, never HTTP headers.
There are at most four simultaneous request channels per peer.

Binary channel messages carry up to 16 KiB of raw stream bytes. Each direction
starts with 64 KiB of credit. The receiver returns credit only when its stream
consumer reads bytes, using a text message `credit:<decimal byte count>`.
Credit must never exceed the original window; over-credit, over-window data,
oversized frames, and malformed control messages close the connection.

`fin` is an ordered half-close. A receiver acknowledges it with `fin-ack`; it
retains already received bytes until read, then returns EOF. Both directions may
half-close independently. HTTP responses must arrive intact before the server
closes the data channel. Abort closes unblock all readers and writers; deadlines
interrupt only the affected operation and can be reset, like a TCP connection.
HTTP request bodies end with Content-Length or chunked framing, not a FIN: an
early FIN would cancel Go's request context during approval or scanning.
Only active request labels are retained. DTLS/SCTP prevents wire replay; a fresh
authenticated channel still passes HTTP authorization even if it reuses a label.
Polling has no lifetime request cap.
Backpressure must bound memory even while an iPhone save dialog is open.

Existing `/api/session`, `/api/request`, `/api/request/{id}`, `/api/upload`, and
`/api/text` semantics remain unchanged, including preflight decline/cancellation,
Defender scanning, attachment marking, and atomic publication.

New device-scoped routes:

- `GET /api/outbox`: `{"items":[{"id","kind","name","size","createdAt"}]}`.
- `GET /api/outbox/{id}`: attachment response for that authenticated device only.
  Text offers use UTF-8 `text/plain`; files use `application/octet-stream`.
  HEAD and Range support allow safe retries. A transfer is not advertised as
  saved merely because the phone pressed Accept Transfer.

Snapshots are queued locally for a specific paired device, survive app restarts,
and remain available for retry until removed on Windows. No arbitrary local path
or another recipient's metadata is exposed. Missing/wrong-device offers return
the same not-found response.

The phone uses an origin-local service worker for streamed saving, not a PWA or
push registration. `/receive/` must return 404 from the public Worker: only the
browser's service worker may fulfill its random, short-lived download URLs.

## Native transport API

Package `internal/peer` owns signaling, identity storage, WebRTC, and stream
connections, not application file handlers:

```go
type Config struct {
    DataDir, ServiceURL, LocalIP string
    OnChange func()
    OnError func(error)
    AllowLoopback bool // tests only; also permits loopback HTTP signaling
}
type Device struct { ID, Name string; Connected bool; CreatedAt time.Time }
type PairRequest struct { ID, Name, Source string; CreatedAt time.Time }
func New(Config) (*Engine, error)
func (*Engine) Start(context.Context) error
func (*Engine) Close() error
func (*Engine) Listener() net.Listener // Addr().String() == "peer.shareme"
func (*Engine) Room() string
func (*Engine) PhoneURL() string // public URL with room, no invitation
func (*Engine) BeginPairing() (string, error) // private invitation URL
func (*Engine) CancelPairing()
func (*Engine) PairRequests() []PairRequest
func (*Engine) DecidePair(id string, accept bool) error
func (*Engine) Devices() []Device
func (*Engine) RenameDevice(id, name string) error
func (*Engine) RevokeDevice(id string) error
func (*Engine) Status() (connected bool, message string)
```

Device and PairRequest fields have lower-camel-case JSON tags.

Accepted listener connections implement `PeerID() string`. RemoteAddr is a
parseable `net.TCPAddr` with the selected peer address. Start returns after setup;
broker connection/reconnection status is reported through Status and OnChange.
Close is idempotent and joins owned workers. No network call holds the engine
state mutex, and no callbacks run while that mutex is held.
