package daemon

import (
	"context"
	"time"
)

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

// MaxMessageBytes is the largest message this tool carries.
//
// There used to be six limits along the path, none of them coordinated or
// documented: sixteen megabytes on the MCP server's input, four on the control
// channel, one between a daemon and a ghost, eight in the supervisor, four in
// the inbox, and sixteen kilobytes on the join endpoint. The lowest one on the
// send path was the control channel, and going over it made the daemon close the
// connection rather than answer, so a large message failed with "broken pipe"
// and no mention of size at all.
//
// One megabyte is the limit now, checked where a message enters, with an error
// that names it. It is far more than a message between two sessions needs and
// comfortably inside every channel it has to cross, which is the property that
// matters: the limit somebody hits should be the one that was explained to them.
const MaxMessageBytes = 1 << 20

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

	// ReplyTo is an address this transport accepts in Outbound.To.
	//
	// It exists because the framing invites the reader to reply and, without it,
	// there was frequently nobody to reply to. A sender who exposes no session of
	// their own is not in anybody's roster, so a reply had no destination and
	// died in the connector with "could not work out who a message was for". A
	// message that arrived is proof enough that its sender can be reached.
	ReplyTo string `json:"replyTo,omitempty"`
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

	// Health describes whether this transport can currently carry anything.
	//
	// It exists because a connector that has lost its relay looks exactly like a
	// connector whose workspace is quiet, and the difference matters to the
	// person using it. Without this, status reported a healthy workspace with an
	// empty roster while the relay had been down for an hour, and only the log
	// said otherwise.
	Health() TransportHealth

	// Close stops the transport and closes the delivery channel.
	Close() error
}

// TransportHealth is what a transport says about its own connection.
type TransportHealth struct {
	// Connected is whether the transport can carry a message right now.
	Connected bool
	// EverConnected is whether it has ever succeeded. The two differ in the case
	// worth separating: a connector that has never been accepted is misconfigured
	// or unwelcome, and one that was accepted and dropped is waiting.
	EverConnected bool
	// Since is when the current state began.
	Since time.Time
	// Detail is the last reason it could not connect, ready to show somebody.
	Detail string
}
