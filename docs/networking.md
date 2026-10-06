# Networking (Phase 2 implemented, Phase 3 design)

Phase 1 implemented the link layer: two peers reach each other and exchange
measured traffic, with no address space at all. Phase 2 adds the address space —
a room is a `/24` out of `10.200.0.0/16`, every machine in it has an address, and
an IP packet a game sends reaches the other machine's stack.

Phase 3 is discovery: making the games that look for players on a LAN find each
other. It is designed at the bottom of this file and not yet built.

## What Phase 1 left

```text
peer A ──control channel (reliable, ordered)──▶ peer B
       ──data channel (unreliable, unordered)──▶
```

`transport.Packet` already carried a `Peer` field identifying the link a packet
arrived on, with `Src` and `Dst` deliberately left empty — they are the virtual
addresses, which did not exist yet. Folding the peer id into `Src` would have
worked for one phase and then silently collided with a real address in the next,
so the two stayed separate.

## The packet path

```text
Game → adapter → Service (3 goroutines) → Router → transport → remote daemon
Game ← adapter ← Service                ← Router ← transport ← remote daemon
```

`network.Service` owns three goroutines and one queue:

| goroutine | reads | writes |
| --- | --- | --- |
| `readAdapter` | the virtual adapter | the transport |
| `readTransport` | the transport | the router's decision |
| `writeAdapter` | `sendQueue` | the virtual adapter |

The service is deliberately ignorant of both ends: it sees a `transport.Transport`
and a `network.Adapter`, so the Wintun driver and the in-memory adapter are
interchangeable and testable without a driver or an administrator.

## Layer 3, not layer 2

There is no Ethernet bridging, no ARP emulation and no MAC learning. Wintun is an
L3 adapter and the MVP treats it as one.

```text
host    10.200.17.1
peer A  10.200.17.2
peer B  10.200.17.3
peer C  10.200.17.4
```

## IPAM (`network/ipam`)

```text
10.200.<room index>.0/24
  .1        room host
  .2 – .254 guest leases
```

The `/24` is derived from the room id by FNV-1a, so both sides compute the same
subnet without negotiating it. `Reserve` claims it; a second room on the same
machine whose index is already taken probes forward and is told it did, because
the room then has to reissue its pairing code against the subnet it actually got.
A guest never reserves: it adopts the subnet its code names, signed by the room
secret.

The host is `.1` in every room, which keeps the gateway predictable for a game
that insists on being told one. Guests get `.2` upward; `.0` and `.255` are
never handed out.

## Routing table (`network/router.go`)

```text
Destination          Next hop
10.200.17.0/24       local, on-link
10.200.17.1          local
10.200.17.2          peer A
10.200.17.3          peer B
default              (not installed: LanBaz is never the default gateway)
```

A packet to an address nobody in the table holds goes to the hub, which is what
makes guest-to-guest work without a mesh. A packet whose source address is not
the address that link is leased is dropped and counted as `Spoofed`; on a guest
the rule loosens to "in the subnet and not me", because a relayed packet arrives
from the hub carrying the *original* sender's address, not the hub's.

LanBaz never installs a default route. A game that resolves or dials outside the
room subnet keeps using the physical adapter.

## Hub-and-spoke

```text
            Peer B
               |
Peer C ----  HUB  ---- Peer D
               |
            Peer E
```

The host is the only peer with a full membership, which is why it is the only one
that allocates. A guest's traffic to another guest costs exactly one hop: it goes
to the host, the host forwards it and decrements the TTL once, and the recipient
accepts it because the source is inside the subnet and is not itself.

Mesh is deliberately not in the MVP: a hub keeps NAT traversal to one negotiated
pair per guest and keeps the relay logic in one place.

## Broadcast and multicast

`255.255.255.255` and the room's own subnet broadcast are relayed by the hub to
every peer except the sender, and the hub keeps a copy for its own games.

Multicast (`224.0.0.0/4`) currently takes the same path — it is relayed, not
group-filtered. That is enough for mDNS and Minecraft discovery to travel, and
it is deliberately not smarter: per-group membership (Phase 3) is only worth
building once there is a reason not to relay a group nobody joined.

Amplification is the abuse vector here, so the router keeps a token bucket per
peer (100 packets/s, burst 100) and drops the excess as `RateLimited`. The bucket
is swept when it has been idle for five minutes so a room with churning guests
does not accumulate buckets forever.

## Backends

`network.Adapter` has two implementations:

| backend | where it is used | what it is for |
| --- | --- | --- |
| `wintun` | Windows, real deployments | the actual virtual adapter |
| `memory` | tests, and `LANBAZ_NETWORK_BACKEND=memory` | a deterministic adapter that needs no driver |

`daemon.initNetwork` picks the backend from `--network-backend`
(`auto`, `wintun`, `memory`, `off`), falling back to memory when Wintun is
unavailable so a machine without the driver still gets a room, a peer list and an
honest `network.status` saying the packet path is missing.

## The control surface

```text
network.status      the whole picture for one room
network.routes      just the routing table
network.interface   just the addressing, i.e. what to type into the game
```

All three take an optional room id and answer with the first room when it is
omitted, because the network is the part most likely to be missing on a given
machine and a UI that could only ask about it once a room existed would have
nothing to show a user whose first attempt is what failed. `lanbazctl network`
renders the same three.

`network.status` also reports counters (`Delivered`, `Forwarded`, `Relayed`,
`Dropped`, `Spoofed`, `RateLimited`, `NoRoute`, `Expired`), which is what turns
"I cannot see the other players" into a question that can be answered.

## Phase 3 — discovery (design)

```go
type DiscoveryHandler interface {
    Handle(packet Packet) ([]Packet, error)
}
```

Planned implementations:

- `MinecraftDiscovery` — parses the LAN announcement payload and answers a query
  with the room's host address.
- `MDNSDiscovery` — forwards mDNS queries and responses so services on other
  peers become visible.
- `SSDPDiscovery` — the same for UPnP/SSDP.

A handler only translates a packet into the reply the local game expects. The
routing above it stays protocol-agnostic.

| Group | Port | Used by |
| --- | --- | --- |
| `224.0.0.251` | 5353 | mDNS |
| `224.0.0.251` | 4445 | Minecraft LAN discovery |

## The L3 caveat

Wintun is an L3 adapter, so broadcast and multicast delivery is not guaranteed by
the driver. This is the single biggest technical risk in the project; see the
"Known risk" section of [architecture.md](architecture.md) for the mitigation
plan and Plan B (TAP-Windows6 behind the same `network.Adapter` interface).