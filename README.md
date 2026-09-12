# claudio

Shared workspaces between Claude Code sessions belonging to **different people, on different
machines**, using the native messaging of Claude Code rather than a chat bolted on beside it.

A message you send arrives in the other person's session the same way a message from one of your own
sessions does, with attribution and a reply address. You do not learn a new tool, and neither does
Claude.

> **Status: version one, not yet released.** Everything described here works and is covered by tests,
> including an acceptance test that runs two connectors and a relay and requires a message to arrive
> in the other person's session inbox. It has not been used by anybody other than its authors, and
> macOS is not verified.

## What it does

```
$ claudio sessions
luis@thinkpad/api     idle   ~/projects/api
luis@thinkpad/front   busy   ~/projects/front
ana@macbook/infra     idle   ~/infra

$ # inside a Claude Code session, Claude does this on its own initiative:
$ # SendMessage(to: "luis-api", "the schema changed, tenant_id is the new column")
```

On the receiving machine that message enters the session inbox natively, framed according to the
trust level its owner granted you.

## How it works

Three levels of presence run on each machine:

| Level | How many | Purpose |
|---|---|---|
| Session ghosts | 0 to N, capped by you | Subscribed remote sessions, as native peers |
| Workspace ghost | 1 | Broadcast, roster, and reaching anyone not promoted |
| MCP server | 1 | Roster, promotion, and fallback transport |

A ghost is a small process that holds one endpoint and relays frames to the daemon. It costs about
6.4 MB of resident memory, measured rather than estimated, which is why the default cap is 8 and why
you can change it.

Messages travel between machines through a relay you can host yourself. The relay routes and queues.
It cannot read anything, because the payload is encrypted end to end.

Read [`docs/architecture.md`](docs/architecture.md) for the full picture and
[`docs/decisions/`](docs/decisions/) for why each choice was made.

## Two things to know before you rely on this

**Half of this rests on an internal detail.** Delivering into a session inbox uses a documented and
supported interface. Discovering peers reads a registry that Anthropic does not document, which can
change in any release. That dependency is isolated in one package, detected by version rather than
assumed, and the tool degrades to an MCP transport instead of breaking. See
[ADR-0008](docs/decisions/ADR-0008-compatibility-boundary.md).

**Granting `peer` trust is granting real trust.** The tool reduces risk through framing and
attribution. It does not remove it. A message from another person can never approve a permission
prompt or change your configuration, and your session permissions always apply, but somebody you
trust at `peer` level is treated by your Claude much like one of your own sessions. See
[ADR-0006](docs/decisions/ADR-0006-receiver-granted-trust.md).

## Platform support

| Platform | Status |
|---|---|
| Windows 11, native | Verified end to end against Claude Code 2.1.269 |
| Linux | Registry schema verified, delivery verified at transport level |
| macOS | **Not verified.** Inferred from documentation only |

Windows and Linux differ more than you would expect, including a field that carries the same name and
incompatible meanings. See [`docs/protocol.md`](docs/protocol.md).

A session inside WSL2 and a native Windows session on the same computer cannot reach each other. That
is a limit of Claude Code, not of this tool, and the daemon reports it rather than failing quietly.

## Quick start

```bash
go install github.com/Danielrp551/claudio/cmd/claudio@latest
```

**One person hosts the workspace and runs the relay:**

```bash
claudio workspace create acme --name "Acme"
claudio relay --addr :8787          # keep this running, behind TLS in production
claudio invite --workspace acme     # prints a code to hand over
```

**Everybody joins with that code:**

```bash
claudio join <code> --relay wss://relay.example.com/connect --workspace acme
claudio expose my-session           # share one session, by name
claudio daemon                      # keep this running
```

**Then check what you have:**

```bash
claudio doctor     # what this machine can and cannot do
claudio status     # what is running, and what each process is for
claudio sessions   # who you can reach
```

Inside a Claude Code session the remote sessions appear in the agent list, so Claude messages them
with `SendMessage` the same way it messages your own.

Optionally register the MCP server with Claude Code, which gives Claude the roster and a way to
promote a session to a native peer:

```bash
claude mcp add claudio -- claudio mcp
```

## Commands

| Command | Description |
|---|---|
| `claudio daemon` | Run the connector for this machine |
| `claudio mcp` | Serve the MCP interface over stdio |
| `claudio relay` | Run a relay server |
| `claudio workspace create` | Create a workspace, where the relay lives |
| `claudio workspace list` | Show members and their fingerprints |
| `claudio workspace revoke` | End a membership, where the relay lives |
| `claudio workspace trust` | Change the level the workspace proposes for a member |
| `claudio invite` | Create an invitation code |
| `claudio join` | Redeem an invitation |
| `claudio expose` | Share a local session with the workspace |
| `claudio unexpose` | Stop sharing one |
| `claudio sessions` | List sessions reachable in a workspace |
| `claudio trust` | Set the trust level for a person, workspace, or session |
| `claudio pending` | List messages the `hold` gate is withholding |
| `claudio approve` | Deliver one of them |
| `claudio drop` | Discard one of them |
| `claudio policy` | Set promotion mode and the cap on native peers |
| `claudio status` | Show what this machine is running and why |
| `claudio doctor` | Report what this machine can and cannot do |

## Development

```bash
make build     # build the binary
make test      # run tests
make race      # run them with the race detector
make lint      # run golangci-lint
make check     # fmt, vet, lint, test
```

The race detector needs cgo and a working 64 bit C compiler. That is the default on Linux and macOS,
and often missing on Windows, which is why it has a target of its own rather than making an ordinary
test run fail.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) first. In short: the repository is in English, decisions go in
an ADR before the code that implements them, and anything touching
[`internal/ccpeer`](internal/ccpeer) needs a note about which Claude Code version it was verified
against.

## License

MIT. See [LICENSE](LICENSE).
