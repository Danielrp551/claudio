# Architecture Decision Records

Every significant decision in this project is recorded here, with the context that produced it and
the alternatives that were rejected. Read these before proposing a change to the architecture. They
exist so the same debate is not had twice.

## Format

Each record follows the same shape: Status, Date, Context, Decision, Alternatives Considered, and
Consequences. Records are never deleted. When a decision changes, a new record supersedes the old
one and both stay in place.

Lifecycle:

```
PROPOSED -> ACCEPTED -> (SUPERSEDED or DEPRECATED)
```

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](ADR-0001-reuse-native-cross-session-messaging.md) | Reuse the native cross-session messaging of Claude Code | Accepted |
| [0002](ADR-0002-go-single-binary.md) | Go as the implementation language, one binary with subcommands | Accepted |
| [0003](ADR-0003-hybrid-addressing.md) | Hybrid addressing with one process per native peer | Accepted |
| [0004](ADR-0004-thin-ghost-smart-daemon.md) | Thin ghost process, smart daemon | Accepted |
| [0005](ADR-0005-relay-with-end-to-end-encryption.md) | Self hosted relay with end to end encryption, not peer to peer | Accepted |
| [0006](ADR-0006-receiver-granted-trust.md) | Trust is granted by the receiver, with three levels | Accepted |
| [0007](ADR-0007-conversation-centric-model.md) | Conversation centric domain model | Accepted |
| [0008](ADR-0008-compatibility-boundary.md) | Isolate the undocumented registry behind a compatibility boundary | Accepted |
| [0009](ADR-0009-package-layout-and-wiring.md) | Pragmatic package layout and manual constructor wiring | Accepted |
| [0010](ADR-0010-english-as-repository-language.md) | English as the repository language | Accepted |

## Evidence behind these records

Several of these decisions rest on experiments run against Claude Code 2.1.269 on Windows 11 and on
Linux under WSL2. The raw findings live outside this repository, in the research folder of the parent
project, and the protocol summary lives in [`docs/protocol.md`](../protocol.md).
