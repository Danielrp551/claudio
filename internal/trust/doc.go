// Package trust decides how a message from another person enters a session.
//
// The rule that orders everything else: trust is granted by the receiver, never
// claimed by the sender. A workspace owner proposes a default and cannot grant
// trust on somebody else's machine. See ADR-0006.
//
// Three levels, resolved by taking the most restrictive value across the
// workspace default, the member setting, the local policy, and any per session
// override, with the ordering source < collaborator < peer:
//
//	peer          arrives as if from another of your own sessions
//	collaborator  arrives marked as another person, framed as a request. The default
//	source        arrives as data, with instructions not to follow it
//
// What changes between levels is the framing applied to the text as it enters
// the inbox. Session permissions apply identically at every level. Trust changes
// framing, never permissions.
package trust
