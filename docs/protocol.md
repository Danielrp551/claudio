# The Claude Code messaging protocol, as verified

Everything here was verified against **Claude Code 2.1.269**, on Windows 11 and on Linux under WSL2,
during September 2026. Where something is inferred rather than verified, it says so.

This document exists because the repository is self contained in English, see
[ADR-0010](decisions/ADR-0010-english-as-repository-language.md), while the raw research notes are in
Spanish outside the repository.

## Two surfaces

**The inbox is documented.** Each session exports the path of its inbox and a per session token to
hooks and shell commands, as `CLAUDE_CODE_MESSAGING_SOCKET` and `CLAUDE_CODE_MESSAGING_TOKEN`. A
program may connect and post a message. The first line may be an authentication line of the form
`{"type":"auth","token":"..."}`, which is optional on macOS and Linux and mandatory on native Windows.

**The registry is not documented.** Each session writes a record on disk, and other sessions read that
directory to learn who they can reach. Nothing about the file name, the schema, or the frame format is
published.

## The session record

Stored as `<pid>.json` in the sessions directory, with a companion `<pid>.<sha256>.key`.

```json
{
  "pid": 51352,
  "sessionId": "bc34cee9-99fa-4473-a5a2-1fe92095be70",
  "cwd": "...",
  "startedAt": 1789188605730,
  "procStart": "134336621606986071",
  "version": "2.1.269",
  "peerProtocol": 1,
  "peerFeatures": ["notify_idle", "artifact_yield"],
  "kind": "interactive",
  "entrypoint": "cli",
  "pidDomain": "win32:desktop-v04upuo",
  "messagingSocketPath": "\\\\.\\pipe\\LOCAL\\cc-msg-<32 hex>",
  "name": "herramientas-22",
  "nameSource": "derived",
  "status": "busy",
  "updatedAt": 1789188707328
}
```

**The file name is the identity.** The number must belong to a live process owned by the same user.
This was established experimentally: one process published three records, each pointing at an endpoint
it owned, all three declaring the same real process id inside the record. Claude Code opened a
connection to all three endpoints, which proves it read and probed all three, and then listed exactly
one, the record whose file name matched the real process id. A send to either of the others answered
`No agent named '...' is reachable`.

One process therefore exposes exactly one native peer.

## Platform differences

| Field | Windows | Linux |
|---|---|---|
| `procStart` | FILETIME, 100 nanosecond intervals since 1601 | Clock ticks since boot, field 22 of `/proc/<pid>/stat` |
| `pidDomain` | `win32:<hostname in lower case>` | `linux:<machine-id>:<contents of /proc/self/ns/pid>` |
| Endpoint | `\\.\pipe\LOCAL\cc-msg-<32 hex>` | `$XDG_RUNTIME_DIR/cc-socks/<pid>.sock` |
| Fallback | not applicable | `/tmp/cc-socks-<uid>`, documented but not used in practice |
| Permissions | Windows ACL | directory `0700`, socket `0600` |
| Auth line | mandatory | optional |
| `peerFeatures` | `notify_idle`, `artifact_yield` | the same plus `reply_across_default_dirs` |

Each derivation was checked directly rather than inferred. On Linux the record carried `procStart` of
`27273` and field 22 of `/proc/803/stat` was exactly `27273`. The machine id and the contents of
`/proc/self/ns/pid` both appear literally inside `pidDomain`.

**`procStart` carries the same name and incompatible meanings.** On Windows it is an absolute instant.
On Linux it is a counter of ticks since the machine booted. It is not convertible between the two.

**`pidDomain` on Linux encodes the process id namespace**, which is deliberate. A process id only
means something inside its namespace, so sessions in different containers must not verify each other.

## What the sender verifies before delivering

Reconstructed from the binary and confirmed by observation. The sender opens a preflight connection,
checks the following, disconnects, and only then reconnects to deliver.

1. The path is a local IPC path. A UNC path or a remote host is refused outright.
2. The endpoint is vouched for by a live record. An unvouched endpoint is refused.
3. The target is not a symbolic link.
4. The process serving the endpoint is the one named in the record: same process id, same start token,
   which defeats process id reuse, and same owning user.

These checks verify consistency between the record and the process serving the endpoint. They do not
verify that the process is Claude Code.

## The frame

Captured live. Newline delimited JSON.

```json
{
  "msgV": 1,
  "msg_id": "36dd150e-a4a9-4c70-8bb0-153a71e158c7",
  "type": "user",
  "message": {
    "role": "user",
    "content": "<cross-session-message from=\"uds:...\" from-name=\"herramientas-22\" from-mode=\"prompting\">\ntext\n</cross-session-message>"
  },
  "priority": "next",
  "from": "uds:..."
}
```

- `from` is the reply address, the sender's own endpoint with a `uds:` prefix.
- `from-mode` is the sender's permission class. It is what feeds the receiver's inbound rules, and
  what this project propagates to defend against permission laundering.
- `msgV` and `peerProtocol` are the two numbers to watch when deciding whether the native path is safe.

## Delivery is not what a successful write tells you

A probe delivered a frame with the authentication line and it appeared in the target conversation. The
same probe without the authentication line delivered nothing, which matches the documented Windows
behaviour.

**In both cases the write succeeded from the sender's side.** No error is returned when the message is
discarded. A successful write proves that bytes were accepted and nothing more. This is why delivery
state distinguishes `received_by_connector` from `delivered_to_agent`, see
[ADR-0007](decisions/ADR-0007-conversation-centric-model.md).

## Operational limits, documented by Anthropic

- Around 1,000,000 characters for the serialized message, refused at the sender.
- A burst rate limit per destination, also refused at the sender.
- At most 50 accepted messages queued for the receiving model, and identical repeats within a short
  window are dropped.
- At most 100 held messages, after which the oldest are dropped.
- Plain text only. Neither history nor files ever travel.

## Cross machine, natively

| Destination | How it travels |
|---|---|
| Same machine | Per session endpoint, never through Anthropic servers |
| Another of your machines | Through Anthropic servers, over Remote Control |
| Claude Code on the web | Through Anthropic servers |

Remote Control requires a claude.ai sign in as the active authentication, and is scoped to one
account. There is no native mechanism for another person's session to appear in your agent list, which
is the gap this project fills.

## A useful side effect for testing

Claude Code writes its record and binds its endpoint **before** it talks to the API. Running it with
an invalid API key is therefore enough to inspect the registry on a platform where you have no
credentials, which is how the Linux schema above was verified.

The same fact is a caution: a session that cannot reach the API still appears as a peer. Being in the
registry does not mean being operational.

## Not verified

- **macOS.** No hardware was available. The schema there is inferred from documentation and from a
  third party implementation that read version 2.1.233. It is marked unverified rather than claimed.
- **End to end delivery on Linux.** Verified at transport level only, because the probe session ran
  with an invalid API key and could not render anything.
