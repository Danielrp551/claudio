# Security policy

## Reporting a vulnerability

Do not open a public issue for a security problem. Use the private vulnerability reporting feature of
this repository, under the Security tab, or contact the maintainers directly.

Include what you were doing, what happened, and the smallest reproduction you have. You will get an
acknowledgement within a few days.

## What this project can and cannot protect

Being explicit here is part of the design rather than a disclaimer.

**The relay cannot read your messages.** Payloads are encrypted end to end between connectors. Someone
who runs a relay sees who is connected and which envelope goes where, and nothing else.

**An invitation is not a credential.** A code is a single use voucher redeemed for an individual
membership. Knowing a code grants no access on its own, and revoking one member rotates nothing for
anyone else.

**Trust is granted by the receiver.** A sender can declare its origin. It can never claim a level of
trust. A workspace owner cannot grant trust on somebody else's machine.

**Granting `peer` trust is granting real trust.** At that level your Claude treats messages from that
person much like messages from your own sessions. Framing and attribution reduce risk. They do not
remove it. Use `collaborator`, which is the default, unless you actually mean it.

**Controls belonging to Claude Code are always respected.** If `crossSessionInbound` is set to refuse,
nothing is delivered and the sender is told. If `isolatePeerMachines` requires your approval, it is
asked for. Session permissions apply to anything a message asks for. This project never works around
any of these, and if one blocks something, that is reported rather than routed around.

**Against permission laundering.** A message from another person can never approve a permission
prompt, change configuration, or run a command by being written as text. The sender's permission class
travels with the message and is visible to the receiver, so a strict session can require equally
strict senders.

## Dependence on undocumented behaviour

Peer discovery reads a registry that Anthropic does not document. A change there can stop the native
path from working. That is a reliability risk rather than a security one, and it is handled by
degrading to a documented transport. See `docs/decisions/ADR-0008-compatibility-boundary.md`.
