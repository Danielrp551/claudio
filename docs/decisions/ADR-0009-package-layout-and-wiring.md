# ADR-0009: Pragmatic package layout and manual constructor wiring

## Status

Accepted

## Date

2026-09-12

## Context

Two structural choices have to be made before any code is written: how packages are organised, and
how dependencies are wired.

Both have popular answers that are wrong for this size of project. Clean architecture with ports,
adapters, and use case objects adds three files per behaviour. A dependency injection container adds
a layer of indirection that has to be learned before anyone can follow a call.

This is a daemon with a supervisor, a transport, a policy engine, and a command line interface. It is
not a large business application, and it is meant to attract outside contributors who should be able
to follow a code path by reading it.

## Decision

**Layout: one package per area of concern under `internal/`, named for what it owns.**

```
cmd/claudio/          entry point only, parse flags and call Run
internal/
  ccpeer/             the Claude Code protocol boundary, see ADR-0008
  ghost/              the thin child process
  daemon/             supervisor, policy, routing
  workspace/          workspaces, members, invitations
  trust/              trust levels and resolution
  transport/          transport interface and relay client
  relay/              the relay server
  mcpserver/          the MCP server
  cli/                command definitions
  config/             configuration loading
```

`pkg/` stays empty until something is genuinely useful to an external consumer. Nothing is exported
for the sake of being exported.

**Wiring: manual constructor injection. No container, no code generation.**

Each package exposes a `New...` constructor that takes its dependencies as interfaces defined by the
consumer. `cmd/claudio` builds the object graph explicitly. If that function becomes hard to read,
that is a signal the design drifted, and it is a signal we want to keep.

## Alternatives Considered

### Clean or hexagonal architecture with explicit ports and adapters

- Pros: uniform structure, easy to test in isolation, familiar to some contributors.
- Cons: for this size it adds indirection without removing coupling, and it hides the actual call
  path behind naming conventions.
- Rejected. The parts that genuinely need an abstraction, namely the transport and the local
  endpoint, get one anyway, because they have more than one real implementation.

### A dependency injection library

- Pros: less wiring code as the graph grows, lifecycle management for free.
- Cons: a contributor has to learn the container before reading the code, and compile time wiring
  errors turn into runtime ones with several of the options.
- Rejected. Revisit only if the manual graph becomes genuinely unwieldy, which would itself be
  evidence worth having.

### Flat layout with everything in one package

- Pros: simplest possible start.
- Cons: the compatibility boundary of ADR-0008 only works if it is a real package boundary.
- Rejected.

## Consequences

- A contributor can follow any request by reading, without knowing a framework.
- Interfaces are declared where they are consumed, which is the Go convention and keeps packages from
  depending on each other for type definitions alone.
- Only two abstractions exist because they earn it: `LocalEndpoint`, which has three platform
  implementations, and `Transport`, which will have a relay implementation now and possibly another
  later.
- If the project grows into something this layout cannot carry, that will be visible in the wiring
  function, and a new ADR supersedes this one.
