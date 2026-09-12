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

	// Close stops the transport and closes the delivery channel.
	Close() error
}
