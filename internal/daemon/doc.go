// Package daemon is the connector that runs once per machine.
//
// It holds the relay connection, resolves trust, routes messages, owns the
// delivery queue, and supervises ghost child processes as the remote roster
// changes. See ADR-0004 for why the intelligence lives here and not in the
// children.
//
// The daemon also keeps an index of the registry records its children wrote,
// stored outside the Claude Code sessions directory, and removes on startup any
// record in that index that no longer belongs to a live child. No invented
// fields are ever written into the sessions directory itself, because that
// directory belongs to Claude Code.
package daemon
