# LanBaz architecture

LanBaz connects several PCs into a virtual LAN so that LAN-only games work over
the internet. It is an open source, self-hosted alternative to Radmin VPN and
Hamachi.

## Status

Phases 0, 1 and 2 are implemented: the daemon, the local control API, the
`lanbazctl` client, the desktop shell, a working serverless peer-to-peer link with
pairing and liveness measurement, and the virtual LAN — IPAM, routing, the packet
router, the Wintun adapter and a broadcast relay through the host.

Concretely, two LanBaz installations on the same machine (or two machines on one
LAN) can today exchange a pairing code, open a peer-to-peer WebRTC link with no
server involved, measure round trip time, jitter and loss on it, and carry IP
packets between each other over `10.200.<room>/24`. Discovery — the multicast and
broadcast traffic that makes LAN-only games find each other — is Phase 3 and is
not in this build.

The virtual LAN degrades rather than failing: a machine with no Wintun driver, no
administrator rights or no adapter still gets a room, a peer list and an honest
`network.status` that says the packet path is missing.

## Layering

```text
        ┌─────────────────┐
        │      Game       │
        └────────┬────────┘
                 ↓
        ┌─────────────────┐
        │  Virtual LAN    │   core/internal/network (Wintun, L3)
        └────────┬────────┘
                 ↓
        ┌─────────────────┐
        │ Packet Router   │   core/internal/network
        └────────┬────────┘
                 ↓
        ┌─────────────────┐
        │ Peer Manager    │   core/internal/peer
        └────────┬────────┘
                 ↓
        ┌─────────────────┐
        │ Transport API   │   core/internal/transport
        └───────┬─┬───────┘
                │ │
        ┌───────┘ └────────┐
        ↓                  ↓
   WebRTC MVP          UDP Future
        ↓
   STUN / ICE
        ↓
     Internet
```

The control plane is a separate, perpendicular path:

```text
   React UI (app/src)        lanbazctl
        │                        │
        └── local WebSocket ─────┘
                     ↓
        core/internal/api  (loopback only, token auth)
                     ↓
        core/internal/daemon
```

### The one rule that matters

`Room`, `Peer`, `IPAM`, `Router`, `VirtualNetwork`, the game profiles and the UI
API must never learn which transport carries a packet. Everything above the
transport line depends only on the `transport.Transport` interface:

```go
type Transport interface {
    Name() string
    Connect(ctx context.Context, peer PeerInfo) error
    Close(peer PeerID) error
    Send(peer PeerID, packet []byte) error
    Receive() <-chan Packet
    Stats(peer PeerID) (TransportStats, error)
    Ready(peer PeerID) bool
    Shutdown(ctx context.Context) error
}
```

WebRTC (Phase 1, implemented), a direct UDP transport and a relay transport are
alternative implementations behind this interface, selected per room. Swapping
them must not require edits in the layers above.

Three optional interfaces sit beside `Transport` rather than inside it, because
forcing them on every implementation would let a hypothetical raw-UDP transport
return a fake success:

```go
type Signalling interface { /* offer / answer exchange */ }
type ControlTransport interface { SendControl(peer PeerID, payload []byte) error }
type ControlReceiver interface { SetControlHandler(fn func(peer PeerID, payload []byte)) }
type StateReporter interface { SetStateHandler(fn func(StateEvent)) }
```

A transport that cannot bootstrap a link out of band returns `false` from
`SignallingCapable` and the room reports a clean capability failure rather than
failing obscurely.

## Who owns the peer state machine

Exactly one component: `core/internal/peer`.

This is a deliberate rule rather than an accident of layering. A transport also
observes ICE completing, DTLS finishing and channels opening, so it is tempting
to track state in both places. Two state machines can disagree — the transport
hears ICE complete while the manager has already written the link off as failed —
and the UI then renders whichever wrote last. So the transport reports *mechanical
facts* through `StateReporter` and the peer manager decides what they mean, using
latency, loss and history that only it sees.

The transition table in `peer/peer.go` is what makes that safe. Pion delivers its
callbacks with no ordering guarantee, so a "channel open" callback really does
arrive after a "connection failed" one. Forward transitions are allowed, terminal
ones are allowed from anywhere, and everything else is refused with a debug line
— which is what stops a dead link from being resurrected by a late callback.

## Repository layout

```text
lanbaz/
├── core/                  Go daemon (module github.com/lanbaz/lanbaz)
│   ├── cmd/lanbazd        daemon binary
│   ├── cmd/lanbazctl      CLI client
│   ├── internal/api       loopback WebSocket control API
│   ├── internal/config    configuration and precedence
│   ├── internal/daemon    lifecycle, state file, wiring
│   ├── internal/logging   logger construction and secret masking
│   ├── internal/network   VirtualNetwork, Adapter, routing, IPAM, Wintun
│   ├── internal/peer      peer state machine, liveness, RTT/loss/jitter
│   ├── internal/room      room lifecycle, pairing handshake
│   ├── internal/pairing   pairing code encode/decode/sign
│   ├── internal/security  token, state file, redactor, ACLs
│   ├── internal/transport transport abstraction + WebRTC implementation
│   └── pkg/protocol       wire contract shared by daemon and clients
├── app/                   Tauri + React + TypeScript desktop shell
├── profiles/              game profiles (JSON, editable outside the binary)
├── roomserver/            optional self-hosted rendezvous (Phase 6)
├── scripts/               build and acceptance helpers
├── tests/integration/     cross-process checks
└── docs/                  this documentation
```

## Desktop shell responsibilities

The Rust shell owns the window, the tray, autostart and the daemon sidecar
lifecycle. It must not implement any protocol logic: it reads
`<state-dir>/daemon.json` and hands the webview an address and a token. The only
exception is the graceful-stop path, which performs a two-message WebSocket
exchange (`hello` + `daemon.shutdown`) so the tray can stop the daemon.

Four pieces of product behaviour also live in the shell, and why each is there:

- **Single instance** ([singleinstance.rs](app/src-tauri/src/singleinstance.rs)):
  a Win32 named mutex taken before the Tauri builder. A second shell would race
  the first for the daemon, the tray icon and the hotkey.
- **Close-to-tray** (`on_window_event` in lib.rs): closing the window hides it.
  Quit stays explicit in the tray menu, because a game may be using the daemon
  that closing "the window" must not interrupt.
- **Daemon supervisor** ([supervisor.rs](app/src-tauri/src/supervisor.rs)): one
  thread probes the daemon every 3s; a crash is restarted with backoff (2s→30s)
  through the same `start_daemon` command the UI uses, and a user-initiated stop
  suppresses it until the next explicit start. State changes are emitted to the
  webviews on `lanbaz://daemon-supervision`.
- **No orphaned daemons** ([parent_watch.go](core/internal/daemon/parent_watch.go)):
  a `Drop` guard in the shell was tried and removed, because `Drop` only runs on
  unwind and a forced kill skips it — the exact case that matters. The guarantee
  lives in the daemon instead, which polls its parent and stops itself when that
  parent is gone. `--managed-by-shell` publishes the relationship in the state
  file so a daemon the user started by hand is never touched.
- **The in-game overlay window**: its flags, its visibility, and the global
  hotkey. That is window plumbing, not protocol, so it stays here. The overlay
  deliberately does not inject a DLL into the game process — see
  [overlay.md](overlay.md) for why, and for the one display mode it cannot beat.

There is exactly one `spawn_sidecar` path, in the crate root, used by both the UI
command and the supervisor. A second copy once lived in `daemon.rs` and silently
dropped `--managed-by-shell`, which is invisible until the orphan case is tested
by actually killing the process.

Packaging is NSIS, per-user, with the sidecar as an external binary; see
[install.md](install.md) for the build, what was verified against the real
binary, and the clean-machine checklist.

## Security posture

- The control API binds to loopback only. `config.Validate` rejects any other
  address, and `api.New` refuses to start on a non-loopback host.
- Every connection must send `hello` with the token from `daemon.json` before any
  method is dispatched.
- The state file is written atomically and restricted to the owner: mode 0600 on
  unix, a protected owner-only DACL on Windows. The identity key gets the same
  treatment, and the Windows ACL is asserted by a test rather than by review.
- Tokens, room secrets and private keys are masked by a `slog` handler that
  rewrites every log message and string attribute, plus a `key=value` backstop.
- Per-connection token bucket rate limiting, a 1 MiB message cap applied at the
  frame layer, and a bounded connection count.
- `/healthz` exposes no state, no pid and no secrets.
- Pairing codes are HMAC-authenticated, single-shot and expire. See
  [pairing.md](pairing.md) for what that does and does not cover.
- A peer that announces an identity its key does not derive has its link closed
  rather than merely ignored.

## Known risk: Wintun is an L3 adapter

**Problem.** Wintun is a layer 3 (TUN) adapter. Minecraft LAN discovery depends
on multicast `224.0.0.251:4445` and on UDP broadcast, and neither is guaranteed
to be delivered by an L3 adapter the way it is on a TAP (layer 2) adapter. Radmin
VPN presents a TAP adapter, which is why its LAN games always discover each
other.

**Why it happens.** Broadcast and multicast are Ethernet-level concepts. An L3
adapter routes IP datagrams to a point-to-point link; a game that sends to
`255.255.255.255` may not have the datagram delivered to the adapter at all,
depending on how the Windows stack handles the datagram.

**Planned mitigation.**

1. The MVP is L3 and does not pretend otherwise.
2. `network.AddressInfo` reports `Broadcast` and `Multicast` capability per
   backend, so the UI can tell the user when discovery will not work.
3. `DiscoveryHandler` (Phase 3) relays discovery packets in user space for the
   protocols it understands, which is enough for Minecraft's fixed packet shape.
4. **Plan B:** implement the same `network.Adapter` interface over
   TAP-Windows6, which gives true L2 with ARP and reliable broadcast. Because
   the adapter sits behind the interface, Plan B is a new backend, not a rewrite.

This risk is why Phase 3 ends with an explicit "Minecraft LAN discovery works"
test rather than an assumption.

## Phase roadmap

| Phase | Scope | Status |
| --- | --- | --- |
| 0 | daemon, control API, CLI, desktop shell, docs | done |
| 1 | Pion WebRTC, STUN/ICE, pairing code, liveness ping | done |
| 2 | Wintun adapter, IPAM, routes, packet router | done |
| 3 | broadcast, multicast, mDNS, Minecraft discovery | planned |
| 4 | full UI: rooms, peers, settings, tray | in progress: shell, status, settings and the rooms flow done; in-game overlay done but unverified on hardware |
| 5 | game profiles and firewall rules | planned |
| 6 | optional self-hosted room server and TURN | planned |

## Verification

| Check | Command | What it proves |
| --- | --- | --- |
| Unit and integration | `go test ./...` | state machine, estimator, pairing crypto, handshake, room flow, IPAM, routing, relay |
| Static analysis | `go vet ./...` | clean |
| Phase 0 acceptance | `scripts/acceptance.sh` | daemon, API, CLI, graceful shutdown |
| Phase 1 acceptance | `scripts/acceptance-phase1.sh` | **two real daemons** pairing, connecting and measuring with no server |
| Phase 2 acceptance | `scripts/acceptance-phase2.sh` | **two real daemons** pairing, agreeing on a subnet, and reporting a live virtual LAN with a routed peer |
| Shell and UI build | `cargo check --offline`, `cargo test --lib`, `tsc --noEmit`, `vite build` | the desktop shell and both webviews compile; supervisor backoff and single-instance naming hold their invariants |
| Installer build | `scripts/build-installer.sh` | sidecar, frontend and NSIS bundle come out of one command (needs network on first run) |
| Released binary | run `lanbaz.exe`, then `taskkill /F` it | the app starts, spawns a live daemon, survives a second launch, and leaves no orphan when force-killed |

`acceptance-phase1.sh` is the important one. It starts two separate `lanbazd`
processes with separate state directories, drives the whole flow through
`lanbazctl`, and asserts that both sides reach `active`, that a round trip comes
back with no loss, that the spent code is refused, and that no token or pairing
code reached a log file. It is the closest thing to a smoke test that this
project can have on a machine with no display.

`acceptance-phase2.sh` follows the same shape and adds the virtual LAN: it runs
both daemons on the in-memory adapter (`--network-backend memory`, because the
Wintun driver needs an administrator and this script must not need one) and
asserts that the two sides agree on the same `/24`, that each has the address the
pairing code named, that the routing table carries a route for the other machine,
and that `network.interface` answers with an address a game could be pointed at.