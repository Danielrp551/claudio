// Package workspace holds the shared model: workspaces, members, invitations,
// conversations, and messages.
//
// The model is shaped so that channels and threads arrive as new values of an
// enum rather than as a migration. A direct message is a conversation of one
// kind, not a separate entity. See ADR-0007.
//
// Identity has four levels, because two are not enough for a person who runs
// several machines and several Claude Code profiles:
//
//	Person --< Machine --< Profile --< AgentSession
//
// An invitation is not a credential. It is a single use voucher redeemed for an
// individual, revocable membership, so knowing the code grants no access on its
// own and revoking one member rotates nothing for the others.
package workspace
