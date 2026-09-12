package daemon

import "context"

// RemoteSession is a Claude Code session on somebody else's machine, as the
// transport reports it.
//
// The identity is spread across four levels because two are not enough. One
// person has several machines, and on each machine several Claude Code profiles,
// so a name alone cannot tell two sessions apart. See ADR-0007.
type RemoteSession struct {
	// ID is the stable identifier the workspace assigns. It survives the remote
	// session being renamed and is what messages are addressed to.
	ID string `json:"id"`

	Person  string `json:"person"`
	Machine string `json:"machine"`
	Session string `json:"session"`

	// Status is idle, busy, or offline.
	Status string `json:"status"`
}

// Outbound is a message leaving this machine for a remote session.
type Outbound struct {
	// To is the RemoteSession.ID of the destination.
	To string `json:"to"`
	// Text is what the local Claude wrote.
	Text string `json:"text"`
	// FromSession is the name of the local Claude Code session that wrote it.
	FromSession string `json:"fromSession"`
	// FromMode is the permission class of that session, carried end to end so
	// the receiver can act on it. See ADR-0006.
	FromMode string `json:"fromMode"`
	// MsgID correlates this message with its delivery states, and appears in the
	// logs of both machines.
	MsgID string `json:"msgId"`
}

// Delivery is a message arriving from a remote session.
type Delivery struct {
	// From is the remote session that sent it.
	From RemoteSession `json:"from"`
	// ToSession is the name of the local Claude Code session it is for.
	ToSession string `json:"toSession"`
	// Text is what the remote Claude wrote.
	Text string `json:"text"`
	// FromMode is the permission class the sender declared.
	FromMode string `json:"fromMode"`
	MsgID    string `json:"msgId"`

	// ProposedTrust is what the workspace suggests for this sender. It is a
	// proposal and nothing more: the receiving machine resolves the level it
	// actually uses by taking the most restrictive of every opinion, including
	// its own. See ADR-0006.
	ProposedTrust string `json:"proposedTrust,omitempty"`

	// Workspace is the slug the message came through, which the framing names so
	// the reader knows where a stranger came from.
	Workspace string `json:"workspace,omitempty"`
}

// ExposedSession is a local Claude Code session this machine shares.
//
// Exposure is per session and opt in. Nothing is published because a connector
// happens to run on the machine.
type ExposedSession struct {
	// ID is stable for this machine and session, so reconnecting replaces an
	// entry rather than adding a second one.
	ID     string `json:"id"`
	Name   string `json:"name"`
	CWD    string `json:"cwd,omitempty"`
	Status string `json:"status"`
}

// Transport carries messages between machines.
//
// It is declared here, where it is consumed, rather than beside its
// implementation. The daemon says what it needs, and a relay client, or a
// loopback used in tests, satisfies it.
type Transport interface {
	// Send hands a message to the network. A nil error means the transport
	// accepted it, not that anybody read it.
	Send(ctx context.Context, msg Outbound) error

	// Deliveries yields messages arriving from remote sessions. It closes when
	// the transport is closed.
	Deliveries() <-chan Delivery

	// Roster returns the remote sessions currently reachable. The daemon polls
	// it rather than subscribing, because the set changes slowly and a snapshot
	// is simpler to reason about than a stream of edits.
	Roster() []RemoteSession

	// Expose publishes the local sessions this machine shares. The daemon calls
	// it whenever that set changes, and a transport is expected to republish it
	// after a reconnect without being asked again.
	Expose(sessions []ExposedSession)

	// Close stops the transport and closes the delivery channel.
	Close() error
}
