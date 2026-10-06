# LanBaz control protocol

The control API is a JSON envelope protocol over a **loopback WebSocket**. It
exists between the daemon (`lanbazd`) and its local clients: the desktop UI and
`lanbazctl`.

## Two encodings, two purposes

| Traffic | Encoding | Defined in |
| --- | --- | --- |
| Control API (daemon ↔ local clients) | JSON envelopes | this document |
| Peer/game packets (daemon ↔ daemon) | binary framing | Phase 2, below |

JSON is never used for a game packet. The control API carries commands and
status only; bulk traffic has its own binary framing so it never pays a JSON
encode cost.

## Endpoint

```text
ws://127.0.0.1:<dynamic-port>/api
```

The port is chosen by the OS at every start (`listen_port: 0` by default) and
written to `<state-dir>/daemon.json` together with the token. There is no
well-known port: two daemons, a stale state file and a port scanner all fail to
predict it.

A second endpoint, `GET /healthz`, answers `{"status":"ok",...}`. It is a
liveness probe only and deliberately exposes no pid, rooms, peers or token.

## Envelope

Every message is one object.

```jsonc
// request
{ "id": "req-1", "type": "daemon.status", "version": 1, "payload": {} }

// success response
{ "id": "req-1", "type": "response", "version": 1, "success": true, "payload": {} }

// error response
{
  "id": "req-1", "type": "response", "version": 1, "success": false,
  "error": { "code": "UNAUTHORIZED", "message": "invalid api token" }
}

// event (server initiated)
{ "id": "evt-1", "type": "daemon.state", "version": 1, "payload": {} }
```

Rules enforced by the daemon:

- `id` is required on requests, at most 64 characters, and correlates the
  response. Clients must never assume ordering; they correlate by id.
- `version` must equal the daemon's protocol version, otherwise the request is
  rejected with `UNSUPPORTED_VERSION`.
- A message is at most 1 MiB. The limit is applied at the WebSocket frame level,
  before JSON parsing.
- A request must not carry an `error` object.
- A parameterless method omits `payload` entirely rather than sending `null`.

## Handshake

The **first** message on every connection must be `hello`:

```json
{ "id": "req-hello", "type": "hello", "version": 1,
  "payload": { "token": "<token from daemon.json>", "client": "lanbaz-ui" } }
```

```json
{ "id": "req-hello", "type": "response", "version": 1, "success": true,
  "payload": { "daemon": "lanbazd", "version": "0.1.0", "protocol_version": 1,
               "session_id": "sess-...", "server_time": "2026-01-01T00:00:00Z" } }
```

Anything else as the first message closes the connection with
`UNAUTHORIZED`. A connection that stays unauthenticated is closed after 10
seconds. The comparison is constant time.

## Methods (Phase 0)

| Method | Request payload | Response payload |
| --- | --- | --- |
| `hello` | `{token, client?, version?}` | `HelloResponse` |
| `daemon.status` | — | `DaemonStatus` |
| `daemon.version` | — | `DaemonVersion` |
| `daemon.shutdown` | `{grace_period?, reason?}` | `ShutdownResponse` |

`daemon.shutdown` is accepted once. A second request returns
`DAEMON_SHUTTING_DOWN`. The requested grace period is clamped to 30 seconds.

`DaemonStatus`:

```json
{
  "state": "running", "version": "0.1.0", "commit": "abc123",
  "build_time": "2026-01-01T00:00:00Z", "go_version": "go1.24.6",
  "protocol_version": 1, "pid": 1234, "uptime_seconds": 42.5,
  "started_at": "2026-01-01T00:00:00Z", "api_listen": "127.0.0.1:51234",
  "api_port": 51234, "state_dir": "C:\\Users\\...\\LanBaz",
  "log_level": "info", "active_clients": 1, "room_count": 0, "peer_count": 0
}
```

## Methods (Phase 1 — implemented)

| Method | Request payload | Response payload |
| --- | --- | --- |
| `room.create` | `{name?, max_peers?, pairing_ttl_seconds?, game_profile?}` | `{room, pairing_code, pairing_uri, expires_at}` |
| `room.join` | `{pairing_code, display_name?}` | `{room, answer_code?, pending, local_peer}` |
| `room.accept` | `{room_id?, answer_code, guest_peer_id?}` | `RoomEvent` |
| `room.get` | `{room_id}` | `RoomSummary` |
| `room.list` | — | `RoomSummary[]` |
| `room.leave` | `{room_id}` | `RoomEvent` |
| `room.regenerate_pairing` | `{room_id, ttl_seconds?}` | `{pairing_code, pairing_uri, expires_at}` |
| `peer.list` | `{room_id}` | `PeerSummary[]` |
| `peer.get` | `{peer_id}` | `PeerSummary` |
| `peer.ping` | `{peer_id, count?, timeout_ms?}` | `PeerPingResult` |
| `peer.kick` | `{room_id?, peer_id}` | `PeerEvent` |

Notes that a client must not have to discover the hard way:

- `room.accept` accepts the answer code **without** `room_id`, because the code
  names its own room. A client that tracks room ids does not have to.
- `room.join` returns `pending: true`. The link is not up until the host applies
  `answer_code` via `room.accept`. This is not a failure.
- `peer.get`, `peer.ping` and `peer.kick` search every room, because a peer id is
  unique per installation but not namespaced by room.
- `peer.ping` may return a **partial** `PeerPingResult` alongside a
  `TRANSPORT_TIMEOUT` error. Two of four probes coming back is more informative
  than an error with no numbers attached, so the numbers are not discarded.

`PeerState` values, in the order the link passes through them:

```text
new → discovering → signaling → ice_checking → connected → dtls
    → data_channel → network_ready → active ⇄ degraded
                                       ↘ disconnected (recoverable)
                                       ↘ failed       (terminal)
```

## Methods (declared, later phases)

Declared centrally in `core/pkg/protocol/methods.go` so the UI, the CLI and the
daemon share one spelling. Requesting one that this build does not implement
returns `METHOD_NOT_ALLOWED`.

```text
room.close
network.status  network.interface  network.routes
game.list  game.detect  game.launch  game.profile.get
```

## Events

| Event | Payload | Meaning |
| --- | --- | --- |
| `daemon.state` | `{state, previous, timestamp}` | lifecycle state changed |
| `daemon.stopping` | `{state, previous, timestamp}` | shutdown acknowledged |
| `daemon.log` | `{timestamp, level, message}` | log line, secrets redacted |
| `room.created` | `RoomEvent` | a room was opened or joined |
| `room.closed` | `RoomEvent` | a room was left or closed |
| `peer.joined` | `PeerEvent` | a peer appeared and identified itself |
| `peer.left` | `PeerEvent` | a peer was removed |
| `peer.state` | `PeerEvent` | link state changed |
| `peer.connected` | `PeerEvent` | the link became usable in both directions |
| `peer.stats` | `PeerEvent` | telemetry changed |

`peer.connected` is a distinct event from `peer.state: active` because "the
transport opened a channel" and "something came back" are different facts, and a
UI that conflates them will show a green light on a link that carries nothing.

A slow client never blocks the daemon: its event queue is bounded and overflow
is dropped with a debug log.

## Error codes

Machine-readable and stable; messages are not part of the contract.

Phase 0:

```text
UNAUTHORIZED  BAD_REQUEST  RATE_LIMITED  NOT_FOUND  INTERNAL
PAYLOAD_TOO_LARGE  UNSUPPORTED_VERSION  TIMEOUT  DAEMON_SHUTTING_DOWN
METHOD_NOT_ALLOWED  HANDSHAKE_TIMEOUT  TOO_MANY_CONNECTIONS
CONFIG_INVALID  STATE_FILE_UNWRITABLE
```

Phase 1:

```text
NAT_BLOCKED  ICE_FAILED  STUN_UNAVAILABLE
PAIRING_EXPIRED  PAIRING_INVALID  PAIRING_REPLAY
ROOM_FULL  PEER_REJECTED  PEER_SPOOFED  TRANSPORT_TIMEOUT
```

Later phases (declared, not yet emitted): `NAT_BLOCKED`, `ICE_FAILED`,
`STUN_UNAVAILABLE`, `PAIRING_EXPIRED`, `PAIRING_INVALID`, `PAIRING_REPLAY`,
`ROOM_FULL`, `PEER_REJECTED`, `PEER_SPOOFED`, `TRANSPORT_TIMEOUT`,
`WINTUN_CREATE_FAILED`, `WINTUN_PERMISSION_DENIED`, `ROUTE_CREATE_FAILED`,
`IPAM_EXHAUSTED`, `FIREWALL_FAILED`, `GAME_NOT_FOUND`, `GAME_PROFILE_INVALID`,
`BROADCAST_NOT_SUPPORTED`, `MULTICAST_NOT_SUPPORTED`.

## State file

`<state-dir>/daemon.json`, written atomically, owner-only, removed on graceful
shutdown:

```json
{
  "state_file_version": 1,
  "pid": 1234,
  "api_listen": "127.0.0.1:51234",
  "api_port": 51234,
  "api_token": "<secret>",
  "api_path": "/api",
  "version": "0.1.0",
  "protocol_version": 1,
  "started_at": "2026-01-01T00:00:00Z",
  "exe_path": "C:\\...\\lanbazd.exe"
}
```

`lanbazctl` and the desktop shell read it to discover the daemon. A state file
whose `pid` is not running is stale and should be deleted.

## Limits and abuse resistance

| Limit | Default | Config key |
| --- | --- | --- |
| Message size | 1 MiB | fixed (`protocol.MaxMessageBytes`) |
| Concurrent control connections | 8 | `max_connections` |
| Requests per second per connection | 200 | `requests_per_second` |
| Burst per connection | 400 | `requests_burst` |
| Handshake timeout | 10 s | fixed |
| Graceful shutdown grace | 5 s | `shutdown_grace_ms` |

## Network framing (Phase 2, specified)

Peer traffic uses a fixed binary header, little endian:

```text
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Version | Type | Flags |      Payload Length (u16)            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        Session / Peer ID                      |
|                        (16 bytes, truncated)                 |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        Payload ...                            |
```

- `Version` — 1.
- `Type` — 1 data, 2 control, 3 ping, 4 pong, 5 broadcast, 6 multicast.
- `Flags` — reserved, must be zero in version 1.
- `Payload Length` — up to the transport MTU; oversized frames are dropped and
  counted, never fragmented here.

Authenticity comes from the transport (DTLS for WebRTC); this envelope carries
no cryptographic material of its own.