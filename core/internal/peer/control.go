// Package peer turns a negotiated transport link into a monitored, authenticated
// peer: it owns the authoritative link state machine, runs the liveness loop
// that proves a link is alive rather than merely open, and turns round trip
// measurements into the numbers the UI shows.
//
// # Why this package exists separately from the transport
//
// A transport can tell you that ICE picked a candidate pair and that a data
// channel opened. It cannot tell you whether the link is any good: only a link
// that has answered a ping is known to carry traffic in both directions, and
// only repeated measurements distinguish 15 ms from 400 ms. Keeping that logic
// here means the transport stays a pure link, and swapping WebRTC for a direct
// UDP transport leaves this file untouched.
//
// # State ownership
//
// There is exactly one state machine, and it is this package's. The transport
// reports mechanical facts through transport.StateReporter; the manager decides
// what they mean. A transport that kept its own copy would produce two machines
// that could disagree, and the UI would render whichever wrote last. See
// docs/architecture.md.
package peer

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/identity"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Control frame types. These travel over the reliable control channel, so a
// lost frame is a real failure rather than something to tolerate.
const (
	// MsgHello is the first frame a peer sends once its control channel opens.
	// It carries the sender's identity so the other side can check that the peer
	// id it negotiated under is the one the key implies.
	MsgHello = "hello"
	// MsgPing is a liveness probe carrying a sequence number.
	MsgPing = "ping"
	// MsgPong answers a ping, echoing its sequence number and carrying the
	// probe's send time so the receiver does not need its own bookkeeping.
	MsgPong = "pong"
	// MsgBye announces an orderly departure.
	MsgBye = "bye"
	// MsgApp carries an application message (chat, presence, mesh signalling)
	// for the room layer. The peer package routes it without interpreting it.
	MsgApp = "app"
)

// MaxFrameBytes bounds one inbound control frame. Every frame in this protocol
// is well under 200 bytes even with a display name, so this is a hard sanity
// limit rather than a working size. It exists because the frame arrives from a
// remote host and is decoded before anything about it is trusted.
//
// App frames are the exception in size: a mesh signalling frame carries an SDP,
// so the bound is large enough for that while still far below what would let a
// peer make the daemon allocate without limit.
const MaxFrameBytes = 16 * 1024

// MaxDisplayNameLen bounds the display name a peer may claim, in runes. A name
// is rendered in a UI list; an unbounded one is both a layout problem and a way
// to smuggle control characters into logs.
const MaxDisplayNameLen = 32

// controlFrame is the wire form of a control message.
//
// The JSON keys are single letters. These frames go over a data channel that is
// itself carrying a compressed, signed SDP, so a hundred bytes saved per ping is
// free, and the format stays debuggable because the values are named.
type controlFrame struct {
	Type string `json:"t"`
	// Seq is the probe sequence number, echoed by a pong.
	Seq uint64 `json:"i,omitempty"`
	// PeerID is the sender's stable id, hex.
	PeerID string `json:"p,omitempty"`
	// PublicKey is the sender's Ed25519 public key, base64url.
	PublicKey string `json:"k,omitempty"`
	// Fingerprint is the short display form of the public key.
	Fingerprint string `json:"f,omitempty"`
	// DisplayName is what the peer wants to be called.
	DisplayName string `json:"n,omitempty"`
	// SentAtUnixNano is when a ping left, echoed in the pong.
	SentAtUnixNano int64 `json:"s,omitempty"`
	// Reason explains a bye.
	Reason string `json:"r,omitempty"`
	// App carries an application message for MsgApp frames.
	App *AppMessage `json:"a,omitempty"`
}

// AppMessage is a room-level message riding on a control frame.
type AppMessage struct {
	// Kind routes it: "chat", "presence", "mesh", ...
	Kind string `json:"k"`
	// ID deduplicates a message that reaches a node over two paths.
	ID string `json:"i"`
	// Origin is the announced id of the node that created the message. A
	// relaying host keeps it; a receiver checks it against the link it arrived
	// on when it came directly.
	Origin string `json:"o"`
	// To optionally addresses one node (mesh signalling); empty means everyone.
	To string `json:"t,omitempty"`
	// Body is the kind-specific payload.
	Body json.RawMessage `json:"b,omitempty"`
}

// encode renders a frame.
func (f controlFrame) encode() ([]byte, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInternal, "peer: encode control frame: %v", err)
	}
	if len(raw) > MaxFrameBytes {
		return nil, protocol.NewErrorf(protocol.CodePayloadTooLarge,
			"peer: control frame is %d bytes, want at most %d", len(raw), MaxFrameBytes)
	}
	return raw, nil
}

// decodeControl parses one inbound frame.
//
// Unknown fields are ignored rather than rejected. Two LanBaz builds may
// briefly disagree about the frame format across a pairing code, and refusing to
// read a frame with a field this build does not recognise would turn a forward
// compatible extension into a hard failure. The one thing that must be exact is
// the type, because that is what routes the frame.
func decodeControl(payload []byte) (controlFrame, error) {
	var f controlFrame
	if len(payload) == 0 {
		return f, protocol.NewError(protocol.CodeBadRequest, "peer: empty control frame")
	}
	if len(payload) > MaxFrameBytes {
		return f, protocol.NewErrorf(protocol.CodePayloadTooLarge,
			"peer: control frame is %d bytes, want at most %d", len(payload), MaxFrameBytes)
	}
	if err := json.Unmarshal(payload, &f); err != nil {
		return f, protocol.NewErrorf(protocol.CodeBadRequest, "peer: malformed control frame: %v", err)
	}
	switch f.Type {
	case MsgHello, MsgPing, MsgPong, MsgBye:
	case MsgApp:
		if f.App == nil || f.App.Kind == "" || f.App.ID == "" {
			return f, protocol.NewError(protocol.CodeBadRequest, "peer: app frame without kind or id")
		}
	default:
		return f, protocol.NewErrorf(protocol.CodeBadRequest,
			"peer: unknown control frame type %q", f.Type)
	}
	return f, nil
}

// parseHello validates an announced identity.
//
// The check that matters is that the peer id is the one the public key
// derives. Without it any peer that reached the room could announce an id
// belonging to somebody else, and every log line, event and access decision
// keyed on that id would point at the wrong installation. Verifying it costs a
// SHA-256 and turns a claim into an assertion.
//
// Note what this does not do: it does not bind the key to the DTLS certificate.
// It does not have to. Both session descriptions travel inside a pairing code
// authenticated by the room secret, so the DTLS fingerprint in the SDP is
// already pinned end to end, and a key/certificate binding would be a second
// lock on the same door. See docs/security.md.
func (f controlFrame) parseHello() (peerID protocol.PeerID, pub []byte, err error) {
	id := protocol.PeerID(f.PeerID)
	if !id.IsValid() {
		return "", nil, protocol.NewErrorf(protocol.CodePeerSpoofed,
			"peer: announced id %q is not a valid peer id", f.PeerID)
	}
	raw, decodeErr := base64.RawURLEncoding.DecodeString(f.PublicKey)
	if decodeErr != nil {
		return "", nil, protocol.NewErrorf(protocol.CodePeerSpoofed,
			"peer: announced key is not valid base64url")
	}
	if len(raw) != identity.PublicKeySize {
		return "", nil, protocol.NewErrorf(protocol.CodePeerSpoofed,
			"peer: announced key is %d bytes, want %d", len(raw), identity.PublicKeySize)
	}
	if got := identity.DeriveID(raw); string(got) != string(id) {
		return "", nil, protocol.NewErrorf(protocol.CodePeerSpoofed,
			"peer: announced id does not match its public key")
	}
	if f.Fingerprint != "" && f.Fingerprint != identity.Fingerprint(raw) {
		return "", nil, protocol.NewErrorf(protocol.CodePeerSpoofed,
			"peer: announced fingerprint does not match its public key")
	}
	return id, raw, nil
}

// cleanDisplayName trims a claimed display name to something safe to render and
// log. Control characters are dropped rather than escaped: a name that needs
// escaping is a name trying to be something other than a name.
func cleanDisplayName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= MaxDisplayNameLen*4 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	runes := []rune(out)
	if len(runes) > MaxDisplayNameLen {
		out = string(runes[:MaxDisplayNameLen])
	}
	return out
}

// pingInterval is how often a healthy peer is probed.
//
// Two seconds is a compromise: fast enough that a user notices a dead link
// within a few seconds, slow enough that a busy machine with fifty peers does
// not spend its time on timers. The interactive peer.ping call sends a burst
// instead of waiting for this.
const pingInterval = 2 * time.Second

// pongTimeout is how long a probe may stay outstanding before it counts as lost.
const pongTimeout = 3 * time.Second

// missedLimit is how many consecutive lost probes mark a link disconnected.
//
// One lost probe means nothing: UDP is allowed to drop, and a laptop changing
// access points drops plenty. Three in a row over six seconds is a pattern, and
// reporting a VPN link as down after a single blip trains people to ignore the
// indicator entirely.
const missedLimit = 3

// failedLimit is how many consecutive lost probes after disconnection mark a
// link failed. Recovery is possible from disconnected; failure is the end.
const failedLimit = missedLimit * 3
