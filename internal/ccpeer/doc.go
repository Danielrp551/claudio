// Package ccpeer is the only place in this project that knows how Claude Code
// represents sessions on disk and on the wire.
//
// # Two surfaces, two levels of risk
//
// This package touches two things that must not be confused.
//
//   - The inbox, which is documented. A session exports the path of its inbox
//     and a per session token to hooks and shell commands, and Anthropic
//     documents that a program may post into a session that way. This half is
//     public and stable.
//
//   - The registry, which is not documented. Each session writes a record on
//     disk, and other sessions read that directory to learn who they can reach.
//     Nothing about the file name, the schema, or the frame format is published.
//     It can change in any release.
//
// Everything that depends on the second surface lives here, behind version
// checks, so the rest of the program can degrade to an MCP transport instead of
// breaking when the format changes. See ADR-0008.
//
// # What Claude Code verifies before it delivers to you
//
// Verified experimentally against Claude Code 2.1.269. Before delivering, the
// sender opens a preflight connection, checks the following, disconnects, and
// only then reconnects to deliver:
//
//  1. The path is a local IPC path, never a UNC path or a remote host.
//  2. The endpoint is vouched for by a live record in the registry.
//  3. The target is not a symbolic link.
//  4. The process serving the endpoint matches the record: same process id, same
//     start token, which defeats process id reuse, and same owning user.
//
// The useful consequence is that those checks verify consistency between the
// record and the process serving the endpoint. They do not verify that the
// process is Claude Code. A process publishing its own real process id and start
// token passes all four, which is what lets this project represent remote
// sessions as native peers.
//
// # Identity is the file name
//
// A record is named after a process id, and that number must belong to a live
// process owned by the same user. A single process publishing several records
// under different names is listed once, under the record whose name matches its
// real process id. One process therefore exposes exactly one native peer. See
// ADR-0003.
package ccpeer
