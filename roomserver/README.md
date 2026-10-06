# roomserver (Phase 6 — not implemented)

This directory is intentionally empty of code in Phase 0. It will hold the
**optional** self-hosted rendezvous service.

## What it is for

A rendezvous server introduces two peers to each other so they can attempt a
direct WebRTC connection. That is the only job. Game traffic must never pass
through it.

```text
Peer A ──┐                        ┌── Peer B
         ├── rendezvous (once) ───┤
Peer A ══╪══════ direct P2P ══════╪══ Peer B
         │     game traffic       │
```

## Rules

- **Optional.** LanBaz must work without it. Phase 1 pairs peers directly from a
  pairing code that already carries the ICE candidates; no server is involved.
- **No game traffic.** If a peer cannot be reached directly, a relay is a
  separate, explicitly configured fallback (Phase 6), never a silent default.
- **Self-hosted first.** The deployment target is a single Go binary or Docker
  image that one person runs for their own group of friends. A hosted instance,
  if any, is a convenience, not a dependency.

## Planned shape

```text
roomserver/
├── cmd/roomserverd     single binary
├── internal/
│   ├── rendezvous      room registry and candidate exchange
│   └── auth            static room tokens per self-hosted instance
├── Dockerfile
└── README.md
```

## Related documents

- [docs/pairing.md](../docs/pairing.md) — the code-based path that needs no server
- [docs/architecture.md](../docs/architecture.md) — transport abstraction, where a relay plugs in
- [docs/security.md](../docs/security.md) — what a self-hosted server is trusted with