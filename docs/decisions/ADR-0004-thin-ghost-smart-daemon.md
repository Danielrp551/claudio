# ADR-0004: Thin ghost process, smart daemon

## Status

Accepted

## Date

2026-09-12

## Context

ADR-0003 requires one process per native peer. That raises a design question: how much of the system
lives in each of those processes.

Two forces pull in opposite directions. Memory argues for the smallest possible child. Correctness
argues for not duplicating routing, policy, and cryptography across many processes that can each
crash independently.

There is also a lifetime problem. A ghost that outlives its parent leaves a peer in the user's
`ListAgents` that answers to nobody.

## Decision

The ghost holds the endpoint and nothing else. It binds, writes its own record with its own process
id, and relays frames to the parent over stdin and stdout as newline delimited JSON. No network, no
cryptography, no state.

The daemon holds everything else: the relay connection, cryptography, trust policy, routing, queues,
and supervision of its children.

```
claudio daemon
  |- relay connection
  |- cryptography, trust policy, routing, queue
  `- supervises children
        |- claudio ghost --id luis-api
        |- claudio ghost --id luis-front
        `- claudio ghost --id ws-acme
```

Shutdown has a mandatory order, and it is not optional:

```
1. stop accepting connections
2. stop the heartbeat and wait for it to finish
3. remove the record and the key
4. exit
```

The daemon also keeps an index of the records it wrote, stored outside the session registry, and
removes on startup any record in that index that no longer belongs to a live child.

**No invented fields are ever written into the session registry.** The registry belongs to Claude
Code, which has handling for corrupt records, and adding unknown keys invites trouble. Our own
bookkeeping goes in our own file.

## Alternatives Considered

### Fat ghosts, each with its own relay connection

- Pros: no parent to child protocol, each peer is independent.
- Cons: N connections to the relay for one user, N copies of the trust policy that can disagree, and
  N places where a key lives. Memory per ghost would rise well above the measured 6.4 MB.
- Rejected.

### Daemon forks itself instead of spawning a subcommand

- Pros: marginally simpler wiring.
- Cons: fork semantics differ across the three target platforms, and Go's runtime does not tolerate
  fork without exec well.
- Rejected.

### A socket between daemon and ghost instead of stdin and stdout

- Pros: reconnection is possible if the parent restarts.
- Cons: another endpoint to secure and to clean up, and it removes the property below.
- Rejected. Losing stdin is exactly the signal we want.

## Consequences

- **A ghost cannot outlive its daemon.** When the parent dies, stdin closes, and the child removes
  its record and exits. No orphan peers.
- The fragile surface, meaning everything that depends on the undocumented registry, is concentrated
  in the smallest component. The ghost is roughly 180 lines, which makes it cheap to rewrite when
  Anthropic changes the format.
- A ghost crash is visible and recoverable. Its record goes stale, Claude Code ignores it because the
  process id is dead, and the daemon restarts it.
- The shutdown order above was learned from a real failure, not from theory. A probe left an orphan
  record because its heartbeat goroutine rewrote the file after cleanup had deleted it. The orphan
  index exists so that such a failure is recoverable, but correct ordering is the first defence and
  needs a dedicated test.
