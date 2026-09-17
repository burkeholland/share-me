// Package shortcut implements a private-LAN, public-key-only SSH protocol for
// Apple's Run Script over SSH action. It never executes commands or processes.
//
// Bind Config.LocalIP to the selected private IPv4 interface and explicitly use
// DefaultPort in production (Port zero requests an ephemeral test port).
// The parent owns the persisted, default-off enable setting and calls Start
// only when enabled. Status reports running state, not the parent's preference.
// Disable with Close; create a new Server to enable again after Close.
// New checks stored keys against IsDeviceAllowed and atomically persists the
// removal of mappings for forgotten browser devices, reclaiming their quota.
// The callback must be ready before New; a pruning failure prevents startup.
// CreateSetup is only for delivery over an already-authenticated local channel.
// Setup.Name is the PC's display name; the paired browser deviceID owns access.
// Pin Setup.Fingerprint on the SSH client; enrollment is not host authentication.
// Enrollment expires after five minutes and is committed only after SSH has
// verified possession of the offered private key. The setup connection cannot
// perform transfers. Persistent state requires Windows user-scoped DPAPI.
//
// Each session accepts one exec operation, described in protocol.go. Input is
// stdin, never shell syntax or a client-supplied filesystem path. Upload input
// is streamed lazily, with no reads before Handle is called. x/crypto/ssh's
// receive window is bounded (2 MiB per accepted channel in the pinned version);
// connection and channel limits bound that transport buffering.
//
// Handle must observe its context, consume upload Body through EOF before
// returning a successful response, and finish validation/scanning/saving before
// producing a receipt. Other request bodies are validated and bounded before
// dispatch. Body is only valid until Handle returns and must not be read
// concurrently. A handler may reject an upload without reading its body.
// Responses must contain valid UTF-8 JSON of at most 64 KiB and an HTTP-style
// StatusCode in [200,599]. StatusCode is not written as an HTTP response:
// application errors, including 4xx/5xx JSON, exit zero so Shortcuts can inspect
// them. Transport, protocol, and handler failures produce stderr and exit one.
//
// Close is terminal and joins network and handler workers; cancellation and
// channel deadlines close the affected connection to unblock SSH reads/writes.
// IsDeviceAllowed must be a prompt, concurrency-safe decision callback; after
// New returns it may read Status, but must not synchronously wait for Close or
// its own requests.
// Handle must not synchronously Close its own server. OnChange and OnError are
// bounded, asynchronous, best-effort notifications, invoked without server
// locks. They must return promptly and may call Close. Notification callbacks
// are deliberately not joined by Close, allowing that reentrant use.
//
// This implementation has loopback SSH integration coverage, not physical
// iPhone interoperability certification.
package shortcut
