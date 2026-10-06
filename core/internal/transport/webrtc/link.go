package webrtc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// link is one peer's PeerConnection plus its two data channels.
type link struct {
	transport *Transport
	peer      transport.PeerID
	log       *slog.Logger

	pc *webrtc.PeerConnection

	// control is reliable and ordered; data is unreliable and unordered.
	control *webrtc.DataChannel
	data    *webrtc.DataChannel

	// negotiation serialises offer/answer. Two concurrent offers to the same
	// peer would interleave SDP and produce a connection that never completes.
	negotiation sync.Mutex

	// stateMu guards the channel pointers, which Pion assigns asynchronously
	// from OnDataChannel, and the closed flag.
	stateMu sync.RWMutex
	closed  bool

	connectedCh chan struct{}
	// controlOpen is closed when the reliable control channel is open, which is
	// the only signal that matters for pings and state messages. The data
	// channel can open first, so a single "usable" flag would let a caller send
	// a ping into a channel that does not exist yet.
	controlOpen chan struct{}
	closedCh    chan struct{}
	once        sync.Once
	controlOnce sync.Once
	closeOnce   sync.Once

	// state is the last state this link reported, kept only so a diagnostic
	// dump can show it. The authoritative state machine lives in the peer
	// manager; see transport.StateReporter for why there is exactly one.
	state atomic.Int32

	// telemetry.
	connectedAt atomic.Int64 // unix nanos, 0 until connected
	bytesSent   atomic.Uint64
	bytesRecv   atomic.Uint64
	framesSent  atomic.Uint64
	framesRecv  atomic.Uint64
	// rttEWMA is the smoothed round trip time in microseconds, measured by the
	// peer manager over the control channel and reported here.
	rttEWMA atomic.Int64
	// linkKind is 1 when the selected candidate pair is direct.
	direct atomic.Bool
	// selected is the selected candidate pair, empty until ICE completes.
	selectedLocal  atomic.Pointer[string]
	selectedRemote atomic.Pointer[string]

	mtu int

	// iceEvents feeds the watchdog; see watch.
	iceEvents chan webrtc.ICEConnectionState

	// publicSeen is closed when the first server-reflexive or relay candidate
	// is gathered; see waitGathering.
	publicSeen chan struct{}
	publicOnce sync.Once
}

// newLink creates a peer connection and its data channels.
func (t *Transport) newLink(peer transport.PeerID) (*link, error) {
	pc, err := t.api.NewPeerConnection(t.cfg)
	if err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInternal, "webrtc: create peer connection: %v", err)
	}

	l := &link{
		transport:   t,
		peer:        peer,
		log:         t.log.With("peer", fmtPeer(peer)),
		pc:          pc,
		connectedCh: make(chan struct{}),
		controlOpen: make(chan struct{}),
		closedCh:    make(chan struct{}),
		mtu:         t.mtu,
		iceEvents:   make(chan webrtc.ICEConnectionState, 16),
		publicSeen:  make(chan struct{}),
	}
	l.state.Store(int32(stateIndex(protocol.PeerNew)))

	// The answerer receives its channels from OnDataChannel; the offerer creates
	// them. Both paths install the same handlers.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		l.installChannel(dc)
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		l.onStateChange(s)
	})

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		l.onICEStateChange(s)
	})

	// Capture the selected candidate pair so the UI can report "direct" or
	// "relay" honestly rather than guessing.
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			t.log.Debug("ice candidate", "peer", fmtPeer(peer), "candidate", c.String())
			if c.Typ == webrtc.ICECandidateTypeSrflx || c.Typ == webrtc.ICECandidateTypeRelay {
				l.publicOnce.Do(func() { close(l.publicSeen) })
			}
		}
	})

	if err := l.createChannels(); err != nil {
		pc.Close()
		return nil, err
	}
	go l.watch(t.pending, t.grace)
	return l, nil
}

// watch decides when a link has failed. Pion's own failed timeout is off (see
// New), because it would also end the initial checking phase while a person is
// still carrying the reply code to the host.
//
//   - Before the first connection the link waits up to pending. Closing the
//     room, a new invite or the transport shutting down ends it sooner.
//   - After it has connected, a disconnection longer than grace fails it.
func (l *link) watch(pending, grace time.Duration) {
	deadline := time.NewTimer(pending)
	defer deadline.Stop()
	everConnected := false
	var lost <-chan time.Time
	var lostTimer *time.Timer
	stopLost := func() {
		if lostTimer != nil {
			lostTimer.Stop()
			lostTimer, lost = nil, nil
		}
	}
	defer stopLost()
	for {
		select {
		case <-l.closedCh:
			return
		case <-deadline.C:
			if everConnected {
				continue
			}
			l.log.Info("peer link gave up waiting for the other side", "after", pending)
			l.fail(protocol.CodeICEFailed, "the other player never connected; the invite or reply was not used in time")
			return
		case s := <-l.iceEvents:
			switch s {
			case webrtc.ICEConnectionStateConnected, webrtc.ICEConnectionStateCompleted:
				everConnected = true
				stopLost()
			case webrtc.ICEConnectionStateDisconnected:
				if everConnected && lostTimer == nil {
					lostTimer = time.NewTimer(grace)
					lost = lostTimer.C
				}
			case webrtc.ICEConnectionStateFailed:
				code, reason := classifyConnectionState(webrtc.PeerConnectionStateFailed)
				l.fail(code, reason)
				return
			}
		case <-lost:
			l.log.Info("peer link stayed disconnected; closing it", "after", grace)
			l.fail(protocol.CodeTransportTimeout, "the connection to the other player was lost")
			return
		}
	}
}

// fail reports a terminal failure once and tears the link down.
func (l *link) fail(code, reason string) {
	select {
	case <-l.closedCh:
		return
	default:
	}
	l.report(protocol.PeerFailed, code, reason)
	l.transport.forget(l)
	go l.close()
}

// createChannels installs the offerer-side data channels.
func (l *link) createChannels() error {
	// The control channel is negotiated first and is reliable: it must survive
	// whatever the data channel does. Ordered delivery means a pong can never
	// overtake the ping that caused it.
	control, err := l.pc.CreateDataChannel(ControlChannel, &webrtc.DataChannelInit{
		Ordered: boolPtr(true),
	})
	if err != nil {
		return protocol.NewErrorf(protocol.CodeInternal, "webrtc: create control channel: %v", err)
	}
	control.OnOpen(func() {
		l.log.Debug("control channel open")
		l.markControlOpen()
	})
	control.OnClose(func() { l.log.Debug("control channel closed") })
	control.OnError(func(err error) { l.log.Warn("control channel error", "error", err) })
	control.OnMessage(func(msg webrtc.DataChannelMessage) { l.onControlMessage(msg) })

	// The data channel is unreliable and unordered. Game traffic is already UDP
	// underneath; a retransmitted or reordered frame would be harmful, and the
	// game's own protocol handles loss better than SCTP would.
	data, err := l.pc.CreateDataChannel(DataChannel, &webrtc.DataChannelInit{
		Ordered:        boolPtr(false),
		MaxRetransmits: uint16Ptr(0),
	})
	if err != nil {
		return protocol.NewErrorf(protocol.CodeInternal, "webrtc: create data channel: %v", err)
	}
	data.OnOpen(func() {
		// The data channel can legitimately open before the control channel, so
		// it must not be what makes the link report ready.
		l.log.Debug("data channel open")
		l.report(protocol.PeerDataChannel, "", "")
	})
	data.OnClose(func() { l.log.Debug("data channel closed") })
	data.OnError(func(err error) { l.log.Warn("data channel error", "error", err) })
	data.OnMessage(func(msg webrtc.DataChannelMessage) { l.onDataMessage(msg) })

	l.stateMu.Lock()
	l.control, l.data = control, data
	l.stateMu.Unlock()
	return nil
}

// installChannel adopts a channel created by the remote side.
func (l *link) installChannel(dc *webrtc.DataChannel) {
	switch dc.Label() {
	case ControlChannel:
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { l.onControlMessage(msg) })
		dc.OnOpen(func() {
			l.log.Debug("control channel open (remote)")
			l.markControlOpen()
		})
		dc.OnClose(func() { l.log.Debug("control channel closed (remote)") })
		l.stateMu.Lock()
		l.control = dc
		l.stateMu.Unlock()
	case DataChannel:
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { l.onDataMessage(msg) })
		dc.OnOpen(func() {
			l.log.Debug("data channel open (remote)")
			l.report(protocol.PeerDataChannel, "", "")
		})
		dc.OnClose(func() { l.log.Debug("data channel closed (remote)") })
		l.stateMu.Lock()
		l.data = dc
		l.stateMu.Unlock()
	default:
		// A channel we did not ask for is refused rather than accepted: it is
		// either a bug or a probe.
		l.log.Warn("refusing an unexpected data channel", "label", dc.Label())
		_ = dc.Close()
	}
}

func boolPtr(b bool) *bool       { return &b }
func uint16Ptr(v uint16) *uint16 { return &v }

// report forwards a state transition to the peer manager and records it for
// diagnostics.
//
// Every transition leaves this link, and none of them decide anything: a link
// that has completed ICE is not yet usable, and only the manager can say whether
// it went on to become healthy. That is why this function never returns a
// verdict of its own.
func (l *link) report(state protocol.PeerState, code, reason string) {
	l.state.Store(int32(stateIndex(state)))
	fn := l.transport.stateHandler()
	if fn == nil {
		return
	}
	fn(transport.StateEvent{Peer: l.peer, State: state, Code: code, Reason: reason})
}

func (l *link) currentState() protocol.PeerState {
	return stateFromIndex(l.state.Load())
}

// stateIndex maps a state onto a small integer for atomic storage. A string
// constant cannot be stored in an int32 directly, and interning them here keeps
// the hot path allocation-free.
func stateIndex(s protocol.PeerState) int32 {
	switch s {
	case protocol.PeerNew:
		return 1
	case protocol.PeerDiscovering:
		return 2
	case protocol.PeerSignaling:
		return 3
	case protocol.PeerICEChecking:
		return 4
	case protocol.PeerConnected:
		return 5
	case protocol.PeerDTLS:
		return 6
	case protocol.PeerDataChannel:
		return 7
	case protocol.PeerNetworkReady:
		return 8
	case protocol.PeerActive:
		return 9
	case protocol.PeerDegraded:
		return 10
	case protocol.PeerFailed:
		return 11
	case protocol.PeerDisconnected:
		return 12
	default:
		return 0
	}
}

func stateFromIndex(i int32) protocol.PeerState {
	all := []protocol.PeerState{
		"",
		protocol.PeerNew,
		protocol.PeerDiscovering,
		protocol.PeerSignaling,
		protocol.PeerICEChecking,
		protocol.PeerConnected,
		protocol.PeerDTLS,
		protocol.PeerDataChannel,
		protocol.PeerNetworkReady,
		protocol.PeerActive,
		protocol.PeerDegraded,
		protocol.PeerFailed,
		protocol.PeerDisconnected,
	}
	if i < 0 || int(i) >= len(all) {
		return ""
	}
	return all[i]
}

func (l *link) onICEStateChange(s webrtc.ICEConnectionState) {
	l.log.Info("ice state", "state", s.String())
	select {
	case l.iceEvents <- s:
	default:
	}
	switch s {
	case webrtc.ICEConnectionStateChecking:
		l.report(protocol.PeerICEChecking, "", "")
		go l.monitor()
	case webrtc.ICEConnectionStateConnected, webrtc.ICEConnectionStateCompleted:
		l.report(protocol.PeerConnected, "", "")
		l.markConnected()
	}
}

func (l *link) onStateChange(s webrtc.PeerConnectionState) {
	switch s {
	case webrtc.PeerConnectionStateConnected:
		// Pion reaches PeerConnectionStateConnected only after DTLS has
		// finished and the link is authenticated, which is exactly what
		// PeerDTLS means. The data channels open a moment later.
		l.report(protocol.PeerDTLS, "", "")
		l.markConnected()
		go l.recordSelectedPair()
	case webrtc.PeerConnectionStateDisconnected:
		l.report(protocol.PeerDisconnected, "", "")
	case webrtc.PeerConnectionStateFailed:
		code, reason := classifyConnectionState(s)
		l.report(protocol.PeerFailed, code, reason)
		l.transport.forget(l)
		go l.close()
	case webrtc.PeerConnectionStateClosed:
		l.report(protocol.PeerDisconnected, "", "")
		l.transport.forget(l)
		go l.close()
	}
}

// markControlOpen records that the reliable control channel is usable. ICE
// connecting is not enough: DTLS runs after ICE succeeds and the channels open
// after DTLS finishes, so a caller that acts on an ICE-only "connected" signal
// sends into a channel that does not exist yet and concludes the peer is broken
// when it is merely still handshaking.
func (l *link) markControlOpen() {
	l.controlOnce.Do(func() {
		close(l.controlOpen)
		l.report(protocol.PeerNetworkReady, "", "")
		l.log.Debug("control channel is usable")
	})
}

// markConnected records the first moment the link was authenticated. The
// connectedCh signal is kept for diagnostics; senders wait on controlOpen,
// because a link can pass ICE and DTLS and still have no channel to write to.
func (l *link) markConnected() {
	l.once.Do(func() {
		l.connectedAt.Store(time.Now().UnixNano())
		close(l.connectedCh)
	})
}

// ready reports whether the link can actually carry a control message. A closed
// link is never ready, even though its controlOpen channel stays closed.
func (l *link) ready() bool {
	select {
	case <-l.closedCh:
		return false
	default:
	}
	select {
	case <-l.controlOpen:
		return true
	default:
		return false
	}
}

// recordSelectedPair notes whether ICE chose a direct path or a TURN relay, so
// the UI can say "direct" or "relay" from fact rather than guess.
func (l *link) recordSelectedPair() {
	sctp := l.pc.SCTP()
	if sctp == nil || sctp.Transport() == nil || sctp.Transport().ICETransport() == nil {
		return
	}
	pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return
	}
	relayed := pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay
	l.direct.Store(!relayed)
	local, remote := pair.Local.String(), pair.Remote.String()
	l.selectedLocal.Store(&local)
	l.selectedRemote.Store(&remote)
	kind := "direct"
	if relayed {
		kind = "relay"
	}
	l.log.Info("peer link established", "path", kind, "local", local, "remote", remote)
	if fn := l.transport.linkKindHandler(); fn != nil {
		fn(l.peer, kind)
	}
}

// CreateOffer implements transport.Signalling.
func (t *Transport) CreateOffer(ctx context.Context, peer transport.PeerInfo) (transport.Description, error) {
	l, err := t.linkFor(peer, true)
	if err != nil {
		return transport.Description{}, err
	}

	l.negotiation.Lock()
	defer l.negotiation.Unlock()

	l.report(protocol.PeerDiscovering, "", "")

	offer, err := l.pc.CreateOffer(nil)
	if err != nil {
		return transport.Description{}, protocol.NewErrorf(protocol.CodeInternal, "webrtc: create offer: %v", err)
	}

	// Gathering must complete before the description is handed out: the pairing
	// code is a one-shot courier, so there is no later channel to trickle a
	// candidate through. Waiting here is what makes a single code sufficient.
	l.report(protocol.PeerSignaling, "", "")
	gatherComplete := webrtc.GatheringCompletePromise(l.pc)
	if err := l.pc.SetLocalDescription(offer); err != nil {
		return transport.Description{}, protocol.NewErrorf(protocol.CodeInternal, "webrtc: set local description: %v", err)
	}

	if err := l.waitGathering(ctx, gatherComplete); err != nil {
		return transport.Description{}, err
	}

	local := l.pc.LocalDescription()
	if local == nil {
		return transport.Description{}, protocol.NewError(protocol.CodeInternal, "webrtc: no local description after gathering")
	}
	l.report(protocol.PeerICEChecking, "", "")
	l.log.Info("invite candidates", "local", candidateSummary(local.SDP))
	return transport.Description{Kind: transport.DescriptionOffer, Data: encodeDescription(local.SDP)}, nil
}

// waitGathering waits for ICE candidate gathering, but not for the slowest
// STUN server: once one public candidate is in, the others have publicGrace to
// arrive. A filtered server would otherwise hold every invite for the full
// gatherTimeout. Partial results are fine - a host candidate alone works on
// the same LAN, and one public candidate is enough to punch most NATs.
func (l *link) waitGathering(ctx context.Context, complete <-chan struct{}) error {
	limit := time.NewTimer(gatherTimeout)
	defer limit.Stop()
	public := l.publicSeen
	var grace <-chan time.Time
	for {
		select {
		case <-complete:
			return nil
		case <-public:
			public = nil
			grace = time.After(publicGrace)
		case <-grace:
			l.log.Debug("ice gathering: continuing with the candidates so far")
			return nil
		case <-limit.C:
			select {
			case <-l.publicSeen:
			default:
				l.log.Info("no public address was found for this PC; only same-network or relayed connections can work",
					"timeout", gatherTimeout)
			}
			return nil
		case <-ctx.Done():
			return protocol.NewErrorf(protocol.CodeTimeout, "webrtc: cancelled while gathering candidates")
		}
	}
}

// AcceptOffer implements transport.Signalling.
func (t *Transport) AcceptOffer(ctx context.Context, peer transport.PeerInfo, remote transport.Description) (transport.Description, error) {
	if len(remote.Data) == 0 {
		return transport.Description{}, protocol.NewError(protocol.CodeBadRequest, "webrtc: empty offer")
	}
	// The answerer needs the remote offer applied before it creates its own
	// channels, so this link is built without the offerer-side channels.
	l, err := t.linkFor(peer, false)
	if err != nil {
		return transport.Description{}, err
	}

	l.negotiation.Lock()
	defer l.negotiation.Unlock()

	l.report(protocol.PeerSignaling, "", "")
	offerSDP, err := decodeDescription(remote.Data)
	if err != nil {
		return transport.Description{}, protocol.NewError(protocol.CodePairingInvalid, err.Error())
	}
	if err := l.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}); err != nil {
		return transport.Description{}, protocol.NewErrorf(protocol.CodePairingInvalid,
			"webrtc: the offer in the pairing code is not usable: %v", err)
	}

	answer, err := l.pc.CreateAnswer(nil)
	if err != nil {
		return transport.Description{}, protocol.NewErrorf(protocol.CodeInternal, "webrtc: create answer: %v", err)
	}
	gatherComplete := webrtc.GatheringCompletePromise(l.pc)
	if err := l.pc.SetLocalDescription(answer); err != nil {
		return transport.Description{}, protocol.NewErrorf(protocol.CodeInternal, "webrtc: set local description: %v", err)
	}

	if err := l.waitGathering(ctx, gatherComplete); err != nil {
		return transport.Description{}, err
	}

	local := l.pc.LocalDescription()
	if local == nil {
		return transport.Description{}, protocol.NewError(protocol.CodeInternal, "webrtc: no local description after gathering")
	}
	l.report(protocol.PeerICEChecking, "", "")
	l.log.Info("reply candidates", "local", candidateSummary(local.SDP), "remote", candidateSummary(offerSDP))
	return transport.Description{Kind: transport.DescriptionAnswer, Data: encodeDescription(local.SDP)}, nil
}

// ApplyAnswer implements transport.Signalling.
func (t *Transport) ApplyAnswer(ctx context.Context, peer transport.PeerInfo, remote transport.Description) error {
	if len(remote.Data) == 0 {
		return protocol.NewError(protocol.CodeBadRequest, "webrtc: empty answer")
	}
	t.mu.RLock()
	l, ok := t.links[peer.ID]
	t.mu.RUnlock()
	if !ok {
		return transport.ErrPeerUnknown
	}

	l.negotiation.Lock()
	defer l.negotiation.Unlock()

	answerSDP, err := decodeDescription(remote.Data)
	if err != nil {
		return protocol.NewError(protocol.CodePairingInvalid, err.Error())
	}
	if err := l.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}); err != nil {
		return protocol.NewErrorf(protocol.CodePairingInvalid,
			"webrtc: the answer is not usable: %v", err)
	}
	l.log.Info("reply applied", "remote", candidateSummary(answerSDP))
	l.report(protocol.PeerICEChecking, "", "")

	// The answer completes the handshake, but ICE finishing is not the same as
	// being able to talk: DTLS and the data channels open afterwards. Returning
	// on the ICE signal would hand the caller a link that rejects its first
	// control message, which is exactly how a peer looks broken when it is only
	// still handshaking. So wait for the reliable channel instead.
	if err := l.waitControlOpen(connectTimeout); err != nil {
		return protocol.NewErrorf(protocol.CodeTransportTimeout,
			"webrtc: the control channel did not open within %s: %v", connectTimeout, err)
	}
	return nil
}

// WaitConnected implements transport.Signalling.
func (t *Transport) WaitConnected(ctx context.Context, peer transport.PeerInfo) error {
	l, ok := t.link(peer.ID)
	if !ok {
		return transport.ErrPeerUnknown
	}
	select {
	case <-l.controlOpen:
		return nil
	case <-l.closedCh:
		return transport.ErrNotConnected
	default:
	}
	wait := connectTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 && d < wait {
			wait = d
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-l.controlOpen:
		return nil
	case <-l.closedCh:
		return transport.ErrNotConnected
	case <-timer.C:
		return protocol.NewErrorf(protocol.CodeTransportTimeout,
			"webrtc: %s did not connect within %s", peer.ID, wait)
	case <-ctx.Done():
		return protocol.NewErrorf(protocol.CodeTimeout, "webrtc: cancelled while waiting for the peer")
	}
}

// linkFor returns the existing link for a peer or creates one. createChannels is
// only true for the offerer, because the answerer receives its channels through
// OnDataChannel and creating its own would raise a duplicate-label error.
func (t *Transport) linkFor(peer transport.PeerInfo, offerer bool) (*link, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, transport.ErrClosed
	}
	if existing, ok := t.links[peer.ID]; ok {
		t.mu.Unlock()
		return existing, nil
	}
	t.mu.Unlock()

	l, err := t.newLink(peer.ID)
	if err != nil {
		return nil, err
	}
	if !offerer {
		// Drop the offerer-side channels: this side will adopt the remote's.
		l.stateMu.Lock()
		control, data := l.control, l.data
		l.control, l.data = nil, nil
		l.stateMu.Unlock()
		if control != nil {
			_ = control.Close()
		}
		if data != nil {
			_ = data.Close()
		}
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		l.close()
		return nil, transport.ErrClosed
	}
	// Another goroutine may have raced us here; keep the first link so both
	// callers observe the same connection.
	if existing, ok := t.links[peer.ID]; ok {
		t.mu.Unlock()
		l.close()
		return existing, nil
	}
	t.links[peer.ID] = l
	t.mu.Unlock()
	return l, nil
}

// SendControl delivers a control message. It blocks until the control channel is
// open, because control traffic is small, reliable and must not be dropped.
func (t *Transport) SendControl(peer transport.PeerID, payload []byte) error {
	if len(payload) > maxControlMessage {
		return &transport.PayloadTooLargeError{Size: len(payload), Max: maxControlMessage}
	}
	l, ok := t.link(peer)
	if !ok {
		return transport.ErrPeerUnknown
	}
	// A control message is small, reliable and must not be dropped, so waiting
	// briefly for the channel is correct here in a way it would not be on the
	// data path.
	if err := l.waitControlOpen(controlOpenWait); err != nil {
		return err
	}
	l.stateMu.RLock()
	dc := l.control
	l.stateMu.RUnlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return transport.ErrNotConnected
	}
	if err := dc.Send(payload); err != nil {
		return protocol.NewErrorf(protocol.CodeInternal, "webrtc: send control: %v", err)
	}
	l.bytesSent.Add(uint64(len(payload)))
	l.framesSent.Add(1)
	return nil
}

// waitControlOpen blocks until the control channel is open or the wait expires.
func (l *link) waitControlOpen(wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-l.controlOpen:
		return nil
	case <-l.closedCh:
		return transport.ErrNotConnected
	case <-timer.C:
		return transport.ErrNotConnected
	}
}

// Send implements transport.Transport.
func (t *Transport) Send(peer transport.PeerID, packet []byte) error {
	if len(packet) > t.mtu {
		return &transport.PayloadTooLargeError{Size: len(packet), Max: t.mtu}
	}
	l, ok := t.link(peer)
	if !ok {
		return transport.ErrPeerUnknown
	}
	l.stateMu.RLock()
	dc := l.data
	l.stateMu.RUnlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return transport.ErrNotConnected
	}
	if err := dc.Send(packet); err != nil {
		return protocol.NewErrorf(protocol.CodeInternal, "webrtc: send data: %v", err)
	}
	l.bytesSent.Add(uint64(len(packet)))
	l.framesSent.Add(1)
	return nil
}

// onControlMessage hands a control frame to the registered handler. The peer
// manager installs one; keeping it here means the transport never imports the
// peer package and the dependency stays one-way.
func (l *link) onControlMessage(msg webrtc.DataChannelMessage) {
	l.bytesRecv.Add(uint64(len(msg.Data)))
	l.framesRecv.Add(1)
	h := l.transport.controlHandler()
	if h == nil {
		return
	}
	h(l.peer, msg.Data)
}

// onDataMessage pushes a game packet into the transport's receive channel.
func (l *link) onDataMessage(msg webrtc.DataChannelMessage) {
	l.bytesRecv.Add(uint64(len(msg.Data)))
	l.framesRecv.Add(1)

	// Copy: Pion reuses the read buffer for the next frame, and the router hands
	// the payload on to the virtual LAN asynchronously.
	payload := make([]byte, len(msg.Data))
	copy(payload, msg.Data)

	t := l.transport
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return
	}
	select {
	case t.packets <- transport.Packet{
		Peer:       l.peer,
		Payload:    payload,
		ReceivedAt: time.Now(),
	}:
	default:
		// A full receive queue means the consumer cannot keep up. Dropping is
		// the correct choice for game traffic: the game's own protocol will
		// recover, and blocking here would stall the whole data channel.
		l.log.Debug("receive queue is full, dropping a packet", "size", len(payload))
	}
}

func (l *link) stats() transport.TransportStats {
	out := transport.TransportStats{
		Transport:     "webrtc",
		Direct:        l.direct.Load(),
		BytesSent:     l.bytesSent.Load(),
		BytesReceived: l.bytesRecv.Load(),
	}
	if us := l.rttEWMA.Load(); us > 0 {
		out.RTT = time.Duration(us) * time.Microsecond
	}
	if nanos := l.connectedAt.Load(); nanos > 0 {
		out.ConnectedAt = time.Unix(0, nanos)
	}
	return out
}

func (l *link) close() {
	l.closeOnce.Do(func() {
		l.stateMu.Lock()
		l.closed = true
		control, data := l.control, l.data
		l.control, l.data = nil, nil
		l.stateMu.Unlock()

		if control != nil {
			_ = control.Close()
		}
		if data != nil {
			_ = data.Close()
		}
		if l.pc != nil {
			_ = l.pc.Close()
		}
		close(l.closedCh)
	})
}

// ReportRTT records a measured round trip time on the link so Stats can report
// it. The peer manager owns the measurement; this only stores the result.
func (l *link) ReportRTT(d time.Duration) {
	l.rttEWMA.Store(d.Microseconds())
}

// ReportDirect records whether the selected candidate pair is direct.
func (l *link) ReportDirect(direct bool) { l.direct.Store(direct) }

// Close tears down a single peer link.
func (t *Transport) ClosePeer(peer transport.PeerID) error { return t.Close(peer) }

// candidateSummary counts the candidate types in an SDP, for the log: a reply
// with no srflx candidate means that side found no public address.
func candidateSummary(sdp string) string {
	counts := map[string]int{}
	for _, line := range strings.Split(sdp, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "a=candidate:") {
			continue
		}
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "typ" {
				counts[f[i+1]]++
				break
			}
		}
	}
	return fmt.Sprintf("host=%d srflx=%d prflx=%d relay=%d", counts["host"], counts["srflx"], counts["prflx"], counts["relay"])
}
