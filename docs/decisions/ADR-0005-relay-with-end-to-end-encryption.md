# ADR-0005: Self hosted relay with end to end encryption, not peer to peer

## Status

Accepted

## Date

2026-09-12

## Context

Messages have to cross the public internet between laptops that are behind home routers, mobile
tethering, and corporate firewalls. The obvious modern sounding option is peer to peer, which avoids
running any server at all.

The workspace also needs presence, deferred delivery to a member who is offline, and revocable
membership. Those requirements interact with the transport choice more than they first appear.

## Decision

A self hostable relay that routes messages, with end to end encryption so the relay operator cannot
read them. The transport sits behind an interface so another implementation can be added later.

The relay knows who is connected and which envelopes to hand to whom. It does not know what is
inside them.

## Alternatives Considered

### Peer to peer, for example over a distributed hash table

- Pros: no server to run, appealing story for an open source tool.
- Cons, each of which is load bearing:
  - **NAT traversal fails in the cases that matter.** Behind carrier grade NAT, symmetric NAT, or a
    corporate firewall, hole punching does not work, and the standard remedy is a relay. The result
    is needing a relay anyway, but a worse one.
  - **Presence becomes hard.** Knowing who is online in a workspace is trivial when everyone holds a
    connection to one place, and is a gossip or DHT problem otherwise, with eventual consistency and
    unstable membership.
  - **Deferred delivery has nowhere to live.** Sending to a session that is offline right now is a
    core workspace feature. In a pure mesh there is no one to hold the message.
  - **Revocation becomes a broadcast problem.** Removing a member means convincing every peer to stop
    talking to them, instead of revoking in one place.
  - Libraries in this space rely on public bootstrap nodes, so it is not really infrastructure free,
    it is somebody else's infrastructure that we do not control.
- Rejected. What people actually want from peer to peer is the security property, and end to end
  encryption over a dumb relay delivers that while keeping the four properties above.

### A hosted service we operate

- Pros: simplest possible onboarding.
- Cons: a single operator becomes a dependency and a liability for an open source tool, and it
  contradicts the self hosting goal.
- Rejected as the only option. A hosted instance can exist later as a convenience, never as a
  requirement.

## Consequences

- A relay must be easy to stand up. One command, or one container, or it will not be self hosted in
  practice.
- The relay is untrusted by design. Keys never leave the connectors. This has to be true in the code,
  not only in the README.
- Message ordering is per conversation, with a sequence number carried in the message, so gaps can be
  detected on reconnect without depending on clocks that belong to different machines.
- Delivery state is observable and honest. A relay acknowledgement is not proof that an agent read
  anything. See ADR-0007.
- Adding a peer to peer transport later, for peers on the same local network where it genuinely
  works, changes only the transport implementation.
