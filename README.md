# LanBaz

Open source LAN gaming over the internet — a lightweight, self-hosted
alternative to Radmin VPN and Hamachi.

LanBaz connects several PCs into a virtual LAN so that LAN-only games (Minecraft,
Counter-Strike, Age of Empires II, …) work across the internet without touching
the game itself.

> **Status: ready for LAN play (0.6.2).** Serverless rooms, a Wintun virtual
> LAN with automatic firewall/priority setup, broadcast/multicast relay for LAN
> discovery, direct player-to-player links, in-room chat, automatic game
> detection, `name.local` player names, Classic LAN (L2/TAP) rooms for old
> games, optional TURN relays, and a Persian/English UI with an in-game
> overlay. Walkthrough: [docs/play-with-a-friend.md](docs/play-with-a-friend.md).

## Features

- **Friends, one click.** Add a friend once with a short code
  (`LBZ-XXXXX-XXXXX`); after that, Join / Invite / Accept, no codes. Friend
  messages are end-to-end encrypted over public Nostr relays.
- **No server, no account.** A room is created on your PC; friends join with
  an invite (a link, a `.lanbaz` file, or text) and send back a reply.
  LanBaz notices invites and replies you copy and offers to use them.
- **Low lag.** Game traffic is peer to peer. Guests also link *directly* to
  each other (mesh), so guest-to-guest traffic does not detour through the
  host; it falls back to the host automatically when a direct path fails.
- **Games just find each other.** LAN discovery (broadcast/multicast, TTL-1
  included, e.g. Minecraft "Open to LAN") is relayed to everyone. Friends'
  names resolve as `name.local` in games that accept host names.
- **Game detection.** LanBaz recognises 131 LAN games (Generals Zero Hour, Battlefield 2, CoD, NFS, Warcraft III, Minecraft, …) by process and sees
  which ports they host on, then shows friends "Ali is hosting Minecraft" with
  the exact address:port and, for Steam/SA-MP titles, a one-click join.
- **Chat** in every room, in the app and in the overlay (Ctrl+Alt+C).
- **In-game overlay** (Ctrl+Alt+L): players, ping, games and chat over a
  borderless game, never injected, so anti-cheat is unaffected.
- **Classic LAN (L2) rooms** for IPX-era games, using the TAP-Windows driver.
- **Advanced settings:** MTU, adapter priority, discovery relay and budget,
  mesh, name resolution, relay-only mode, UDP port range, STUN/TURN.

## Why

| | Radmin VPN | Hamachi | LanBaz |
| --- | --- | --- | --- |
| Cost | free, closed source | free, 5-user limit | free, open source (MIT) |
| Sign-up | none | none | none |
| Game traffic | relayed through their servers | relayed | **peer to peer** |
| Your own server | no | no | yes (optional, Phase 6) |
| Ping / latency visible | no | limited | yes |

Game traffic is meant to flow directly between peers. LanBaz never needs to see
it.

## Requirements

- Windows 10/11 for the desktop app and the virtual adapter
- Go 1.24+ to build the daemon
- Node 20+ and Rust (stable) + WebView2 for the desktop shell

## Quick start

Build the installer (Windows PowerShell; Go is found on PATH or in
`..\.tools\go`) and hand the result to your friends:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build-installer.ps1
# app\src-tauri\target\release\bundle\nsis\LanBaz_0.4.2_x64-setup.exe
```

Everyone installs it and starts LanBaz (it asks for administrator rights: the
virtual adapter needs them). The host creates a room and sends each friend a
code; each friend sends the reply code back. Full walkthrough:
[docs/play-with-a-friend.md](docs/play-with-a-friend.md).

Other scripts: `scripts\build.ps1` (daemon, CLI, sidecar and driver only) and
`scripts\dev.ps1` (hot-reload desktop app). Bash equivalents live next to them.

Or drive the daemon directly:

```bash
# 1. build the daemon and the CLI
make build          # outputs ./bin/lanbazd and ./bin/lanbazctl

# 2. run the daemon as administrator, so it can create the adapter
./bin/lanbazd
# LanBaz daemon started (version 0.2.0, pid 1234)
# API listening on 127.0.0.1:51234/api

# 3. create a room; this prints the pairing code
./bin/lanbazctl room create --name "Friday"

# 4. check the virtual network came up
./bin/lanbazctl network status
# State    ready
# Adapter  wintun
# Subnet   10.200.85.0/24
# Address  10.200.85.1

# 5. stop it gracefully
./bin/lanbazctl daemon shutdown
```

On Windows use `.\bin\lanbazd.exe` and the equivalent commands.

### Run the desktop app

```bash
cd app
npm install
cd ..
make dev            # builds the sidecar, then runs `npm run tauri dev`
```

The UI shows the connection state, the daemon status and version, and a
graceful **Shutdown** button that stops the daemon.

### Verify everything

```bash
make check          # go vet + go test
make acceptance     # end-to-end Phase 0 checks against a real daemon
cd app && npm run build   # typecheck + bundle the frontend
```

## Repository layout

```text
lanbaz/
├── core/                  Go daemon
│   ├── cmd/lanbazd        daemon binary
│   ├── cmd/lanbazctl      CLI client
│   ├── internal/api       loopback WebSocket control API
│   ├── internal/daemon    lifecycle and wiring
│   ├── internal/security  token, state file, redaction, ACLs
│   ├── internal/network   VirtualNetwork (Phase 2)
│   ├── internal/transport transport abstraction (interface now)
│   └── pkg/protocol       wire contract shared by daemon and clients
├── app/                   Tauri + React + TypeScript shell
├── profiles/              game profiles (JSON, editable outside the binary)
├── roomserver/            optional self-hosted rendezvous (Phase 6)
├── third_party/wintun/    vendored network driver + provenance and checksum
├── scripts/               build, icon and acceptance helpers
├── tests/integration/     cross-process checks
└── docs/                  architecture, protocol, security, …
```

## Architecture in one page

```text
Game → Virtual LAN → Packet Router → Peer Manager → Transport → Internet
                                                        ↙ WebRTC (Phase 1)
```

The transport is an interface. WebRTC, direct UDP and a relay are interchangeable
implementations, and no layer above the transport knows which one is in use.
See [docs/architecture.md](docs/architecture.md).

## Control API

The daemon exposes a token-authenticated WebSocket on a **dynamically chosen
loopback port** and records the address in `%LOCALAPPDATA%\LanBaz\daemon.json`.
There is no fixed port, no remote binding and no unauthenticated command path.
Details: [docs/protocol.md](docs/protocol.md).

## Phase roadmap

| Phase | Scope | Status |
| --- | --- | --- |
| 0 | daemon, control API, CLI, desktop shell, docs | **done** |
| 1 | Pion WebRTC, STUN/ICE, pairing code, live ping | **done** |
| 2 | Wintun adapter, IPAM, routes, packet router | **done** |
| 3 | broadcast/multicast relay (LAN discovery, TTL-1 aware), adapter priority | **done** (raw relay; no protocol-specific handlers) |
| 4 | full UI: rooms, peers, overlay, settings, tray | **done** |
| 5 | firewall rule + Private profile **done**; game profiles (detection, launch) | partly |
| 6 | user-configured TURN relay **done**; self-hosted room server | partly |

## Known issues and risks

- **LanBaz runs as administrator.** The app's manifest requests it at every
  launch (the logon task starts it without a prompt). Creating and configuring
  the adapter needs it.
- **Hub and spoke.** Guests reach each other through the host, so the host
  should have the best connection, and the room ends when the host leaves.
- **Strict NAT on both sides needs a relay.** Peer-to-peer fails when both
  players are behind symmetric or carrier-grade NAT; configure a TURN server in
  Settings (see [docs/play-with-a-friend.md](docs/play-with-a-friend.md)).
  Settings → **Connection check** tells each player their NAT type.
- **Discovery priority.** While in a room, the LanBaz adapter has the lowest
  interface metric so broadcast/multicast discovery goes to the room; LAN
  discovery on the physical network may not show until you leave.
- **The overlay cannot cover exclusive fullscreen.** It draws without injecting
  into the game, on purpose, so that anti-cheat has nothing to detect. Use
  borderless windowed. See [docs/overlay.md](docs/overlay.md).
- **The control API is not encrypted.** It is loopback-only and token
  authenticated. See [docs/security.md](docs/security.md) for the full threat
  model.
- **Windows-only in practice.** The daemon is portable Go, but Wintun and the
  firewall rules are Windows specific.
- **The installer is unsigned.** SmartScreen warns. Code signing is a
  release-blocking step before anyone outside a LAN of friends runs it.

## Contributing

Correctness → testability → security → performance → features, in that order.
Each phase is finished when implementation, tests, build, documentation, error
handling and logging are all in place. See `Makefile` for the `check` and
`acceptance` targets.

## License

MIT — see [LICENSE](LICENSE).