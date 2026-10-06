# Pairing

Two people on different networks must be able to connect with one transferable
string, with no account, no server, and no long-lived secret they have to copy by
hand.

This document describes what Phase 1 implements. Where the implementation
differs from the original design sketch, the difference and its reason are called
out rather than quietly dropped.

## The serverless constraint

LanBaz has no room server in the common case. That single decision determines
everything about pairing:

- The WebRTC offer must reach the guest *through the code*, because there is
  nowhere else for it to go.
- The guest's answer must travel back *through another code*, because the guest
  has no address the host can reach.
- Nothing may be revocable by contacting a server, so revocation is a rotation
  the host performs and guests discover on their next attempt.

The consequence is that a pairing code is **not a short number**. Radmin's
five-digit code works because a server holds the ICE credentials and the guest
only has to prove who they are. With no server, the ICE credentials *are* the
code. See [Code size](#code-size) for what that costs in practice.

## The exchange

```
host                                    guest
  |                                        |
  |  room.create                           |
  |    -> pairing code (contains offer)   |
  |  ---------- code to guest ------------>|
  |                                        |  room.join
  |                                        |    -> answer code (contains answer)
  |  <------- answer code to host --------|
  |  room.accept                           |
  |    -> link is up                       |
```

The guest is deliberately **not** connected when `room.join` returns. The
response sets `pending: true` and carries an `answer_code` for the UI to show.
The host then calls `room.accept` with it. Pretending the join completed would
mean the UI showing a peer that cannot be reached yet, and a user waiting for
something that will never happen on its own.

## Code content

```json
{
  "version": 1,
  "room_id": "lbzroom-…",
  "host_id": "…",
  "host_key": "…",
  "host_fp": "…",
  "guest_id": "…",
  "secret": "…",
  "signal": "…",
  "expires_at": "2026-01-01T00:30:00Z",
  "nonce": "…",
  "sig": "…"
}
```

- `secret` is 32 bytes of CSPRNG output. It is the HMAC key for `sig`, and it is
  what makes the code self-authenticating: there is no server holding a key that
  both sides check against.
- `signal` is the base64url SDP, kept **opaque**. The pairing package does not
  know what an SDP is, which is what lets a future UDP transport put something
  entirely different in the same field.
- `guest_id` is the transport peer id the host has already reserved. It travels
  in the offer and is echoed in the answer, so the host applies the answer to a
  link it created itself rather than to an id the answer's bearer chose. It is
  covered by `sig`, so it cannot be rewritten in transit.
- `sig` is `HMAC-SHA256(SHA256(secret), canonical payload with sig zeroed)`.

## Encoding

The payload is deflated at `BestSpeed`, rendered in a base32 alphabet that omits
`I`, `L`, `O` and `U`, chunked into groups of four and prefixed with `LBZ-`.

The decoder accepts the code in every form a user can plausibly produce: bare,
dashed, undashed, upper case, lower case, space-separated, as
`lanbaz://join/…`, with a query string appended, and with the prefix typed
without its dash. Every one of these round-trips, which is what makes the code
survive a chat client, a screenshot and a human.

## Code size

This is the honest limitation of the design, and it is worth stating plainly.

A gathered SDP with three ICE candidates is roughly 2 KiB. Deflate recovers
about 10% of that, because SDP is already dense and highly structured; base32
then expands the result by 8/5. A typical host code is therefore **around 2,400
characters**.

Consequences, all of them intended rather than accidental:

- The intended transfer is a **clipboard or a URI**, which `room.create` also
  returns as `lanbaz://join/…`. `scripts/acceptance-phase1.sh` exercises exactly
  this path.
- Reading the code aloud is possible but miserable. The base32 alphabet is chosen
  for that fallback, not for the primary path.
- Two codes are exchanged, not one. This is the largest single tax of going
  serverless.

`core/internal/pairing/size_test.go` measures this against a realistic SDP shape
and fails if an encoder change doubles it, so the number cannot regress silently.

If a shorter code becomes a requirement, the honest options are a TURN or
signalling server (which removes the SDP from the code entirely), or stripping
the offer to a minimal candidate set - which is a real negotiation risk and is
not recommended without measurement.

## Security properties

| Property | Mechanism | Status |
| --- | --- | --- |
| Integrity | `HMAC-SHA256` over the whole payload, including `expires_at` | implemented |
| Expiry | checked **after** verification; default 10 minutes | implemented |
| Replay | a code is spent the moment its answer is applied; `room.regenerate_pairing` mints a fresh one with new ICE credentials | implemented |
| MITM | both descriptions travel inside HMAC-authenticated codes, so neither SDP can be substituted | implemented |
| Peer spoofing | the guest's `hello` carries its Ed25519 key; the host checks that the announced peer id is the one that key derives | implemented |
| Room binding | an answer is refused by any room but the one that produced the offer | implemented |
| Kick | `peer.kick` closes the link and stops probing it | implemented |
| Revocation of outstanding codes | `room.regenerate_pairing` | implemented |

Verification order matters and is enforced by tests: `Decode`, then `Verify`,
then `Validate`. Checking the expiry before the signature would let an attacker
extend `expires_at` freely, because the field would not yet be covered by
anything they could not recompute.

## What a code deliberately does not do

A pairing code does not bind a peer's long-term key to its DTLS certificate. It
does not have to: both SDPs are already authenticated by `sig`, so the DTLS
fingerprint inside the SDP is pinned end to end, and a second binding would be a
second lock on the same door. The `hello` check does something different - it
stops a peer that reached the room from *claiming* somebody else's id, which the
SDP signature cannot do on its own.

## Out of scope for the MVP

- Automatic peer discovery without a code.
- Multi-hop rooms. Every peer connects to every other peer directly, which is a
  full mesh at the transport level; the *routing* layer that makes a star
  topology viable arrives with the virtual LAN.
- TURN relay fallback. The transport accepts TURN configuration but the daemon
  does not yet provision credentials for one.