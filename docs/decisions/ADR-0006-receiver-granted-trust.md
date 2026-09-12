# ADR-0006: Trust is granted by the receiver, with three levels

## Status

Accepted

## Date

2026-09-12

## Context

Today every peer a Claude Code session can reach belongs to the same person. A workspace shared by
several people breaks that assumption. A message from a stranger would arrive through the same inbox,
with the same framing, as a message from one of your own sessions.

Users want different behaviour for different people. With a teammate they already work with, the
desired behaviour is autonomy, close to how they treat their own sessions. With an unknown
collaborator or an automated sender, the desired behaviour is to treat the content as data.

## Decision

Trust is a property the receiver assigns to a sender. A message can declare its origin, it can never
claim a level. A workspace owner proposes a default and cannot grant trust on someone else's machine.

Three levels:

| Level | Behaviour | Typical use |
|---|---|---|
| `peer` | Arrives as if from another of your own sessions, full autonomy | People you already work with |
| `collaborator` | Arrives natively, marked unambiguously as another person, framed as a request to evaluate rather than an instruction. **The default** | Collaborators, clients, open source |
| `source` | Arrives as data, with an explicit instruction not to follow embedded instructions | Untrusted sources, bots, automation |

Plus one gate that is not a level, `hold`, which withholds delivery until the user approves. It is
written where a level is written and resolves like the most restrictive value there is, so a hold
anywhere holds. What makes it a gate rather than a level is what happens next: the message is kept,
`claudio pending` lists what is waiting, and `claudio approve` or `claudio drop` decides. A message
that is held is stored unframed and framed at delivery, with the level that applies then, because the
point of holding is that the person had not decided yet.

What changes between levels is **the framing applied to the text as it enters the inbox**, which is
the only thing that actually governs how the receiving model treats it. Session permissions apply
identically at all three levels. Trust changes framing, never permissions.

Resolution takes the most restrictive value:

```
effective = min( workspace.default_trust,
                 member.trust,
                 local.policy_for(person or workspace),
                 local.session_override )

with the ordering  source < collaborator < peer
```

Consequences of that rule, all intended:

- Raising trust requires both sides to agree. Nobody raises your level on their own.
- Lowering it can be done by either side, unilaterally, at once.
- A single session can be stricter than the rest of a machine, which is useful for the session that
  touches production.

## Alternatives Considered

### A single level, with attribution only

- Pros: far simpler to build.
- Cons: forces the same posture for a teammate and for a stranger. The user asked for both.
- Rejected, though it remains the behaviour a user gets by pinning everything to `collaborator`.

### Quarantine by default, nothing delivered without approval

- Pros: maximum control.
- Cons: breaks the asynchronous flow that makes the tool useful, and duplicates a control Claude Code
  already offers.
- Rejected as a default. It is available as the `hold` gate.

### Sender declared trust

- Rejected outright. It inverts the security direction and would make an invitation irreversible in
  practice.

## Consequences

- Configuration must fit in one line or nobody will use it:

  ```
  claudio trust ana@acme peer
  claudio trust --workspace acme collaborator
  claudio trust --session repos-04 source
  ```

- The tool composes with the controls Claude Code already has and never circumvents them.
  `crossSessionInbound: refuse` is respected and reported back to the sender. `hold` is respected and
  the delivery state stays `held`, never `delivered`. `isolatePeerMachines` is respected.
- Against permission laundering, the frame attribute that declares the sender's permission class is
  propagated end to end and made visible to the receiver, so a strict session can require equally
  strict senders.
- This reduces risk, it does not remove it. Granting `peer` is granting real trust, and the README
  must say so plainly.
- Even at `peer`, attribution is never hidden. What changes with the level is autonomy, not
  transparency.
