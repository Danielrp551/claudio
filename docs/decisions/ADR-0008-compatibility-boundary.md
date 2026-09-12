# ADR-0008: Isolate the undocumented registry behind a compatibility boundary

## Status

Accepted

## Date

2026-09-12

## Context

This project touches two surfaces of Claude Code with very different guarantees.

**The inbox is documented.** A session exports the path of its inbox and a per session token to hooks
and shell commands, and Anthropic documents that a script may post into a session that way. That half
is public and stable.

**The registry is not documented.** Each session writes a record on disk, and other sessions read
that directory to learn who they can message. Nothing about the file name, the schema, or the frame
format is published. It can change in any release.

Verification also showed the two supported platforms differ more than expected:

| Field | Windows | Linux |
|---|---|---|
| Process start token | FILETIME since 1601 | Clock ticks since boot, field 22 of `/proc/<pid>/stat` |
| Process id domain | `win32:<hostname>` | `linux:<machine-id>:<pid namespace>` |
| Endpoint | Named pipe | `$XDG_RUNTIME_DIR/cc-socks/<pid>.sock` |
| Auth line | Mandatory | Optional |

The same field name carries incompatible meanings across platforms. On Linux the process id domain
also encodes the process id namespace, which is deliberate, because a process id only means something
inside its namespace and sessions in different containers must not verify each other.

## Decision

All dependence on the undocumented surface lives in one package, `internal/ccpeer`, behind a
`LocalEndpoint` interface with build tagged implementations per platform. Nothing outside that package
parses a record, writes a record, builds a frame, or interprets a process start token.

The boundary detects rather than assumes. Two numbers the protocol publishes are the anchors:
`peerProtocol` in the record and `msgV` in the frame. An unknown value is never interpreted
optimistically.

When the boundary cannot vouch for the environment, the daemon degrades instead of breaking. It stops
spawning ghosts, routes everything through the MCP tools, and tells the user why in plain language.

## Alternatives Considered

### Depend on the registry everywhere, and pin a version of Claude Code

- Pros: less indirection.
- Cons: a user upgrading Claude Code would break the tool, and a version pin on somebody else's
  product is not a promise we can keep.
- Rejected.

### Use only the documented surface and drop native discovery

- Pros: no fragile dependency at all.
- Cons: gives up the reason the project exists, see ADR-0001.
- Rejected as the primary mode. This is precisely the degraded mode described above, which is why it
  is worth building well.

### Vendor the third party Go library that implements the Unix side

- Pros: saves work on macOS and Linux.
- Cons: it read the protocol from an older version, it is Unix only, and it declares that Windows has
  no cross session messaging, which is no longer true. Taking it as a source of truth would import a
  stale model.
- Rejected as a dependency. Useful as a second opinion when reading the protocol.

## Consequences

- The fragile surface is small, isolated, and testable against a real Claude Code session.
- A release of Claude Code that changes the registry degrades the tool, it does not kill it.
- Cross platform work is confined to build tagged files inside one package.
- The README states the risk plainly rather than hiding it. Users deserve to know which half of this
  rests on an internal detail.
- macOS is not verified. The schema there is inferred from documentation and from a third party
  implementation that read an older version. Until somebody runs an equivalent probe on a Mac, macOS
  support is marked unverified rather than claimed.
