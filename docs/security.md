# Security

Security is designed in from the first line of code, not retrofitted. This
document records what Phases 0 and 1 implement, what they deliberately leave
out, and the requirements later phases must meet.

## Threat model

| Threat | Mitigation | Status |
| --- | --- | --- |
| Another local process drives the daemon | loopback bind + mandatory token handshake | implemented |
| Another user on the machine reads the token | owner-only ACL / 0600 on the state file | implemented |
| A local client floods the daemon | per-connection token bucket, message cap, connection cap | implemented |
| A client exhausts daemon memory | 1 MiB frame limit applied before parsing | implemented |
| The token leaks into logs or the UI | redaction handler, key=value backstop, token never in React state | implemented |
| A stale state file points at someone else's port | pid liveness check before connecting | implemented |
| A client keeps using a stopped daemon | control sockets are force-closed on shutdown | implemented |
| A peer spoofs another peer | peer id is derived from the Ed25519 key; the `hello` check refuses a mismatch and closes the link | implemented |
| A replayed pairing code joins a room | the code is spent when its answer is applied; `room.regenerate_pairing` mints fresh ICE credentials | implemented |
| An observer rewrites a code in transit | `HMAC-SHA256` over the whole payload, including `expires_at` and the reserved guest id | implemented |
| A MITM substitutes its own SDP | both descriptions travel inside HMAC-authenticated codes; the DTLS fingerprint inside them is pinned end to end | implemented |
| An answer is replayed into another room | the answer names its room and is refused by any other | implemented |
| A peer floods the control channel | a 4 KiB frame cap per message, decoded before dispatch | implemented |
| A hostile peer forces a huge allocation | `SettingEngine.SetSCTPMaxMessageSize` bounds an SCTP message before Pion hands it over | implemented |
| A peer fills a room with half-finished joins | pending joins are counted against capacity and expire | implemented |
| A room host reads peer game traffic | inherent to hub-and-spoke; **documented, not mitigated** | documented |

## Control API

- **Loopback only.** `config.Validate` rejects any non-loopback `listen_host`,
  and `api.New` refuses to start on a non-loopback host. There is no flag that
  enables remote binding.
- **Dynamic port.** The OS chooses a free port on every start; it is recorded in
  `daemon.json`. No well-known port to scan.
- **Mandatory `hello`.** No method is dispatched before authentication. The
  first message must be `hello` carrying the correct token, compared in constant
  time. Any other first message closes the connection.
- **Handshake timeout.** An unauthenticated connection is closed after 10
  seconds, so a silent client cannot hold a slot.
- **Rate limiting.** A token bucket per connection, 200 req/s with a burst of
  400 by default; the first arrival already consumed the bucket.
- **Connection cap.** 8 concurrent control connections by default.
- **Message cap.** 1 MiB, enforced by `SetReadLimit` at the frame level, before
  any JSON parsing. `protocol.Decode` rejects the same size independently.

## Secret handling

Secrets and their handling:

| Secret | Where it lives | Never |
| --- | --- | --- |
| API token | `daemon.json`, 0600 / owner DACL | logged, sent to the UI store, printed |
| Peer private key | `identity.key`, 0600 / owner DACL | logged, sent, included in a pairing code |
| Room secret | memory, inside a pairing code | logged, written to disk |

`acceptance-phase1.sh` asserts both of the first two properties by grepping the
daemon logs for the token and for anything matching a whole pairing code. A log
line containing a complete code is a line that can be replayed by whoever reads
the log, so the assertion is about the *code*, not just the secret.

### What the private key is and is not used for

The Ed25519 identity keys the peer's stable id and authenticates its `hello`. It
is **not** used to sign the DTLS certificate.

Pion's `NewCertificate` only accepts RSA and ECDSA keys, so binding DTLS to an
Ed25519 identity would mean deriving a signing key from the seed — a design that
buys little here, because both session descriptions already travel inside
HMAC-authenticated pairing codes. The DTLS fingerprint inside that authenticated
SDP pins the key for the session, so a second binding would be a second lock on
the same door.

What the `hello` check does add, and what the SDP signature cannot do on its own,
is stopping a peer that reached the room from *claiming* an id that belongs to a
different installation.

Two independent defences keep tokens out of logs:

1. `security.RedactingHandler` wraps the `slog` handler and masks every
   registered secret in the message and in every string attribute value.
2. `logging.maskTokens` is a backstop that masks `token=…`, `secret: …`,
   `private_key=…` and `password=…` shaped values in attribute strings.

`security.Redactor.Register` refuses values shorter than 8 characters: masking a
1–2 character "secret" would shred unrelated log output instead of protecting
anything.

## State file

Written to a temporary file in the same directory, `Sync`ed, then renamed, so a
crash cannot leave a world-readable token behind. Permissions are re-asserted
after the rename because a pre-existing target can keep its old mode. On
Windows the portable `0600` is ignored, so an explicit protected DACL granting
only the current user, SYSTEM and Administrators is applied.

## What Phases 0 and 1 do not do

Stated plainly so nobody assumes otherwise:

- **No encryption on the control API.** It is loopback-only and token
  authenticated; another local process could in principle sniff it. This is
  accepted for the MVP and documented rather than hidden.
- **No platform keystore.** The identity key lives in `identity.key` with an
  owner-only ACL. That is better than nothing and worse than DPAPI or the
  credential manager, and it is the one item here that would be worth doing
  before the project is used with anything a user would mind losing.
- **No revocation list.** `peer.kick` closes one link on this machine. A kicked
  peer cannot rejoin with the same code — the code is spent — but a code the host
  has not yet issued could still be redeemed by someone who kept an old copy
  before it was rotated.
- **TURN is user-supplied.** Settings accept TURN servers and relay is off
  until the user enables it. LanBaz provisions no credentials of its own and
  ships no default relay, so a relay only ever sees traffic when the user chose
  that server. Credentials are stored in `settings.json` (owner-only file).
- **No rate limiting on the peer control channel.** The frame size is bounded,
  and the ping loop is fixed-rate, but a peer that sends unsolicited control
  frames faster than the liveness cycle is not yet throttled.
- **One firewall rule per adapter.** When an adapter comes up, LanBaz adds an
  inbound allow rule scoped to that adapter (`-InterfaceAlias`) and to
  `10.200.0.0/16`, marks the adapter's network Private, and removes the rule
  when the adapter is destroyed. Nothing on the physical network is opened.
- **The app runs elevated.** The desktop app's manifest requests administrator
  rights, because creating and configuring the adapter needs them; the daemon
  inherits that token. The installer is per-machine (Program Files), so the
  binaries an elevated process loads - including `wintun.dll` - are not
  user-writable.

## Requirements for later phases

1. Broadcast and multicast rate limiting per peer to prevent amplification.
2. Size limits and validation on every network-facing parser, including the
   game discovery handlers.
3. Firewall rules added only through `FirewallManager`, never from UI code
   invoking `netsh` or PowerShell directly.
4. Move the identity key into the platform keystore.
5. TURN provisioning, with credentials scoped to a room and rotated on kick.
6. No security mechanism removed to simplify a phase. If something is genuinely
   out of scope, it is written down here first.
## Room messaging, mesh, presence and names (0.3)

- **Room messages** (chat, presence, mesh signalling) ride the authenticated
  control channel. A message arriving directly from its origin must carry that
  link's announced identity; only the host may relay on behalf of others.
  Messages are deduplicated by id and chat is rate limited per origin (2/s,
  burst 8), so a guest cannot flood the room through the host's relay.
- **Mesh links** are negotiated with offers and answers relayed by the host.
  The receiving guest pins the expected identity of the dialling peer, so a
  link whose hello does not match is dropped. A failed mesh link changes
  nothing: traffic keeps using the host.
- **Presence** is a label plus ports. A remote join link is only shown if it
  points at that player's own room address, so a crafted presence cannot make
  a click open an arbitrary URL; the shell additionally allows only
  `steam://connect`, `steam://run`, `minecraft://` and `samp://` links.
- **Game detection** reads the process list and the socket tables; it never
  opens a game process or reads its memory.
- **Name resolution** answers mDNS/LLMNR queries for player names on the
  LanBaz adapter only, with addresses inside the room subnet.
- **Classic LAN (L2)** uses the Microsoft-signed TAP-Windows6 driver, installed
  only when the user asks. The switch binds at most 8 MAC addresses to a link
  and refuses a MAC already owned by another link (anti-spoofing).
