package workspace

import (
	"time"

	"github.com/Danielrp551/claudio/internal/identity"
)

// Roles a member can hold.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Member states.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

// Conversation kinds. Only direct is used in version one. Channels and threads
// are values of this same field rather than new tables, which is what keeps
// version two from being a migration. See ADR-0007.
const (
	KindDirect  = "direct"
	KindChannel = "channel"
	KindThread  = "thread"
)

// Delivery states, in the order they normally progress.
//
// They exist as separate values because conflating them is how a tool ends up
// lying to its user. A relay acknowledgement is not proof that an agent read
// anything, and a successful write into a session inbox is not either: Claude
// Code discards a frame it does not accept without reporting anything back. See
// ADR-0007.
const (
	DeliveryQueued      = "queued"
	DeliverySentToRelay = "sent_to_relay"
	DeliveryReceived    = "received_by_connector"
	DeliveryDelivered   = "delivered_to_agent"
	DeliveryHeld        = "held"
	DeliveryRefused     = "refused"
	DeliveryFailed      = "failed"
	DeliveryExpired     = "expired"
)

// Workspace is a group of people whose Claude Code sessions can reach each other.
type Workspace struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"ownerId"`
	CreatedAt time.Time `json:"createdAt"`

	// DefaultTrust is what the owner proposes for new members. It is a proposal
	// rather than a setting: the receiving machine always resolves trust by
	// taking the most restrictive value. See ADR-0006.
	DefaultTrust string `json:"defaultTrust"`
}

// Member is a person in a workspace.
//
// Revocation is a state rather than a deletion, so a revoked member can be
// listed and audited, and so revoking one rotates nothing for anybody else.
type Member struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspaceId"`
	Person      string          `json:"person"`
	Identity    identity.Public `json:"identity"`
	Role        string          `json:"role"`
	Trust       string          `json:"trust"`
	Status      string          `json:"status"`
	InvitedBy   string          `json:"invitedBy,omitempty"`
	JoinedAt    time.Time       `json:"joinedAt"`
	RevokedAt   *time.Time      `json:"revokedAt,omitempty"`
}

// Active reports whether this member may currently do anything.
func (m Member) Active() bool { return m.Status == StatusActive }

// Invitation is a single use voucher redeemed for an individual membership.
//
// The code itself is never stored. Knowing a code grants no access on its own,
// it only lets somebody ask to join, and what they receive is a membership with
// its own key that can be revoked without touching anybody else. That is the
// difference between an invitation and a shared secret.
type Invitation struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	CodeHash    string     `json:"codeHash"`
	CreatedBy   string     `json:"createdBy"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	MaxUses     int        `json:"maxUses"`
	Uses        int        `json:"uses"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
}

// Usable reports whether an invitation can still be redeemed at a point in time.
func (i Invitation) Usable(now time.Time) bool {
	switch {
	case i.RevokedAt != nil:
		return false
	case !i.ExpiresAt.IsZero() && now.After(i.ExpiresAt):
		return false
	case i.MaxUses > 0 && i.Uses >= i.MaxUses:
		return false
	default:
		return true
	}
}

// Machine is one computer belonging to a person. It is the thing that runs a
// connector and the thing that goes offline.
type Machine struct {
	ID               string    `json:"id"`
	MemberID         string    `json:"memberId"`
	Hostname         string    `json:"hostname"`
	OS               string    `json:"os"`
	ConnectorVersion string    `json:"connectorVersion"`
	LastSeen         time.Time `json:"lastSeen"`
}

// Session is a Claude Code session somebody has chosen to expose.
//
// Exposure is per session and opt in. Nothing about a machine is published
// because a connector runs on it.
type Session struct {
	ID        string `json:"id"`
	MachineID string `json:"machineId"`
	MemberID  string `json:"memberId"`

	// Profile is the configuration directory the session belongs to. Without it
	// two identically named sessions from different profiles on one machine
	// cannot be told apart, which is a real situation. See ADR-0007.
	Profile string `json:"profile,omitempty"`

	Name     string    `json:"name"`
	CWD      string    `json:"cwd,omitempty"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
}

// Conversation groups messages. A direct message is a conversation of one kind
// rather than a separate entity, which is what lets channels and threads arrive
// later without a migration.
type Conversation struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Kind        string    `json:"kind"`
	ParentID    string    `json:"parentId,omitempty"`
	Topic       string    `json:"topic,omitempty"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Message is one piece of text moving between sessions.
type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`

	// Seq counts within a conversation rather than globally, which is what lets
	// a gap be detected on reconnect without depending on clocks belonging to
	// different machines.
	Seq int64 `json:"seq"`

	SenderMemberID  string `json:"senderMemberId"`
	SenderSessionID string `json:"senderSessionId"`
	Body            string `json:"body"`

	// TrustAtSend freezes the trust level in force when this was sent, so
	// revoking somebody later does not retroactively rewrite how their past
	// messages were interpreted.
	TrustAtSend string `json:"trustAtSend"`

	CreatedAt time.Time `json:"createdAt"`
}

// DeliveryRecord tracks one message towards one session.
type DeliveryRecord struct {
	ID              string    `json:"id"`
	MessageID       string    `json:"messageId"`
	TargetSessionID string    `json:"targetSessionId"`
	State           string    `json:"state"`
	Attempts        int       `json:"attempts"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Error           string    `json:"error,omitempty"`
}

// Artifact is modelled and not implemented. Version three attaches a diff, a
// file, or a finding to a message, and the relation existing now is what keeps
// that from being a migration.
type Artifact struct {
	ID          string `json:"id"`
	MessageID   string `json:"messageId"`
	Kind        string `json:"kind"`
	BlobRef     string `json:"blobRef"`
	Permissions string `json:"permissions"`
}
