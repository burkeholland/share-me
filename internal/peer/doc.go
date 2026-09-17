// Package peer implements the secure-protocol.md native transport contract.
//
// New requires a selected active private IPv4 interface and an HTTPS service
// origin. Persistent state is encrypted with the current Windows user's DPAPI;
// non-Windows production construction fails closed. AllowLoopback is only a
// network-policy test option and never bypasses identity protection.
//
// Start launches authenticated signaling and reports asynchronous availability
// through Status, OnChange, and OnError. Callbacks can query the engine, may run
// concurrently, and should return promptly. Existing LAN streams survive broker
// reconnections. Close cancels signaling and peers and joins owned workers.
//
// Serve HTTP on Listener, obtaining the authenticated device from a connection's
// PeerID method (for example in http.Server.ConnContext), not from request headers.
// Stream Close flushes FIN acknowledgement; engine close and revocation abort
// streams. Pairing authorizes a device, not any individual file transfer.
package peer
