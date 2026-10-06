package peer

import (
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/identity"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Peer is one remote link and everything known about it.
//
// The manager owns all of them and is the only thing that mutates a Peer, but
// every field is guarded anyway: Summary is read by an API handler on another
// goroutine, and a data race there would show up as a peer whose state
// flickers between two values as the UI polls.
type Peer struct {
	mu sync.RWMutex

	// id is the transport-level peer id, which is the key the transport uses to
	// find the link. It is assigned when the link is created and never changes,
	// even if the remote later announces a different identity: a peer that
	// renames itself is a bug or an attack, and neither is allowed to silently
	// become a different entry.
	id transport.PeerID

	// announced is the identity the peer stated in its hello. It is empty until
	// then, and it is deliberately not used as id.
	announced protocol.PeerID
	publicKey string
	fingerprt string
	display   string

	// greeted records that we have already introduced ourselves to this peer.
	//
	// It exists because hello is a request *and* a reply: a naive
	// send-hello-answer-hello loop would never terminate, since every hello
	// provokes another. The first hello in each direction is all that is needed
	// for both sides to learn each other.
	greeted bool

	// roomID scopes the peer for event routing.
	roomID string
	isHost bool
	isSelf bool

	state  protocol.PeerState
	reason string
	code   string

	linkKind string
	// qGrade and qKind are the last link grade and path the log reported.
	qGrade, qKind string
	since         time.Time

	// missed counts consecutive lost probes. It is reset by the first
	// successful round trip, so intermittent loss over a long session cannot
	// accumulate into a disconnect that is no longer true.
	missed int

	est estimator
	log *slog.Logger
}

// newPeer creates a peer record in the initial state.
func newPeer(id transport.PeerID, roomID string, isHost, isSelf bool, log *slog.Logger, now time.Time) *Peer {
	return &Peer{
		id:       id,
		roomID:   roomID,
		isHost:   isHost,
		isSelf:   isSelf,
		state:    protocol.PeerNew,
		linkKind: "unknown",
		since:    now,
		log:      log.With("peer", string(id)),
	}
}

// ID returns the transport-level peer id.
func (p *Peer) ID() transport.PeerID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.id
}

// State returns the current state.
func (p *Peer) State() protocol.PeerState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// Missed returns the count of consecutive unanswered probes.
func (p *Peer) Missed() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.missed
}

// Announced reports the identity the peer stated, empty until its hello lands.
func (p *Peer) Announced() protocol.PeerID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.announced
}

// Greeted reports whether we have already introduced ourselves to this peer.
func (p *Peer) Greeted() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.greeted
}

// ResetMissed clears the consecutive-loss counter after a good round trip.
func (p *Peer) ResetMissed() {
	p.mu.Lock()
	p.missed = 0
	p.mu.Unlock()
}

// markGreeted records that the introduction was sent.
func (p *Peer) markGreeted() {
	p.mu.Lock()
	p.greeted = true
	p.mu.Unlock()
}

// Rank orders the states that represent forward progress. Degraded shares a rank
// with active because it is a lateral move, not an advance.
func rank(s protocol.PeerState) int {
	switch s {
	case protocol.PeerNew:
		return 0
	case protocol.PeerDiscovering:
		return 1
	case protocol.PeerSignaling:
		return 2
	case protocol.PeerICEChecking:
		return 3
	case protocol.PeerConnected:
		return 4
	case protocol.PeerDTLS:
		return 5
	case protocol.PeerDataChannel:
		return 6
	case protocol.PeerNetworkReady:
		return 7
	case protocol.PeerActive, protocol.PeerDegraded:
		return 8
	default:
		// disconnected, failed and anything unknown are not on the ladder.
		return -1
	}
}

// canTransition reports whether from -> to is a legal move.
//
// This is not defensive boilerplate; it is load bearing. Pion delivers ICE, DTLS
// and channel callbacks from its own goroutines with no ordering guarantee, so a
// "data channel open" callback can and does arrive after a "connection failed"
// callback. Without this check the late callback would walk a dead link back to
// network_ready and the UI would show a peer as connected when its socket has
// been closed. The rule is: forward is allowed, sideways among the terminal
// states is allowed, backward is not.
func canTransition(from, to protocol.PeerState) bool {
	if from == to {
		return true
	}
	if from == protocol.PeerFailed {
		// Failed is final. There is no transition back out, because the link
		// object is gone and nothing would carry traffic anyway.
		return false
	}
	if to == protocol.PeerFailed || to == protocol.PeerDisconnected {
		// Any live state may fail or drop, including one already disconnected.
		return true
	}
	if from == protocol.PeerDisconnected {
		// Recovery is real: ICE can find a new path on a connection that merely
		// went quiet. So disconnected may advance again.
		return rank(to) >= 0
	}
	fr, tr := rank(from), rank(to)
	if fr < 0 || tr < 0 {
		return false
	}
	if to == protocol.PeerDegraded {
		// Degraded shares a rank with active, so the plain "forward or level"
		// rule would let a brand new peer be declared degraded. A link can only
		// be an impaired version of something that was already up.
		return fr >= rank(protocol.PeerNetworkReady)
	}
	return tr >= fr
}

// setState applies a transition, returning false when it was refused.
func (p *Peer) setState(next protocol.PeerState, code, reason string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !canTransition(p.state, next) {
		// Refusing is the normal case for a late callback, so this is not worth
		// a warning: logging every one would bury a real failure.
		p.log.Debug("ignoring an out-of-order transition",
			"from", string(p.state), "to", string(next))
		return false
	}
	if p.state == next {
		return true
	}
	old := p.state
	p.state = next
	p.code = code
	p.reason = reason
	p.log.Debug("peer state", "from", string(old), "to", string(next), "reason", reason)
	return true
}

// adoptIdentity records the identity a peer announced, if the record is still
// blank. A second announcement is ignored: a peer that changes its key after
// saying hello is either confused or hostile, and in neither case should the
// record follow it.
func (p *Peer) adoptIdentity(id protocol.PeerID, pub []byte, display string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.announced != "" {
		if p.announced != id {
			p.log.Warn("a peer tried to change its identity", "was", string(p.announced), "now", string(id))
		}
		return
	}
	p.announced = id
	p.publicKey = base64.RawURLEncoding.EncodeToString(pub)
	p.fingerprt = identity.Fingerprint(pub)
	p.display = cleanDisplayName(display)
}

// setLinkKind records whether traffic is direct or relayed.
func (p *Peer) setLinkKind(kind string) {
	if !protocol.ValidLinkKind(kind) {
		return
	}
	p.mu.Lock()
	p.linkKind = kind
	p.mu.Unlock()
}

// Summary renders the UI view.
func (p *Peer) Summary() protocol.PeerSummary {
	p.mu.RLock()
	out := protocol.PeerSummary{
		PeerID:          p.announced,
		TransportPeerID: string(p.id),
		LinkID:          string(p.id),
		DisplayName:     p.display,
		State:           p.state,
		LinkKind:        p.linkKind,
		PublicKey:       p.publicKey,
		Fingerprint:     p.fingerprt,
		IsHost:          p.isHost,
		IsSelf:          p.isSelf,
		Since:           p.since,
	}
	p.mu.RUnlock()

	t := p.est.snapshot()
	out.RTTMillis = millis(t.RTT)
	out.JitterMs = millis(t.Jitter)
	out.PacketLoss = t.Loss
	out.RoundTrips = t.Received
	return out
}

// Event renders the transition notification the UI subscribes to.
func (p *Peer) Event(state protocol.PeerState, reason string, at time.Time) protocol.PeerEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()
	summary := protocol.PeerSummary{
		PeerID:          p.announced,
		TransportPeerID: string(p.id),
		LinkID:          string(p.id),
		DisplayName:     p.display,
		State:           state,
		LinkKind:        p.linkKind,
		PublicKey:       p.publicKey,
		Fingerprint:     p.fingerprt,
		IsHost:          p.isHost,
		IsSelf:          p.isSelf,
		Since:           p.since,
	}
	t := p.est.snapshot()
	summary.RTTMillis = millis(t.RTT)
	summary.JitterMs = millis(t.Jitter)
	summary.PacketLoss = t.Loss
	summary.RoundTrips = t.Received
	return protocol.PeerEvent{
		PeerID:          p.announced,
		TransportPeerID: protocol.PeerID(p.id),
		RoomID:          p.roomID,
		State:           state,
		Peer:            &summary,
		Reason:          reason,
		At:              at,
	}
}

// millis converts a duration to the fractional milliseconds the DTO uses.
// Keeping the unit in the protocol package means the UI never has to know that
// Go durations are nanoseconds.
func millis(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(d) / float64(time.Millisecond)
}

// DisplayName is the name the peer announced, or "" before its hello.
func (p *Peer) DisplayName() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.display
}

// IsHost reports whether this peer is the room's host.
func (p *Peer) IsHost() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.isHost
}
