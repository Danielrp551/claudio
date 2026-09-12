# ADR-0007: Conversation centric domain model

## Status

Accepted

## Date

2026-09-12

## Context

Version 1 ships direct messages only. Version 2 adds channels and threads. Version 3 adds shared
artifacts such as a diff, a file, or a finding.

A naive version 1 would model a message as pointing at a destination session. That shape forces a
rewrite of the message table and of everything that reads it as soon as channels arrive.

## Decision

**A direct message is a conversation of one kind, not a separate entity.**

| Entity | v1 | v2 channels | v3 artifacts |
|---|---|---|---|
| `Conversation` | `kind=direct` | `kind=channel`, `kind=thread` with `parent_id` | unchanged |
| `Participant` | the two endpoints | channel members | unchanged |
| `Message` | complete | unchanged | gains attachments |
| `Artifact` | modelled, not implemented | unchanged | implemented |

Channels and threads then arrive as new values of an enum and a column that already exists. There is
no structural migration.

Identity is modelled at four levels, not two:

```
Person --< Machine --< Profile --< AgentSession
```

`Profile` is not optional. Without it, two identically named sessions from different configuration
directories on the same machine cannot be told apart, which is a real situation for users who run
several profiles.

Neither the session id nor the process id is a stable join key. A `/clear` mints a new session id
without restarting the process, and process ids are recycled. The stable key across connector
restarts is the triple of machine, process id, and process start token.

Two fields that look minor and are not:

- `Message.seq` is a per conversation counter, not global. It is what allows gaps to be detected on
  reconnect without depending on clocks belonging to different machines.
- `Message.trust_at_send` freezes the trust level in force when the message was sent, so revoking
  someone does not retroactively rewrite how their past messages were interpreted in an audit.

Delivery is observable, with states that distinguish the things usually conflated:

```
queued -> sent_to_relay -> received_by_connector -> delivered_to_agent
                                                 -> held
                                                 -> refused
                                                 -> failed or expired
```

Local state that belongs to a machine and is never synchronised with the workspace is modelled
separately: promotion policy, subscriptions, ghost assignments, and trust overrides.

## Alternatives Considered

### Message points at a session

- Pros: the shortest path to a working version 1.
- Cons: guarantees a rewrite at version 2, of the most written to table in the system.
- Rejected. The cost of the abstraction now is one indirection. The cost later is a migration.

### Model channels fully in version 1

- Pros: no future work at all.
- Cons: builds surface nobody has used yet, and the right design for channels depends on how people
  use direct messages first.
- Rejected. The model leaves room, the implementation does not fill it.

## Consequences

- Version 2 and version 3 add behaviour, not migrations.
- A relay acknowledgement can never be recorded as `delivered_to_agent`. This is not theoretical. A
  probe confirmed that writing to a session inbox succeeds from the sender's side even when the
  message is then discarded, and the sender receives no error. A successful write proves
  `received_by_connector` and nothing more.
- Conversation history is never transmitted. The native channel carries plain text only, never the
  sender's history or files, and this project copies that decision deliberately, for privacy and for
  size.
