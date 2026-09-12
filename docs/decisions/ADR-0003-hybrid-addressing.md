# ADR-0003: Hybrid addressing with one process per native peer

## Status

Accepted

## Date

2026-09-12

## Context

For a remote session to appear in a user's `ListAgents`, a local process must publish a record in the
session registry and hold the endpoint that record points to. We call such a process a ghost.

The open question was how many ghosts are needed. If one process could publish several records, one
process could represent a whole workspace at full fidelity. An experiment settled it.

A single process published three records, each pointing at an endpoint it owned, all three declaring
the same real process id in the record body. Claude Code opened a connection to all three endpoints,
which proves it read and probed all three records. It then listed exactly one, the record whose file
name matched the real process id. A send to either of the others answered
`No agent named '...' is reachable`.

**The file name is the identity, and it must be a live process id owned by the same user.**

So each native peer costs one process. The remaining question was whether that cost is acceptable.
Twelve Go processes, each binding an endpoint with both readers active, measured 77.0 MB of working
set in total, which is 6.42 MB each, in a tight range of 6.36 to 6.49 MB.

## Decision

Adopt a hybrid model with three levels of presence on each machine:

| Level | How many | Purpose |
|---|---|---|
| Session ghosts | 0 to N, user capped | Subscribed remote sessions, as native peers |
| Workspace ghost | Exactly 1 | Broadcast, roster, and reaching anyone not promoted |
| MCP server | 1 | Roster, promotion, and fallback transport |

Promotion policy is `auto` by default, promoting every exposed remote session up to a cap and
evicting the least recently used beyond it. Other modes are `manual` and `off`.

**The cap belongs to the user, not to the workspace.** The default is 8, chosen with the measured
cost in view. A workspace owner cannot force a member to run more processes.

| Cap | Ghost memory |
|---|---|
| 4 | 26 MB |
| 8, the default | 51 MB |
| 12 | 77 MB, measured |
| 20 | 128 MB |

Peer names use safe characters only. The documented `@` mention typeahead requires double quotes
around any name containing characters outside letters, digits, hyphens, and underscores, so a name
like `workspace:acme` would have to be typed as `@"workspace:acme"`. The default template produces
`luis-api` and `ws-acme`, with the workspace prefix added when a user belongs to several workspaces.

## Alternatives Considered

### One peer for the whole workspace

- Pros: exactly one process per machine, cost independent of workspace size.
- Cons: Claude cannot see a remote session in `ListAgents`, so it cannot message one on its own
  initiative. It has to consult a roster first. That removes the main reason for the project.
- Rejected once the measurement showed the hybrid is affordable.

### One peer per remote session with no cap

- Pros: full fidelity at all times.
- Cons: a twenty person workspace would spawn dozens of processes on every member's machine without
  the member ever agreeing to it.
- Rejected. The cap with eviction gives the same experience for the sessions that matter.

## Consequences

- The daemon becomes a process supervisor. Spawning, reaping, and restarting ghosts as the remote
  roster changes is real work that needs its own tests.
- Nothing becomes unreachable at the cap. Evicted sessions are still reachable through the workspace
  ghost.
- Claude can promote a session itself through an MCP tool when it notices repeated traffic with a
  session that has no ghost.
- Two risks specific to this model:
  - **Antivirus on Windows.** A process spawning children that bind named pipes is a pattern that
    detection engines inspect closely. Mitigation is to sign the binary and to keep ghosts alive
    rather than churning them. This needs testing against Defender early.
  - **Task manager noise.** Several `claudio` processes will be visible. `claudio status` must
    explain what each one is.
- A favourable side effect: Claude Code rate limits bursts per destination session, so spreading
  traffic across ghosts spreads that limit too.
