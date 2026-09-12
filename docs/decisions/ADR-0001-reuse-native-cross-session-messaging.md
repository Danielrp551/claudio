# ADR-0001: Reuse the native cross-session messaging of Claude Code

## Status

Accepted

## Date

2026-09-12

## Context

Claude Code sessions on the same machine already discover and message each other. Claude finds peers
with `ListAgents` and delivers with `SendMessage`. That path works well, users never invoke the tools
by hand, and an arriving message is rendered in the conversation with attribution and a reply
address.

What Claude Code does not provide is a workspace shared by different people. Cross machine messaging
exists, but it travels through Anthropic servers over Remote Control, and Remote Control is scoped to
a single account. There is no mechanism for a session belonging to another person, signed in with
another account, to appear in your `ListAgents`.

So the product needs a control plane of its own. The open question was whether the message itself
should also travel through a channel of our own, or whether the native path could be reused on both
ends.

Every comparable tool surveyed uses MCP tools, hooks, Channels, or a bespoke adapter. None reuses the
native discovery and delivery path.

## Decision

Reuse the native path on both ends.

Incoming messages are delivered into the target session's own inbox, so they arrive exactly like a
message from another of the user's sessions. Outgoing messages are addressed with the native
`SendMessage` tool, to a peer that a local process of ours publishes.

The workspace layer, meaning ownership, invitations, membership, presence, and routing, is built by
this project. Only the last hop on each machine is native.

## Alternatives Considered

### An MCP server as the only surface

- Pros: fully documented and stable API, no dependency on internals, trivial to make cross platform.
- Cons: Claude has to know it should reach for a different tool instead of the one it already uses.
  The experience stops feeling native, which is the whole point of the project.
- Rejected as the only surface. Kept as the fallback transport, see ADR-0008.

### Hooks or Channels

- Pros: Channels is an official way to push external events into a running session.
- Cons: Channels is a research preview, is restricted to an allowlist of plugins, requires a session
  to be started with a specific flag, and is blocked by default for Team and Enterprise
  organizations. It is also one directional in spirit, built for external events rather than for peer
  conversation.
- Rejected. Reconsider if Channels leaves preview with a stable contract.

### A parallel chat, entirely our own

- Pros: no dependency on Claude Code internals at all.
- Cons: the user installs and learns a second thing, and the receiving Claude treats the content as
  tool output rather than as a peer message.
- Rejected. This is what the existing tools do, and it is the gap this project exists to close.

## Consequences

- The delivery half rests on a documented and supported surface, namely the session inbox and its two
  environment variables.
- The discovery half rests on an undocumented surface, namely the on disk session registry. That risk
  is real and is contained by ADR-0008.
- The project inherits, and must respect, the controls Claude Code already gives the user:
  `crossSessionInbound`, `isolatePeerMachines`, and per session permissions. The tool never works
  around them. If a control blocks something, that is reported.
- Message size and burst limits of the native channel become our limits too.
