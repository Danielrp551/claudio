// Package ghost is the thin child process that holds one native peer endpoint.
//
// A ghost binds its endpoint, writes its own registry record under its own real
// process id, and relays frames to its parent over stdin and stdout as newline
// delimited JSON. It does no networking, no cryptography, and keeps no state.
// Everything else lives in the daemon. See ADR-0004.
//
// Two properties matter more than anything else here:
//
// A ghost cannot outlive its daemon. When the parent dies, stdin closes, and the
// ghost removes its record and exits, so no orphan peer is left in the user's
// agent list.
//
// Shutdown has a mandatory order, learned from a real failure rather than from
// theory. A probe left an orphan record because its heartbeat rewrote the file
// after cleanup had removed it:
//
//  1. stop accepting connections
//  2. stop the heartbeat and wait for it to finish
//  3. remove the record and the key
//  4. exit
package ghost
