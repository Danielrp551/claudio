// Package transport carries messages between machines.
//
// The interface exists because there will be more than one implementation. The
// first is a client for a self hosted relay, chosen over peer to peer because a
// relay gives NAT traversal, presence, deferred delivery, and single point
// revocation, while end to end encryption gives the security property people
// actually want from peer to peer. See ADR-0005.
//
// Whatever the implementation, the relay never sees plaintext. Keys do not leave
// the connector, and that has to be true in the code rather than in the README.
package transport
