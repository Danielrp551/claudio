# Architecture

This is the shape of the system. The reasoning behind each choice lives in
[`decisions/`](decisions/), and the protocol it rests on is described in [`protocol.md`](protocol.md).

## The problem in one paragraph

Claude Code sessions on one machine already discover and message each other, and that experience is
good. What does not exist is a workspace shared by different people with different accounts. Cross
machine messaging exists but is scoped to a single account. So the workspace layer has to be built,
while the last hop on each machine can stay native.

## Layers

```
person --> machine --> profile --> session
```

1. **Control plane.** Workspaces, owners, invitations redeemed for revocable memberships, the session
   directory, and presence.
2. **Transport.** A self hosted relay, with payloads encrypted end to end so the relay cannot read
   them.
3. **Connector.** One daemon per machine. It reads the session registry of every profile the user
   configures, decides which sessions to expose, and supervises ghosts.
4. **Compatibility boundary.** One package holds every dependence on undocumented behaviour.

## What runs on a machine

```
claudio daemon                       one per machine
  |- relay connection
  |- cryptography, trust policy, routing, queue
  `- supervises children
        |- claudio ghost --id luis-api      a native peer
        |- claudio ghost --id luis-front    a native peer
        `- claudio ghost --id ws-acme       the workspace peer
```

A ghost holds one endpoint and relays frames to its parent over stdin and stdout. It does no
networking and keeps no state. When the parent dies, stdin closes, and the ghost removes its record
and exits.

## A message, end to end

```
your session   SendMessage(to: "luis-api", "the schema changed")
     |
     v  local endpoint
ghost[luis-api]          relays the frame
     |
     v  stdin and stdout
daemon                   resolves the destination, applies policy, encrypts
     |
     v  relay connection
relay                    routes, or queues if the destination is offline
     |
     v
their daemon             decrypts, resolves trust, frames the text
     |
     v  local endpoint plus auth line where required
their session            the message arrives natively
```

The reply address carried in the frame is the ghost that represents the sender on the receiving
machine, so the conversation works in both directions without either side learning anything new.

## Addressing

Each machine shows the workspace as one peer plus one peer per subscribed remote session:

```
repos-04        local   busy
luis-api        remote  idle
luis-front      remote  busy
ws-acme         remote  idle
```

Names use letters, digits, hyphens, and underscores only. The `@` mention typeahead requires double
quotes around anything else, so a name like `workspace:acme` would have to be typed as
`@"workspace:acme"`.

Promotion is automatic up to a cap the user sets, defaulting to 8, with least recently used eviction.
Nothing becomes unreachable at the cap, because evicted sessions are still reachable through the
workspace peer.

## Trust

Granted by the receiver, never claimed by the sender, resolved by taking the most restrictive value
across the workspace default, the member setting, the local policy, and any per session override.

```
peer          arrives as if from another of your own sessions
collaborator  arrives marked as another person, framed as a request. The default
source        arrives as data, with instructions not to follow it
```

What changes between levels is the framing applied as the text enters the inbox. Permissions never
change.

## Degradation

`peerProtocol` in the record and `msgV` in the frame are the two anchors. When either is unknown, the
daemon stops spawning ghosts, routes everything through the MCP tools, and says why. A release of
Claude Code can make this tool less elegant. It cannot make it dead.

## Platform differences

Absorbed by one interface with three build tagged implementations. Nothing outside `internal/ccpeer`
branches on the operating system. The differences are real and include a field carrying the same name
with incompatible meanings, so read [`protocol.md`](protocol.md) before touching that package.
