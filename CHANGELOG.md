# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- The protocol boundary, `internal/ccpeer`: the session registry, the frame format, and one endpoint
  implementation per platform. macOS reports itself unverified rather than guessing.
- The ghost, the small process that holds one native peer and cannot outlive its daemon.
- The connector, which discovers local sessions, supervises ghosts, routes messages, and cleans up
  after a run that did not shut down cleanly.
- Member identities and sealed envelopes, built only from the standard library.
- The workspace model and its store, with invitations that are vouchers rather than credentials.
- The relay and its client, with messages sealed end to end and queued for members who are offline.
- Trust framing with three levels, resolved by taking the most restrictive opinion that applies.
- The command line and the MCP server, the second of which is also the fallback transport.
- Ten architecture decision records, and `docs/protocol.md`, the verified description of the Claude
  Code messaging protocol on Windows and Linux.

### Notes

Version one is messaging between sessions. Channels and threads are version two, and shared context
is version three. The data model already carries both, so neither is a migration.

macOS is not verified. Delivery into a session works there, publishing a native peer does not, and
the connector says so rather than guessing.
