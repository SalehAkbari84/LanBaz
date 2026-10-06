package peer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/identity"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// base64RawURL renders a public key the way the pairing code and the control
// frames both spell it.
func base64RawURL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Errors returned by the manager.
var (
	// ErrNoSuchPeer is returned for a peer the room has never seen.
	ErrNoSuchPeer = transport.ErrPeerUnknown
	// ErrNoControlChannel is returned when the transport cannot carry control
	// frames, which makes a link unmeasurable.
	ErrNoControlChannel = errors.New("peer: the transport has no control channel")
	// ErrNotReady is returned when a peer is asked to do something its link
	// cannot yet support.
	ErrNotReady = transport.ErrNotConnected
)

// Event is a peer transition delivered to the room, which turns it into a
// protocol event and pushes it to the UI.
type Event struct {
	// Kind is the protocol event name, e.g. EventPeerJoined.
	Kind string
	Body protocol.PeerEvent
}

// Manager owns every peer on one room's transport.
//
// One Manager per room rather than one per daemon: liveness state, pairing
// expiry and room capacity are all room-scoped, and a shared manager would need
// the same qualifiers on every method.
type Manager struct {
	roomID string

	// local is the identity announced to peers.
	localID     protocol.PeerID
	localPub    []byte
	localFP     string
	displayName string

	tr    transport.Transport
	ctrl  transport.ControlTransport
	recv  transport.ControlReceiver
	state transport.StateReporter

	log *slog.Logger

	// now is injectable so tests can drive expiry and timeouts without sleeping.
	now func() time.Time

	mu    sync.RWMutex
	peers map[transport.PeerID]*Peer

	// onEvent is called outside every lock, so an implementation may call back
	// into the manager.
	onEvent func(Event)

	// outstanding tracks probes awaiting a pong, keyed by sequence number.
	// Probes are matched by sequence rather than by arrival order so a late
	// pong still measures the right round trip.
	outMu       sync.Mutex
	outstanding map[transport.PeerID]map[uint64]time.Time
	nextSeq     uint64

	// wake nudges the liveness loop so a change that matters - a new peer, a
	// channel that just opened - is acted on immediately instead of at the next
	// tick. It is buffered to one so that a burst of transitions does not queue
	// up a burst of redundant passes.
	wake chan struct{}

	// appFn receives application frames for the room layer.
	appFn func(from *Peer, msg AppMessage)

	// expect pins the identity a link must announce, keyed by link id. A guest
	// learns the host's id from the signed pairing code before the link
	// exists, and a hello that disagrees means something else answered.
	expect map[transport.PeerID]protocol.PeerID

	closeOnce sync.Once
}

// Options configures NewManager.
type Options struct {
	RoomID string
	// LocalID and LocalPub are this daemon's identity, announced in hello.
	LocalID     protocol.PeerID
	LocalPub    []byte
	DisplayName string
	// Transport is the room's link. It must already be constructed.
	Transport transport.Transport
	Logger    *slog.Logger
	// Now is the clock; nil selects time.Now.
	Now func() time.Time
	// OnEvent receives peer transitions. It must not block.
	OnEvent func(Event)
}

// NewManager builds a manager and wires the transport's inbound handlers to it.
//
// The wiring happens here rather than at the room level because both handlers
// point back at this manager, and a room that had to remember to install them
// would eventually be a room whose peers never say hello.
func NewManager(opts Options) (*Manager, error) {
	if opts.Transport == nil {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "peer: manager needs a transport")
	}
	ctrl, ok := opts.Transport.(transport.ControlTransport)
	if !ok {
		return nil, protocol.NewErrorf(protocol.CodeConfigInvalid,
			"peer: transport %q cannot carry control frames", opts.Transport.Name())
	}
	recv, ok := opts.Transport.(transport.ControlReceiver)
	if !ok {
		return nil, protocol.NewErrorf(protocol.CodeConfigInvalid,
			"peer: transport %q cannot deliver control frames", opts.Transport.Name())
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	m := &Manager{
		roomID:      opts.RoomID,
		localID:     opts.LocalID,
		localPub:    append([]byte(nil), opts.LocalPub...),
		localFP:     identity.Fingerprint(opts.LocalPub),
		displayName: cleanDisplayName(opts.DisplayName),
		tr:          opts.Transport,
		ctrl:        ctrl,
		recv:        recv,
		log:         log.With("room", opts.RoomID),
		now:         now,
		peers:       make(map[transport.PeerID]*Peer),
		onEvent:     opts.OnEvent,
		outstanding: make(map[transport.PeerID]map[uint64]time.Time),
		wake:        make(chan struct{}, 1),
		expect:      make(map[transport.PeerID]protocol.PeerID),
	}
	if sr, ok := opts.Transport.(transport.StateReporter); ok {
		m.state = sr
		sr.SetStateHandler(m.onTransportState)
	}
	// The inbound handler is installed here rather than left to the room,
	// because forgetting it is silent: the link still connects, still measures
	// as ready, and simply never receives a pong, which looks like a flaky
	// network rather than a wiring mistake.
	recv.SetControlHandler(m.HandleControl)
	if lk, ok := opts.Transport.(interface {
		SetLinkKindHandler(func(transport.PeerID, string))
	}); ok {
		lk.SetLinkKindHandler(m.onLinkKind)
	}
	return m, nil
}

// onLinkKind records whether a link ended up direct or relayed.
func (m *Manager) onLinkKind(id transport.PeerID, kind string) {
	p, err := m.Get(id)
	if err != nil {
		return
	}
	p.setLinkKind(kind)
	m.emit(Event{Kind: protocol.EventPeerStats, Body: p.Event(p.State(), "", m.now())})
}

// ExpectIdentity pins the identity a link's hello must announce.
func (m *Manager) ExpectIdentity(link transport.PeerID, id protocol.PeerID) {
	if id == "" {
		return
	}
	m.mu.Lock()
	m.expect[link] = id
	m.mu.Unlock()
}

// Lookup finds a peer by either of its ids: the identity it announced, which is
// what every client sees, or the link id the transport uses. Clients only ever
// hold the first, so a lookup by link id alone made ping, kick and get fail for
// every peer a UI asked about.
func (m *Manager) Lookup(id string) (*Peer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p, ok := m.peers[transport.PeerID(id)]; ok {
		return p, nil
	}
	for _, p := range m.peers {
		if string(p.Announced()) == id {
			return p, nil
		}
	}
	return nil, ErrNoSuchPeer
}

// RoomID returns the room this manager serves.
func (m *Manager) RoomID() string { return m.roomID }

// LocalSummary renders the local daemon as a peer row, which is what lets the UI
// show "you" in the same list as everyone else instead of special-casing it.
//
// It carries the public key because the pairing code needs it: the code is what
// tells a guest who the host is, and a host with no key in its own summary could
// not produce one.
func (m *Manager) LocalSummary(isHost bool) protocol.PeerSummary {
	return protocol.PeerSummary{
		PeerID:      m.localID,
		DisplayName: m.displayName,
		State:       protocol.PeerActive,
		LinkKind:    "unknown",
		PublicKey:   base64RawURL(m.localPub),
		Fingerprint: m.localFP,
		IsHost:      isHost,
		IsSelf:      true,
	}
}

// Add registers a peer whose link already exists. It is called once per link,
// immediately after the transport creates it.
//
// isHost records which side of the pairing the peer was: the guest is not the
// host, and confusing the two would make the UI show a local machine as a
// remote one.
func (m *Manager) Add(id transport.PeerID, isHost bool, isSelf bool) (*Peer, error) {
	if id == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "peer: empty peer id")
	}
	m.mu.Lock()
	if existing, ok := m.peers[id]; ok {
		m.mu.Unlock()
		return existing, nil
	}
	p := newPeer(id, m.roomID, isHost, isSelf, m.log, m.now())
	m.peers[id] = p
	m.mu.Unlock()

	m.emit(Event{Kind: protocol.EventPeerJoined, Body: p.Event(protocol.PeerNew, "", m.now())})
	m.poke()
	return p, nil
}

// poke asks the liveness loop to run a pass now.
func (m *Manager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
		// A pass is already queued, which is exactly as good as another.
	}
}

// Remove drops a peer and its outstanding probes.
func (m *Manager) Remove(id transport.PeerID) {
	m.mu.Lock()
	p, ok := m.peers[id]
	if ok {
		delete(m.peers, id)
	}
	delete(m.expect, id)
	m.mu.Unlock()

	m.dropOutstanding(id)
	if !ok {
		return
	}
	_ = m.tr.Close(id)
	m.emit(Event{Kind: protocol.EventPeerLeft, Body: p.Event(protocol.PeerDisconnected, "removed", m.now())})
}

// Get returns one peer.
func (m *Manager) Get(id transport.PeerID) (*Peer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.peers[id]
	if !ok {
		return nil, ErrNoSuchPeer
	}
	return p, nil
}

// List returns every peer, ordered by id so the UI does not reshuffle on every
// poll.
func (m *Manager) List() []protocol.PeerSummary {
	m.mu.RLock()
	out := make([]protocol.PeerSummary, 0, len(m.peers))
	for _, p := range m.peers {
		out = append(out, p.Summary())
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].PeerID < out[j].PeerID })
	return out
}

// Count returns the number of known peers.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.peers)
}

// Ready reports whether a peer's link can carry traffic.
func (m *Manager) Ready(id transport.PeerID) bool { return m.tr.Ready(id) }

// SendControl delivers a control frame to a peer.
func (m *Manager) SendControl(id transport.PeerID, payload []byte) error {
	return m.ctrl.SendControl(id, payload)
}

// onTransportState is the transport.StateReporter sink.
//
// It runs on a Pion callback goroutine, so it does the minimum: record the
// transition, then let the liveness loop decide what it means. Deciding health
// here would mean sampling the estimator from inside a callback, which is how
// deadlocks and reordering bugs are manufactured.
func (m *Manager) onTransportState(ev transport.StateEvent) {
	p, err := m.Get(ev.Peer)
	if err != nil {
		// A transition for a peer nobody registered. Not worth an error: the
		// transport may report the close of a link the room already dropped.
		m.log.Debug("transition for an unknown peer", "peer", string(ev.Peer), "state", string(ev.State))
		return
	}
	if !p.setState(ev.State, ev.Code, ev.Reason) {
		return
	}
	m.emit(Event{Kind: protocol.EventPeerState, Body: p.Event(ev.State, ev.Reason, m.now())})
	if ev.State == protocol.PeerFailed {
		// A failed link has been dropped by the transport and will never carry
		// anything again. Keeping the record would leave a dead peer in the
		// room for good, counting against its capacity and showing as present.
		// Removal runs off the Pion callback goroutine, because it closes the
		// link and Pion must not be re-entered from its own callback.
		m.log.Info("peer link failed; removing it", "peer", string(ev.Peer), "reason", ev.Reason)
		go m.Remove(ev.Peer)
	}
}

// HandleControl processes one inbound control frame.
//
// It is the transport's ControlHandler, installed by NewManager. An error here
// is logged and, for a spoofed identity, acted on: a peer that claims an id its
// key does not derive gets its link closed rather than merely ignored, because
// a link that is not allowed to participate should not stay open.
func (m *Manager) HandleControl(id transport.PeerID, payload []byte) {
	p, err := m.Get(id)
	if err != nil {
		m.log.Debug("control frame from an unknown peer", "peer", string(id))
		return
	}
	f, err := decodeControl(payload)
	if err != nil {
		// A malformed frame is not fatal on its own: it could be a version
		// mismatch. Count it as a bad probe only if it is a probe, and otherwise
		// log and move on.
		m.log.Debug("discarding a control frame", "peer", string(id), "error", err)
		return
	}

	switch f.Type {
	case MsgHello:
		m.handleHello(p, f)
	case MsgPing:
		// Answering promptly is what keeps the other side's estimate honest.
		// There is no reason to defer: the frame is already small and the
		// channel is reliable.
		if err := m.reply(p, MsgPong, controlFrame{
			Type:           MsgPong,
			Seq:            f.Seq,
			SentAtUnixNano: f.SentAtUnixNano,
		}); err != nil {
			m.log.Debug("could not answer a ping", "peer", string(id), "error", err)
		}
	case MsgPong:
		m.handlePong(p, f)
	case MsgBye:
		m.log.Info("a peer said goodbye", "peer", string(id), "reason", f.Reason)
		m.Remove(id)
	case MsgApp:
		// Only identified peers may speak at the room level: the origin check
		// downstream compares against the announced identity.
		if p.Announced() == "" {
			return
		}
		m.mu.RLock()
		fn := m.appFn
		m.mu.RUnlock()
		if fn != nil {
			fn(p, *f.App)
		}
	}
}

// SetAppHandler installs the receiver for application frames.
func (m *Manager) SetAppHandler(fn func(from *Peer, msg AppMessage)) {
	m.mu.Lock()
	m.appFn = fn
	m.mu.Unlock()
}

// SendApp sends an application message to one peer.
func (m *Manager) SendApp(id transport.PeerID, msg AppMessage) error {
	p, err := m.Get(id)
	if err != nil {
		return err
	}
	if !m.tr.Ready(id) {
		return ErrNotReady
	}
	return m.reply(p, MsgApp, controlFrame{Type: MsgApp, App: &msg})
}

// BroadcastApp sends an application message to every ready, identified peer
// except the listed links. It returns how many peers it reached.
func (m *Manager) BroadcastApp(msg AppMessage, except ...transport.PeerID) int {
	skip := make(map[transport.PeerID]bool, len(except))
	for _, id := range except {
		skip[id] = true
	}
	m.mu.RLock()
	ids := make([]transport.PeerID, 0, len(m.peers))
	for id, p := range m.peers {
		if !skip[id] && p.Announced() != "" {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	sent := 0
	for _, id := range ids {
		if err := m.SendApp(id, msg); err == nil {
			sent++
		}
	}
	return sent
}

// LocalID is this node's announced identity.
func (m *Manager) LocalID() protocol.PeerID { return m.localID }

// LocalName is the display name this node announces.
func (m *Manager) LocalName() string { return m.displayName }

// handleHello records an announced identity and answers it if we have not
// already introduced ourselves.
//
// The reply matters as much as the receipt: both sides must learn each other's
// key, and a peer that never receives a hello back has no way to tell a one-way
// path from a working link. Guarding it on greeted is what stops the two from
// answering each other's hello forever.
func (m *Manager) handleHello(p *Peer, f controlFrame) {
	id, pub, err := f.parseHello()
	if err != nil {
		m.log.Warn("refusing a peer with an unusable identity",
			"peer", string(p.id), "claimed", f.PeerID, "error", err)
		m.fail(p, protocol.CodePeerSpoofed, "the announced identity does not match its key")
		return
	}
	m.mu.RLock()
	want, pinned := m.expect[p.id]
	m.mu.RUnlock()
	if pinned && want != id {
		m.log.Warn("refusing a peer whose identity does not match the pairing code",
			"peer", string(p.id), "announced", string(id), "expected", string(want))
		m.fail(p, protocol.CodePeerSpoofed, "the peer is not the host named in the pairing code")
		return
	}
	p.adoptIdentity(id, pub, f.DisplayName)
	m.log.Info("peer identified",
		"peer", string(p.id), "announced", string(id), "fingerprint", f.Fingerprint)
	m.emit(Event{Kind: protocol.EventPeerJoined, Body: p.Event(p.State(), "hello", m.now())})

	if p.Greeted() {
		return
	}
	if err := m.sendHello(p); err != nil {
		m.log.Debug("could not answer hello", "peer", string(p.id), "error", err)
	}
}

// handlePong folds a completed round trip into the estimator.
//
// The probe's send time travels in the frame rather than being looked up locally,
// so the measurement is the true round trip even if the answer comes back after
// the bookkeeping was touched. The local record is still removed, so a matching
// probe does not linger in the window until it is reaped as a loss that never
// happened.
func (m *Manager) handlePong(p *Peer, f controlFrame) {
	sentAt, tracked := m.takeOutstanding(p.id, f.Seq)

	rtt := time.Duration(0)
	switch {
	case f.SentAtUnixNano > 0:
		rtt = m.now().Sub(time.Unix(0, f.SentAtUnixNano))
	case tracked:
		rtt = m.now().Sub(sentAt)
	}
	if rtt <= 0 {
		return
	}
	p.est.observe(rtt)

	// A round trip is the only proof of a working link, so it is what promotes
	// a merely-open link to active.
	switch p.State() {
	case protocol.PeerNetworkReady, protocol.PeerDisconnected, protocol.PeerDegraded, protocol.PeerDataChannel, protocol.PeerDTLS:
		p.ResetMissed()
		m.transition(p, protocol.PeerActive, "", "")
	default:
		p.ResetMissed()
	}
}

// fail records a terminal failure and closes the link.
func (m *Manager) fail(p *Peer, code, reason string) {
	m.transition(p, protocol.PeerFailed, code, reason)
	m.Remove(p.id)
}

// transition applies a state change and emits it, but only if the change stuck.
func (m *Manager) transition(p *Peer, next protocol.PeerState, code, reason string) {
	if !p.setState(next, code, reason) {
		return
	}
	kind := protocol.EventPeerState
	if next == protocol.PeerActive {
		kind = protocol.EventPeerConnected
	}
	m.emit(Event{Kind: kind, Body: p.Event(next, reason, m.now())})
}

// reply sends a frame to a peer.
func (m *Manager) reply(p *Peer, kind string, f controlFrame) error {
	f.Type = kind
	raw, err := f.encode()
	if err != nil {
		return err
	}
	return m.SendControl(p.id, raw)
}

// SendHello announces the local identity to a peer.
func (m *Manager) SendHello(id transport.PeerID) error {
	p, err := m.Get(id)
	if err != nil {
		return err
	}
	return m.sendHello(p)
}

// sendHello introduces this daemon to one peer.
func (m *Manager) sendHello(p *Peer) error {
	if p.Greeted() {
		return nil
	}
	if err := m.reply(p, MsgHello, controlFrame{
		Type:        MsgHello,
		PeerID:      string(m.localID),
		PublicKey:   base64RawURL(m.localPub),
		Fingerprint: m.localFP,
		DisplayName: m.displayName,
	}); err != nil {
		return err
	}
	p.markGreeted()
	return nil
}

// SendBye tells peers this side is leaving, then the caller tears them down.
// Announcing the departure is what turns an abrupt disconnect into an orderly
// one, so the other side can update its peer list immediately instead of waiting
// for its own liveness loop to notice.
func (m *Manager) SendBye(reason string) error {
	m.mu.RLock()
	ids := make([]transport.PeerID, 0, len(m.peers))
	for id := range m.peers {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	var firstErr error
	for _, id := range ids {
		p, err := m.Get(id)
		if err != nil {
			continue
		}
		// A peer whose control channel never opened cannot be told anything,
		// and SendControl would wait up to 20 s for it - per peer - which is
		// how a shutdown overran its grace period.
		if !m.tr.Ready(id) {
			continue
		}
		if err := m.reply(p, MsgBye, controlFrame{Type: MsgBye, Reason: reason}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SendByeTo tells one peer this side is dropping it.
func (m *Manager) SendByeTo(id transport.PeerID, reason string) error {
	p, err := m.Get(id)
	if err != nil || !m.tr.Ready(id) {
		return err
	}
	return m.reply(p, MsgBye, controlFrame{Type: MsgBye, Reason: reason})
}

// PingResult is the outcome of an explicit measurement.
type PingResult struct {
	Sent     int
	Received int
	RTT      time.Duration
	Jitter   time.Duration
	Loss     float64
	Min      time.Duration
	Max      time.Duration
}

// Ping measures a peer now, without waiting for the next liveness tick.
//
// The default count is four because one sample has no dispersion: a single 9 ms
// round trip is entirely consistent with a link whose typical latency is 90 ms
// and which happened to be quiet. Four consecutive samples are enough to tell a
// link apart from a fluke without making the UI wait.
const (
	defaultPingCount   = 4
	defaultPingTimeout = 5 * time.Second
	pingGap            = 120 * time.Millisecond
)

// Ping runs a burst of probes and reports the measurements.
func (m *Manager) Ping(ctx context.Context, id transport.PeerID, count, timeoutMillis int) (PingResult, error) {
	p, err := m.Get(id)
	if err != nil {
		return PingResult{}, err
	}
	if count <= 0 {
		count = defaultPingCount
	}
	if count > 32 {
		return PingResult{}, protocol.NewErrorf(protocol.CodeBadRequest,
			"peer: ping count %d is out of range", count)
	}
	timeout := time.Duration(timeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultPingTimeout
	}
	if rank(p.State()) < rank(protocol.PeerNetworkReady) {
		return PingResult{}, protocol.NewErrorf(protocol.CodePeerRejected,
			"peer: %s is not connected yet", id)
	}
	if !m.tr.Ready(id) {
		return PingResult{}, protocol.NewErrorf(protocol.CodePeerRejected,
			"peer: %s has no usable link", id)
	}

	deadline := m.now().Add(timeout)
	var sent, received int
	var results []time.Duration

	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}
		seq, _, err := m.sendPing(p)
		if err != nil {
			// The control channel is gone. Report what was gathered rather than
			// discarding the burst, so the caller can say "3 of 4 went out" and
			// the failure that stopped it.
			return PingResult{Sent: sent, Received: received},
				protocol.NewErrorf(protocol.CodeTransportTimeout,
					"peer: %s could not be probed: %v", id, err)
		}
		sent++
		p.est.recordSent()

		if !m.awaitPong(ctx, p.id, seq, deadline) {
			// One lost probe does not end the burst: the remaining samples
			// still say something useful and the caller sees the loss ratio.
			p.est.recordTimeout()
		} else {
			received++
			results = append(results, p.est.snapshot().RTT)
		}
		if i < count-1 {
			select {
			case <-ctx.Done():
			case <-time.After(pingGap):
			}
		}
	}

	after := p.est.snapshot()
	out := PingResult{
		Sent:     sent,
		Received: received,
		RTT:      after.RTT,
		Jitter:   after.Jitter,
		Loss:     after.Loss,
		Min:      after.MinRTT,
		Max:      after.MaxRTT,
	}
	if received == 0 {
		return out, protocol.NewErrorf(protocol.CodeTransportTimeout,
			"peer: %s did not answer %d probes", id, sent)
	}
	return out, nil
}

// Run drives the liveness loop until ctx is cancelled.
//
// It is the only goroutine that mutates liveness state, which is what keeps the
// counters consistent without a lock around the whole loop. wake lets a state
// transition cut the wait short, so a link that opens at t+1.9s is greeted at
// t+1.9s rather than at the next tick.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
			// Drain the signal so a burst of transitions produces one extra
			// pass rather than several identical ones.
			select {
			case <-m.wake:
			default:
			}
		case <-ticker.C:
		}
		m.pass(ctx)
	}
}

// pass performs one liveness round: greet peers that have not yet introduced
// themselves, reap the probes that were not answered, and probe the rest.
func (m *Manager) pass(ctx context.Context) {
	m.mu.RLock()
	ids := make([]transport.PeerID, 0, len(m.peers))
	for id := range m.peers {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		p, err := m.Get(id)
		if err != nil {
			continue
		}
		if !m.tr.Ready(id) {
			// The transport already knows the link is gone; it will report the
			// transition. Counting probes here as well would double-count the
			// same outage.
			continue
		}
		if !p.Greeted() {
			// Introduce ourselves the moment the reliable channel exists. Doing
			// it here rather than from the transport's callback keeps the send
			// off a Pion goroutine, where a blocking write would stall ICE.
			if err := m.sendHello(p); err != nil {
				m.log.Debug("could not greet a peer", "peer", string(id), "error", err)
			}
		}
		m.reap(p)
		// A disconnected link is still probed, and deliberately so: it is the
		// only way back. Skipping it here would make the demotion permanent,
		// because the state that would have promoted the peer again is the one
		// that a pong triggers.
		switch p.State() {
		case protocol.PeerNetworkReady, protocol.PeerActive, protocol.PeerDegraded, protocol.PeerDisconnected:
		default:
			continue
		}
		if _, _, err := m.sendPing(p); err != nil {
			// The channel refused the probe. That is the same evidence as an
			// unanswered one: nothing is getting through in either direction.
			m.noteMiss(p)
			continue
		}
		p.est.recordSent()
		// The UI's latency column is live because of this event; without it
		// RTT froze at whatever the last full reload happened to read.
		m.emit(Event{Kind: protocol.EventPeerStats, Body: p.Event(p.State(), "", m.now())})
		m.noteQuality(p)
	}
}

// reap expires outstanding probes and applies the resulting demotion.
func (m *Manager) reap(p *Peer) {
	cutoff := m.now().Add(-pongTimeout)
	var (
		missed bool
		count  int
	)
	m.outMu.Lock()
	pending := m.outstanding[p.id]
	for seq, sentAt := range pending {
		if sentAt.After(cutoff) {
			continue
		}
		delete(pending, seq)
		missed = true
		count++
	}
	if len(pending) == 0 {
		delete(m.outstanding, p.id)
	}
	m.outMu.Unlock()
	if !missed {
		return
	}
	for i := 0; i < count; i++ {
		p.est.recordTimeout()
	}
	m.noteMiss(p)
}

// noteMiss counts a lost probe and demotes or fails the link as it crosses the
// configured thresholds.
func (m *Manager) noteMiss(p *Peer) {
	p.mu.Lock()
	p.missed++
	missed := p.missed
	state := p.state
	p.mu.Unlock()

	switch {
	case missed >= failedLimit:
		m.fail(p, protocol.CodeTransportTimeout, "the peer stopped answering probes")
	case missed >= missedLimit:
		if state != protocol.PeerDisconnected {
			m.transition(p, protocol.PeerDisconnected, "",
				fmt.Sprintf("%d probes went unanswered", missed))
		}
	}
}

// sendPing emits one probe and remembers when it left.
func (m *Manager) sendPing(p *Peer) (uint64, time.Time, error) {
	m.outMu.Lock()
	m.nextSeq++
	seq := m.nextSeq
	if m.outstanding[p.id] == nil {
		m.outstanding[p.id] = make(map[uint64]time.Time)
	}
	m.outMu.Unlock()

	sendAt := m.now()
	frame := controlFrame{Type: MsgPing, Seq: seq, SentAtUnixNano: sendAt.UnixNano()}
	raw, err := frame.encode()
	if err != nil {
		return 0, time.Time{}, err
	}
	if err := m.SendControl(p.id, raw); err != nil {
		return 0, time.Time{}, err
	}

	m.outMu.Lock()
	if m.outstanding[p.id] != nil {
		m.outstanding[p.id][seq] = sendAt
	}
	m.outMu.Unlock()
	return seq, sendAt, nil
}

// awaitPong blocks until the answer to seq clears its outstanding record, or
// the deadline passes.
//
// Polling is the honest option here: the answer arrives through a transport
// callback and there is no shared channel to select on. The poll interval is
// five milliseconds against a round trip measured in tens of milliseconds, so
// the resolution cannot meaningfully skew the result.
func (m *Manager) awaitPong(ctx context.Context, id transport.PeerID, seq uint64, deadline time.Time) bool {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		m.outMu.Lock()
		pending := m.outstanding[id]
		_, still := pending[seq]
		m.outMu.Unlock()
		if !still {
			// Either the pong cleared it, which is a success, or the reaper
			// expired it, which is a loss. Reaping only happens after
			// pongTimeout, so a cleared record means a fast enough answer.
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
		if !m.now().Before(deadline) {
			return false
		}
	}
}

// takeOutstanding returns the send time of a probe and removes it.
func (m *Manager) takeOutstanding(id transport.PeerID, seq uint64) (time.Time, bool) {
	m.outMu.Lock()
	defer m.outMu.Unlock()
	pending := m.outstanding[id]
	sentAt, ok := pending[seq]
	if ok {
		delete(pending, seq)
		if len(pending) == 0 {
			delete(m.outstanding, id)
		}
	}
	return sentAt, ok
}

// dropOutstanding forgets every probe for a peer.
func (m *Manager) dropOutstanding(id transport.PeerID) {
	m.outMu.Lock()
	delete(m.outstanding, id)
	m.outMu.Unlock()
}

// emit delivers an event, dropping it if no sink is installed.
func (m *Manager) emit(ev Event) {
	m.mu.RLock()
	fn := m.onEvent
	m.mu.RUnlock()
	if fn == nil {
		return
	}
	fn(ev)
}

// SetEventHandler installs the event sink after construction, for callers that
// need the manager before they can build a closure over themselves.
func (m *Manager) SetEventHandler(fn func(Event)) {
	m.mu.Lock()
	m.onEvent = fn
	m.mu.Unlock()
}

// Close releases the handlers so the transport does not retain the manager, and
// says goodbye to every peer.
//
// Clearing the handlers is not optional tidiness. The transport holds a
// reference to the manager through two closures; a room that closed without
// clearing them would keep the whole room alive and would let a late callback
// write into a room that is being torn down.
func (m *Manager) Close(ctx context.Context) error {
	var err error
	m.closeOnce.Do(func() {
		// Saying goodbye is best effort. By the time a room closes, links may
		// already be gone, and a peer that never learns we left will discover it
		// from its own liveness timeout - which is exactly what that timeout is
		// for. Failing the close over it would trade a clean shutdown for a
		// cosmetic error.
		if berr := m.SendBye("local shutdown"); berr != nil {
			m.log.Debug("a peer did not receive its goodbye", "error", berr)
		}
		m.mu.RLock()
		ids := make([]transport.PeerID, 0, len(m.peers))
		for id := range m.peers {
			ids = append(ids, id)
		}
		m.mu.RUnlock()
		for _, id := range ids {
			if cerr := m.tr.Close(id); cerr != nil && err == nil {
				err = cerr
			}
		}
		m.mu.Lock()
		m.peers = make(map[transport.PeerID]*Peer)
		m.onEvent = nil
		m.mu.Unlock()
		m.outMu.Lock()
		m.outstanding = make(map[transport.PeerID]map[uint64]time.Time)
		m.outMu.Unlock()
		if m.state != nil {
			m.state.SetStateHandler(nil)
		}
		if m.recv != nil {
			m.recv.SetControlHandler(nil)
		}
	})
	return err
}
