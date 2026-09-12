package relay

import (
	"github.com/Danielrp551/claudio/internal/identity"
	"github.com/Danielrp551/claudio/internal/workspace"
)

// The wire protocol between a connector and a relay, as newline delimited JSON
// over a WebSocket.
//
// The relay routes and queues. It never sees plaintext, because the only part of
// a message it carries is a sealed envelope it has no key for. See ADR-0005.
//
// Connecting goes:
//
//	client  hello      here is the workspace and who I claim to be
//	server  challenge  prove it
//	client  auth       a signature over the challenge
//	server  welcome    you are this member
//
// Signing a fresh challenge rather than sending a token means nothing reusable
// ever travels, so a recording of the exchange grants nothing.
const (
	TypeHello     = "hello"
	TypeChallenge = "challenge"
	TypeAuth      = "auth"
	TypeWelcome   = "welcome"
	TypeSessions  = "sessions"
	TypeRoster    = "roster"
	TypeEnvelope  = "envelope"
	TypeError     = "error"
	TypePing      = "ping"
)

// Message is one frame of the protocol. One struct rather than a union because
// the protocol is small and a single shape is easier to read on both sides.
type Message struct {
	Type string `json:"type"`

	// Hello.
	Workspace string `json:"workspace,omitempty"`
	Signing   []byte `json:"signing,omitempty"`
	Agreement []byte `json:"agreement,omitempty"`
	Person    string `json:"person,omitempty"`

	// Challenge and auth.
	Nonce     []byte `json:"nonce,omitempty"`
	Signature []byte `json:"signature,omitempty"`

	// Welcome.
	MemberID    string `json:"memberId,omitempty"`
	WorkspaceID string `json:"workspaceId,omitempty"`

	// Sessions, sent by a connector to say what its user exposes.
	Machine *workspace.Machine  `json:"machine,omitempty"`
	Exposed []workspace.Session `json:"exposed,omitempty"`

	// Roster, sent by the relay whenever the workspace changes.
	Members []RosterMember  `json:"members,omitempty"`
	Peers   []RosterSession `json:"peers,omitempty"`

	// Envelope.
	To          []byte             `json:"to,omitempty"`
	From        []byte             `json:"from,omitempty"`
	ToSession   string             `json:"toSession,omitempty"`
	FromSession string             `json:"fromSession,omitempty"`
	Payload     *identity.Envelope `json:"payload,omitempty"`

	// Error.
	Reason string `json:"reason,omitempty"`
}

// RosterMember is a person in the workspace, as the relay reports them.
type RosterMember struct {
	ID       string          `json:"id"`
	Person   string          `json:"person"`
	Role     string          `json:"role"`
	Trust    string          `json:"trust"`
	Status   string          `json:"status"`
	Identity identity.Public `json:"identity"`
}

// RosterSession is an exposed session, with enough about its owner to build a
// peer name without a second lookup.
type RosterSession struct {
	ID       string `json:"id"`
	MemberID string `json:"memberId"`
	Person   string `json:"person"`
	Machine  string `json:"machine"`
	Name     string `json:"name"`
	Status   string `json:"status"`
}

// Payload is what travels inside a sealed envelope. The relay never sees it.
type Payload struct {
	Text string `json:"text"`
	// FromSession is the name of the session that wrote the message.
	FromSession string `json:"fromSession"`
	// FromMode is the permission class of the sending session, carried end to
	// end so the receiver can act on it. See ADR-0006.
	FromMode string `json:"fromMode"`
	// ToSession is the identifier of the session it is for.
	ToSession string `json:"toSession"`
	MsgID     string `json:"msgId"`
	// Seq counts within the conversation between these two sessions, so a gap
	// can be noticed on reconnect without trusting anybody's clock.
	Seq int64 `json:"seq"`
}
