// Package webrtc implements the transport.Transport and transport.Signalling
// interfaces on top of Pion WebRTC.
//
// This is the first link implementation of LanBaz. It is the only package in
// the tree that imports pion/webrtc; the room, the peer manager, the virtual LAN
// and the UI see only the interfaces from internal/transport. Swapping this for
// a direct UDP transport must not touch any of them.
//
// Two data channels
//
//	control: reliable and ordered. Carries pings, keepalives, capability and
//	         state messages. Correctness beats speed here: a lost pong would
//	         read as packet loss when nothing was wrong with the game.
//	data:    unreliable and unordered. Carries game traffic in Phase 2, where
//	         a retransmitted or reordered UDP datagram is worse than a dropped
//	         one, because the game's own transport already handles that.
//
// The reliable control channel is also what makes an unreliable data channel
// safe to use: if the data channel is blocked by congestion, the control
// channel keeps flowing so the link can still be measured and torn down.
package webrtc

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	ptransport "github.com/pion/transport/v5"
	"github.com/pion/webrtc/v4"

	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Channel names. They are negotiated by Pion's in-band header, so both sides
// must agree on them.
const (
	ControlChannel = "control"
	DataChannel    = "data"
)

// Default STUN servers, used when the configuration does not name any. Pion's
// own Google STUN servers are the default; LanBaz does not hard-code a provider
// anywhere else, and the config file overrides this list entirely.
var defaultICEServers = []string{
	"stun:stun.cloudflare.com:3478",
	"stun:global.stun.twilio.com:3478",
	"stun:stun.nextcloud.com:443",
}

// lanbazRange is the address space LanBaz's own virtual adapters live in.
var lanbazRange = &net.IPNet{IP: net.IPv4(10, 200, 0, 0), Mask: net.CIDRMask(16, 32)}

// cgnatRange is 100.64.0.0/10. On a host interface it is almost always another
// VPN's tunnel (Tailscale, Hotspot Shield, ...), never a path to a friend.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// usableInterface drops interfaces that never lead to a friend: LanBaz's own
// adapters and Hyper-V/WSL virtual switches (vEthernet). Fewer candidates
// also mean shorter codes and fewer paths to try.
func usableInterface(name string) bool {
	return !strings.HasPrefix(name, "LanBaz") && !strings.HasPrefix(name, "vEthernet")
}

// usableCandidateIP decides which local addresses become host candidates.
//
// Every candidate travels inside the pairing code, and a gaming PC typically
// has several virtual adapters - Hyper-V, Radmin, Hamachi, other VPNs - plus
// an IPv6 link-local address on each. Offering all of them made codes so long
// they exceeded the decoder's limit and could not be joined at all. Addresses
// that can never connect two players in different homes are dropped here; the
// server-reflexive (public) candidate from STUN is unaffected.
func usableCandidateIP(ip net.IP) bool {
	switch {
	case ip == nil, ip.IsLoopback(), ip.IsUnspecified():
		return false
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return false
	case lanbazRange.Contains(ip), cgnatRange.Contains(ip):
		return false
	}
	if ip.To4() == nil {
		// IPv6: only global addresses are worth offering. Unique-local
		// (fc00::/7) addresses are private to one site, like RFC 1918.
		if ip[0]&0xfe == 0xfc {
			return false
		}
	}
	return true
}

// Timing constants.
const (
	// gatherTimeout bounds how long CreateOffer waits for ICE gathering. The
	// offer must carry every candidate because there is no signalling channel to
	// trickle later candidates over; a host behind a symmetric NAT needs all of
	// them. Two seconds past the STUN response is generous but bounded.
	gatherTimeout = 5 * time.Second
	// publicGrace is how long gathering continues after the first public
	// candidate, for the other STUN servers to answer.
	publicGrace = time.Second
	// connectTimeout bounds WaitConnected.
	connectTimeout = 30 * time.Second
	// pendingTimeout bounds how long a link that never connected is kept
	// alive. It is a little longer than the longest invite (room.MaxTTL), so
	// a guest's link outlives every reply code it could have produced.
	pendingTimeout = 65 * time.Minute
	// reconnectGrace is how long an established link may stay disconnected
	// before it is declared failed. ICE recovers short Wi-Fi hiccups by itself.
	reconnectGrace = 15 * time.Second
	// controlOpenWait bounds how long SendControl blocks waiting for the
	// reliable channel to finish opening. Control frames are small and must not
	// be dropped, so waiting is correct here in a way it would not be on the
	// data path; the bound keeps a wedged link from blocking the caller forever.
	controlOpenWait = 20 * time.Second
)

// maxControlMessage bounds a single control frame. The control channel carries
// small JSON-ish messages; anything larger is a bug or an attack.
const maxControlMessage = 64 * 1024

// Transport is a WebRTC-based transport. It is safe for concurrent use.
type Transport struct {
	roomID string
	log    *slog.Logger

	api *webrtc.API
	cfg webrtc.Configuration
	// mtu bounds a single data channel frame.
	mtu int

	// gatherTimeout and connectTimeout are package constants; they are recorded
	// on the transport only so diagnostics can report them.

	// pending and grace are the watchdog bounds; tests shorten them.
	pending, grace time.Duration

	mu    sync.RWMutex
	links map[transport.PeerID]*link
	// packets fans in from every peer link.
	packets chan transport.Packet
	closed  bool

	// controlFn receives every inbound control frame. The peer manager installs
	// it via SetControlHandler. Keeping the callback here rather than importing
	// the peer package keeps the dependency one-way: transport never knows who
	// consumes control traffic, only that someone does.
	controlFn ControlHandler
	controlMu sync.RWMutex

	// stateFn receives every link state transition. The peer manager installs
	// it via SetStateHandler and owns the resulting state machine; the link
	// only reports the mechanical facts of a connection.
	stateFn func(transport.StateEvent)
	kindFn  func(transport.PeerID, string)
	stateMu sync.RWMutex

	shutdownOnce sync.Once
	done         chan struct{}
}

// ControlHandler receives an inbound control frame from a peer.
//
// The payload is owned by the receiver and must be copied if it is retained.
//
// It is an alias, not a defined type, so that Transport.SetControlHandler
// satisfies transport.ControlReceiver exactly. A defined type here would force
// every caller to convert, and a conversion at an interface boundary is exactly
// the sort of thing that silently fails to compile in one of the two directions.
type ControlHandler = func(peer transport.PeerID, payload []byte)

// SetControlHandler installs the inbound control frame handler.
func (t *Transport) SetControlHandler(fn ControlHandler) {
	t.controlMu.Lock()
	t.controlFn = fn
	t.controlMu.Unlock()
}

// controlHandler returns the installed handler, or nil.
func (t *Transport) controlHandler() ControlHandler {
	t.controlMu.RLock()
	defer t.controlMu.RUnlock()
	return t.controlFn
}

// SetStateHandler implements transport.StateReporter.
func (t *Transport) SetStateHandler(fn func(transport.StateEvent)) {
	t.stateMu.Lock()
	t.stateFn = fn
	t.stateMu.Unlock()
}

// stateHandler returns the installed transition sink, or nil.
func (t *Transport) stateHandler() func(transport.StateEvent) {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.stateFn
}

// Options configures New.
type Options struct {
	// RoomID scopes this transport.
	RoomID string
	// PrivateKey is the local Ed25519 identity in its 64-byte form. WebRTC signs
	// its DTLS certificate fingerprint with it so a peer can verify the DTLS
	// fingerprint belongs to the key advertised in the pairing code.
	PrivateKey []byte
	// STUNServers are stun: URLs. Empty selects the built-in defaults.
	STUNServers []string
	// TURN servers for relay fallback. Optional; empty disables relaying.
	TURNServers []TURNServer
	// Logger receives structured diagnostics.
	Logger *slog.Logger
	// MTU bounds a single data frame. 0 selects 1200, the safe default for an
	// arbitrary Internet path.
	MTU int
	// RelayOnly uses only TURN relay candidates.
	RelayOnly bool
	// PortMin/PortMax restrict local UDP ports, for users who port-forward.
	PortMin, PortMax uint16

	// net replaces the network stack; tests use Pion's virtual network to
	// put real NATs between two transports.
	net ptransport.Net
	// keepalive overrides natKeepalive; tests use a short router lifetime.
	keepalive time.Duration
	// checkLimit overrides the per-path check limit; tests use it to show
	// what the old limit did.
	checkLimit uint16
}

// TURNServer describes a relay for peers that cannot be punched.
type TURNServer struct {
	URLs       []string
	Username   string
	Credential string
}

func icePolicy(relayOnly bool) webrtc.ICETransportPolicy {
	if relayOnly {
		return webrtc.ICETransportPolicyRelay
	}
	return webrtc.ICETransportPolicyAll
}

func (o Options) iceServers() []webrtc.ICEServer {
	var out []webrtc.ICEServer
	stun := o.STUNServers
	// nil means "not configured", so the built-in defaults apply. A non-nil but
	// empty slice is an explicit choice to use no STUN at all, which is what
	// the offline tests and the loopback-only deployment need. Collapsing the
	// two cases would make it impossible to turn STUN off, and it would make a
	// test silently reach the public Internet.
	if stun == nil {
		stun = defaultICEServers
	}
	for _, u := range stun {
		out = append(out, webrtc.ICEServer{URLs: []string{u}})
	}
	for _, t := range o.TURNServers {
		out = append(out, webrtc.ICEServer{
			URLs:       t.URLs,
			Username:   t.Username,
			Credential: t.Credential,
		})
	}
	return out
}

// New builds a transport. No sockets are opened until a peer is negotiated.
func New(opts Options) (*Transport, error) {
	if len(opts.PrivateKey) != 64 {
		return nil, protocol.NewErrorf(protocol.CodeConfigInvalid,
			"webrtc: private key is %d bytes, want 64", len(opts.PrivateKey))
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	mtu := opts.MTU
	if mtu <= 0 {
		mtu = 1200
	}
	// The DTLS certificate is Pion's ephemeral self-signed one, not a
	// certificate minted from our Ed25519 identity. Pion's NewCertificate only
	// accepts RSA and ECDSA keys, so binding DTLS to the identity would mean
	// deriving a signing key from the seed - a design that buys little here.
	//
	// What actually authenticates the link is the pairing code. Both session
	// descriptions travel inside a code that is HMAC'd with a secret derived from
	// the room and expires, so a man in the middle cannot substitute its own SDP
	// for either one. The DTLS fingerprint inside that authenticated SDP then
	// pins the key for the session. A future phase that wants the peer's long-term
	// public key on the record should carry a to-be-signed signature of the
	// fingerprint through the control channel, not depend on the certificate.
	//
	// SettingEngine bounds what a peer can make the daemon allocate.
	// SetSCTPMaxMessageSize is enforced by Pion before a message is handed to us,
	// so a hostile peer cannot force a multi-megabyte allocation by sending one
	// large data channel frame.
	se := webrtc.SettingEngine{}
	se.SetSCTPMaxMessageSize(maxControlMessage)
	// A machine already in another LanBaz room has a 10.200.x.x address, and
	// offering it as an ICE candidate would let a link be built *through* an
	// existing tunnel - which works until that room closes and then takes this
	// one down with it. LanBaz's own adapters are never a path to a peer.
	se.SetIPFilter(usableCandidateIP)
	se.SetInterfaceFilter(usableInterface)
	if opts.PortMin > 0 && opts.PortMax >= opts.PortMin {
		if err := se.SetEphemeralUDPPortRange(opts.PortMin, opts.PortMax); err != nil {
			log.Warn("ignoring the configured UDP port range", "error", err)
		}
	}
	// Pion's own failed timeout is disabled (0). It also bounds the *initial*
	// checking phase, and the answerer starts checking the moment its reply
	// code exists - long before a human has carried that reply back to the
	// host. A 16 s budget there killed every real join. The link watchdog
	// (link.watch) decides failure instead: a link that never connected waits
	// as long as an invite can live, and a link that did connect and then
	// stays disconnected fails after reconnectGrace.
	se.SetICETimeouts(4*time.Second, 0, 1*time.Second)
	// Pion marks a path failed after 7 unanswered checks, a few seconds. A
	// guest starts checking the moment its reply exists, so by the time the
	// host applies it, every path had already "failed" and the guest had gone
	// silent - and behind a port-restricted NAT the host's checks are then
	// dropped, because the guest is no longer sending to the host. Two-PC
	// tests showed exactly that: 8 checks per path, then nothing. Keep every
	// path alive instead; the link watchdog bounds the whole attempt.
	limit := uint16(65535)
	if opts.checkLimit > 0 {
		limit = opts.checkLimit
	}
	se.SetICEMaxBindingRequests(limit)
	base := opts.net
	if base != nil {
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	} else {
		base = NewBypassNet()
	}
	keep, err := newKeepNet(base)
	if err != nil {
		return nil, protocol.NewErrorf(protocol.CodeInternal, "webrtc: network: %v", err)
	}
	se.SetNet(keep)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))

	t := &Transport{
		roomID:  opts.RoomID,
		log:     log,
		pending: pendingTimeout,
		grace:   reconnectGrace,
		api:     api,
		mtu:     mtu,
		links:   make(map[transport.PeerID]*link),
		packets: make(chan transport.Packet, 256),
		done:    make(chan struct{}),
		cfg: webrtc.Configuration{
			ICEServers:         opts.iceServers(),
			ICETransportPolicy: icePolicy(opts.RelayOnly),
		},
	}
	every := natKeepalive
	if opts.keepalive > 0 {
		every = opts.keepalive
	}
	var stunURLs []string
	for _, srv := range t.cfg.ICEServers {
		for _, u := range srv.URLs {
			if strings.HasPrefix(u, "stun:") {
				stunURLs = append(stunURLs, u)
			}
		}
	}
	go t.keepMappings(keep, stunURLs, every)
	return t, nil
}

// Name implements transport.Transport.
func (t *Transport) Name() string { return "webrtc" }

// SupportsSignalling implements transport.SignallingCapable.
func (t *Transport) SupportsSignalling() bool { return true }

// Factory constructs a webrtc transport from the generic transport config. It
// lets the daemon register the implementation without importing Pion itself.
type Factory struct{}

// Name implements transport.Factory.
func (Factory) Name() string { return "webrtc" }

// New implements transport.Factory.
func (Factory) New(_ context.Context, cfg transport.Config) (transport.Transport, error) {
	var turn []TURNServer
	if cfg.AllowRelay {
		for _, r := range cfg.TURNServers {
			if strings.TrimSpace(r.URL) == "" {
				continue
			}
			turn = append(turn, TURNServer{
				URLs:       []string{strings.TrimSpace(r.URL)},
				Username:   r.Username,
				Credential: r.Credential,
			})
		}
	}
	return New(Options{
		RoomID:      cfg.RoomID,
		PrivateKey:  cfg.PrivateKey,
		STUNServers: cfg.STUNServers,
		TURNServers: turn,
		MTU:         cfg.MTU,
		Logger:      cfg.Logger,
		RelayOnly:   cfg.RelayOnly && len(turn) > 0,
		PortMin:     cfg.PortMin,
		PortMax:     cfg.PortMax,
	})
}

func (t *Transport) isClosed() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.closed
}

// link returns the link for a peer, or false when the transport never saw it.
// Every per-peer operation funnels through here so that a closed transport and a
// deleted link are reported the same way instead of one panicking on a nil
// dereference.
func (t *Transport) link(peer transport.PeerID) (*link, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return nil, false
	}
	l, ok := t.links[peer]
	return l, ok
}

// Receive implements transport.Transport.
func (t *Transport) Receive() <-chan transport.Packet { return t.packets }

// Connect implements transport.Transport for the server-assisted path, where the
// signalling blobs arrive out of band over the control API rather than inside a
// pairing code.
//
// LanBaz does not require this path: the pairing code already carries the offer
// and the answer. Connect exists because transport.Transport declares it and so
// that a future room server can offer an automatic mode without changing any
// layer above. PeerInfo.Addr carries the peer's signalling blob for this
// transport.
func (t *Transport) Connect(ctx context.Context, peer transport.PeerInfo) error {
	// Drive the same negotiation the pairing path uses, but source the two
	// descriptions from Addr as a pre-negotiated pair. This keeps one code path
	// for the actual WebRTC logic.
	t.mu.Lock()
	_, existing := t.links[peer.ID]
	t.mu.Unlock()
	if existing {
		return t.WaitConnected(ctx, peer)
	}
	return protocol.NewError(protocol.CodeUnsupportedVersion,
		"webrtc: this build negotiates through pairing codes only; "+
			"use room.join and room.accept rather than a server-assisted connect")
}

// forget removes a link from the table if it is still the one registered for
// its peer. A failed link left in the table is a zombie: it reports nothing,
// carries nothing, and makes a later attempt for the same peer reuse a dead
// PeerConnection instead of building a new one.
func (t *Transport) forget(l *link) {
	t.mu.Lock()
	if cur, ok := t.links[l.peer]; ok && cur == l {
		delete(t.links, l.peer)
	}
	t.mu.Unlock()
}

// SetLinkKindHandler installs a sink told whether each link ended up direct or
// relayed once ICE has selected a path.
func (t *Transport) SetLinkKindHandler(fn func(peer transport.PeerID, kind string)) {
	t.stateMu.Lock()
	t.kindFn = fn
	t.stateMu.Unlock()
}

func (t *Transport) linkKindHandler() func(transport.PeerID, string) {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return t.kindFn
}

// Ready implements transport.Transport.
func (t *Transport) Ready(peer transport.PeerID) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	l, ok := t.links[peer]
	if !ok {
		return false
	}
	return l.ready()
}

// Close implements transport.Transport by dropping one peer link.
func (t *Transport) Close(peer transport.PeerID) error {
	t.mu.Lock()
	l, ok := t.links[peer]
	if ok {
		delete(t.links, peer)
	}
	t.mu.Unlock()
	if !ok {
		// Closing an unknown peer is not an error, per the interface contract.
		return nil
	}
	l.close()
	return nil
}

// Stats implements transport.Transport.
func (t *Transport) Stats(peer transport.PeerID) (transport.TransportStats, error) {
	t.mu.RLock()
	l, ok := t.links[peer]
	t.mu.RUnlock()
	if !ok {
		return transport.TransportStats{}, transport.ErrPeerUnknown
	}
	return l.stats(), nil
}

// Shutdown implements transport.Transport.
func (t *Transport) Shutdown(ctx context.Context) error {
	t.shutdownOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		links := make([]*link, 0, len(t.links))
		for id, l := range t.links {
			links = append(links, l)
			delete(t.links, id)
		}
		t.mu.Unlock()

		for _, l := range links {
			l.close()
		}
		// The packets channel is closed under the write lock. Pion does not wait
		// for its data channel read loops when a PeerConnection closes, so a late
		// frame can still arrive; onDataMessage checks closed under the read lock,
		// which is what makes this close safe instead of a send-on-closed panic.
		t.mu.Lock()
		close(t.packets)
		t.mu.Unlock()
		close(t.done)
	})
	return nil
}

// CloseWithError tears the transport down because of a failure. It exists so the
// peer manager can record a reason before the links disappear.
func (t *Transport) CloseWithError(_ context.Context, _ error) error {
	return t.Shutdown(context.Background())
}

// ErrNoLink is returned when an operation names a peer with no link.
var ErrNoLink = transport.ErrPeerUnknown

// classifyConnectionState maps a terminal WebRTC state into the
// machine-readable codes the UI branches on, so a failure shown to a user has a
// stable code rather than a Pion error string.
func classifyConnectionState(state webrtc.PeerConnectionState) (code string, reason string) {
	switch state {
	case webrtc.PeerConnectionStateFailed:
		return protocol.CodeICEFailed, "the peer connection failed; no working path was found"
	case webrtc.PeerConnectionStateDisconnected:
		return protocol.CodeTransportTimeout, "the peer connection dropped"
	case webrtc.PeerConnectionStateClosed:
		return protocol.CodeTransportTimeout, "the peer connection was closed"
	}
	return "", ""
}

// fmtPeer renders a peer id for logs. A peer id is public by design, so this is
// just the id with no transformation beyond readability.
func fmtPeer(p transport.PeerID) string { return string(p) }

var _ transport.Transport = (*Transport)(nil)
var _ transport.Signalling = (*Transport)(nil)
var _ transport.SignallingCapable = (*Transport)(nil)
var _ transport.StateReporter = (*Transport)(nil)
var _ transport.ControlTransport = (*Transport)(nil)
var _ transport.ControlReceiver = (*Transport)(nil)
