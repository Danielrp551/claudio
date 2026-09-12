# Contributing

Thank you for considering a contribution. This project is young, so the rules below are few and
mostly about keeping the reasoning visible.

## Ground rules

**Everything in this repository is in English.** Code, identifiers, comments, commit messages,
issues, and documentation. Design discussions elsewhere happen in Spanish, and where a decision
depends on a finding recorded there, the decision record restates it in English so you never need to
read a document you cannot. See `docs/decisions/ADR-0010-english-as-repository-language.md`.

**A decision goes in an ADR before the code that implements it.** If your change alters how the
system works rather than what it does, open a decision record first. A record is cheap to write and
prevents the same debate a second time. Records are never deleted. A changed decision gets a new
record that supersedes the old one.

**Do not use em dashes or semicolons in prose.** Use commas, colons, parentheses, or a new sentence.
This applies to documentation, comments, and commit messages.

## Working on the protocol boundary

`internal/ccpeer` is the only package that knows how Claude Code stores sessions on disk and frames
messages on the wire. None of that is documented by Anthropic, and it can change in any release.

If you change anything in that package:

1. Say in the pull request **which version of Claude Code you verified against** and on which
   operating system.
2. Update `docs/protocol.md` if what you learned differs from what is written there.
3. Do not guess at a value you have not observed. An unknown `msgV` or `peerProtocol` must degrade,
   never be interpreted optimistically.
4. Never add invented fields to a session record. That directory belongs to Claude Code, and our own
   bookkeeping lives in our own file.

macOS is currently unverified. A probe run on a Mac, with its results written into `docs/protocol.md`,
would be one of the most useful contributions available right now.

## Development

```bash
make build     # build the binary into bin/
make test      # run tests with the race detector
make lint      # run golangci-lint
make check     # what CI runs: fmt-check, vet, lint, test
```

Requirements: Go 1.22 or newer, and `golangci-lint` for the lint target.

## Pull requests

- Keep a pull request to one concern. A refactor and a behaviour change in the same diff are hard to
  review and harder to revert.
- Add tests for behaviour you add or fix. Anything touching process lifetime or registry cleanup
  needs a test, because failures there leave state on a user machine.
- Run `make check` before pushing.
- Explain why, not only what. The what is in the diff.

## Reporting a bug

Use the issue template. What matters most is your operating system, your Claude Code version, and the
output of `claudio status`.

For anything that looks like a security problem, read `SECURITY.md` first and do not open a public
issue.
