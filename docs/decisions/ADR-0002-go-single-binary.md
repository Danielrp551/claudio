# ADR-0002: Go as the implementation language, one binary with subcommands

## Status

Accepted

## Date

2026-09-12

## Context

The tool needs four runtime shapes on a user's machine:

1. A long lived connector that holds a network connection and supervises child processes.
2. A set of small child processes, one per native peer, each holding a local endpoint.
3. An MCP server spoken over stdio, registered inside Claude Code.
4. A command line interface for the human.

A relay server is also needed, but it is deployed separately.

Users of this tool are developers who already run Claude Code. They should not have to install a
runtime, and they should not have to install four things.

## Decision

Implement everything in Go, shipped as a single executable with subcommands.

```
claudio daemon     the connector, runs in the background
claudio ghost      the thin child process, not invoked by users directly
claudio mcp        the MCP server over stdio, registered in Claude Code
claudio relay      the relay server, same binary or a container
claudio <rest>     the human facing commands
```

Distribution is one static executable per operating system and architecture, with Homebrew, Scoop,
and `go install` on top.

## Alternatives Considered

### Node or TypeScript

- Pros: the first working probes were written in Node, the Claude Code plugin and MCP ecosystem is
  JavaScript native, and named pipes work out of the box.
- Cons: requires Node on the user's machine, or bundling with an extra packaging step. Measured
  memory of a minimal Node process holding a pipe was around 56 MB, against 6.4 MB for the Go
  equivalent. With one process per native peer, that difference decides the addressing model.
- Rejected mainly on the memory measurement, see ADR-0003.

### Rust

- Pros: the smallest runtime footprint of the three, and excellent handles for pipes and ACLs.
- Cons: slower to write, and a smaller pool of contributors for a young open source project.
- Rejected. Reconsider only if the per peer footprint ever becomes the binding constraint. The ghost
  is the one component small enough to be rewritten in isolation.

### Separate binaries per role

- Pros: each artifact is minimal.
- Cons: four things to build, sign, ship, and keep in version lockstep. On Windows, code signing
  multiplies. Shared code would still live in one module anyway.
- Rejected. Subcommands give the same separation with one artifact.

## Consequences

- One artifact to build, sign, and distribute. Signing matters on Windows, see ADR-0003.
- The ghost and the daemon are the same binary, so the operating system shares the read only code
  pages and the marginal cost of a ghost is close to its heap and stack alone.
- Cross platform differences are confined to build tagged files, see ADR-0008.
- Go has no ergonomic way to build a truly minimal process, so around 6 MB of resident memory per
  ghost is a floor we accept rather than a number we can tune away.
