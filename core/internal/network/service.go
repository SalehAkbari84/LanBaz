package network

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// AdapterFactory builds the OS facing interface for one room.
//
// It is a factory rather than a single global adapter because the choice
// depends on the machine: Wintun on a Windows install that has the driver, and
// the in-memory backend everywhere else. Deciding that here rather than in the
// daemon keeps the fallback a property of the network package, which is the
// only place that knows what a working adapter has to do.
type AdapterFactory interface {
	// Name identifies the backend for logs and for network.status.
	Name() string
	// New builds an adapter. It does not create the interface; that is Create.
	New(ctx context.Context, roomID string) (Adapter, error)
}

// FactoryFunc adapts a function to AdapterFactory.
type FactoryFunc struct {
	Backend string
	Fn      func(ctx context.Context, roomID string) (Adapter, error)
}

// Name implements AdapterFactory.
func (f FactoryFunc) Name() string { return f.Backend }

// New implements AdapterFactory.
func (f FactoryFunc) New(ctx context.Context, roomID string) (Adapter, error) {
	return f.Fn(ctx, roomID)
}

// ServiceConfig configures NewService.
type ServiceConfig struct {
	RoomID string
	// Subnet is the room's address space and Local is this machine's address
	// within it. Both must already agree between the participants; see the
	// pairing code.
	Subnet netip.Prefix
	Local  netip.Addr
	// IsHost marks the room owner, the only node that relays.
	IsHost bool
	// Transport carries the packets. The service never inspects it beyond Send,
	// Ready and Receive, so replacing WebRTC with UDP changes nothing here.
	Transport transport.Transport
	Adapter   Adapter
	// Backend names the adapter implementation for the status payload.
	Backend string
	// MTU bounds a single packet; 0 selects DefaultMTU.
	MTU int
	// NoDiscoveryRelay stops broadcast/multicast from crossing the room.
	NoDiscoveryRelay bool
	// BroadcastRate is the per-source fan-out budget, packets/s; 0 = default.
	BroadcastRate float64
	Logger        *slog.Logger
	// Now is the clock; nil selects time.Now.
	Now func() time.Time
	// OnChange is called when the addressing changes, so a UI can be told to
	// refresh without polling.
	OnChange func(Status)
}

// Service is one room's virtual LAN.
//
// It owns the three goroutines that make a tunnel move packets and nothing
// else. There is deliberately no state machine here beyond started/stopped: the
// peer state machine belongs to the peer package, and a second one would be a
// second thing that can disagree with the first about whether a peer exists.
type Service struct {
	roomID  string
	isHost  bool
	backend string
	tr      transport.Transport
	adapter Adapter
	router  *Router
	log     *slog.Logger
	now     func() time.Time
	// backendNote explains a non-Wintun backend in the status payload.
	backendNote string
	// trace reports LAN traffic flows to the developer log.
	trace *tracer

	mu        sync.RWMutex
	state     State
	startedAt time.Time
	note      string
	addr      AddressInfo

	onChange func(Status)

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
	// pumps tracks the three packet goroutines. Stop waits on it before the
	// adapter is destroyed: Wintun frees the session's rings and event handle
	// on close, and a pump still inside a driver call at that moment is a
	// use-after-free in native code that Go cannot recover from.
	pumps sync.WaitGroup
	// oversizedLogged rate-limits the "packet too large" warning.
	oversizedLogged time.Time
	// names answers mDNS/LLMNR lookups for player names; nil disables it.
	names func() map[string]netip.Addr
	// sendQueue carries packets to the adapter. It exists so the goroutine
	// reading from the transport never blocks on an adapter write: a stuck
	// driver must not be able to stop the control channel's packets draining.
	sendQueue chan Packet
}

// outboundDepth bounds the queue in front of the adapter.
//
// A full queue drops the newest packet. That is the right loss for game traffic
// - the game's own protocol is built to lose packets, and holding them would
// only deliver stale state late - but the counter in Status makes it visible, so
// "the LAN is dropping traffic" is a fact the user can see rather than a
// mystery they infer from a laggy game.
const outboundDepth = 512

// NewService builds a per-room virtual network.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "network: a room id is required")
	}
	if cfg.Adapter == nil {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "network: an adapter is required")
	}
	if cfg.Transport == nil {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "network: a transport is required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	router, err := NewRouter(RouterConfig{
		RoomID:           cfg.RoomID,
		Subnet:           cfg.Subnet,
		Local:            cfg.Local,
		IsHost:           cfg.IsHost,
		MTU:              cfg.MTU,
		NoDiscoveryRelay: cfg.NoDiscoveryRelay,
		BroadcastRate:    cfg.BroadcastRate,
		Logger:           cfg.Logger,
		Now:              cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	backend := cfg.Backend
	if backend == "" {
		backend = cfg.Adapter.Name()
	}
	return &Service{
		roomID:    cfg.RoomID,
		isHost:    cfg.IsHost,
		backend:   backend,
		tr:        cfg.Transport,
		adapter:   cfg.Adapter,
		router:    router,
		log:       log.With("room", cfg.RoomID, "network", cfg.Local.String()),
		now:       now,
		state:     StateStopped,
		onChange:  cfg.OnChange,
		sendQueue: make(chan Packet, outboundDepth),
		done:      make(chan struct{}),
		trace:     newTracer(log.With("room", cfg.RoomID), now()),
	}, nil
}

// CheckDiscovery warns once in the log when a game is running but none of its
// LAN discovery has come through LanBaz (see tracer.checkSilence).
func (s *Service) CheckDiscovery(gameRunning bool) { s.trace.checkSilence(s.now(), gameRunning) }

// RoomID implements VirtualNetwork.
func (s *Service) RoomID() string { return s.roomID }

// Router exposes the routing table, for diagnostics and tests.
func (s *Service) Router() *Router { return s.router }

// Adapter exposes the backend, mainly so a test can inject into it.
func (s *Service) Adapter() Adapter { return s.adapter }

// Start brings the adapter up and begins pumping packets.
func (s *Service) Start(ctx context.Context) error {
	var err error
	s.startOnce.Do(func() { err = s.start(ctx) })
	return err
}

func (s *Service) start(ctx context.Context) error {
	s.setState(StateStarting)
	local := s.router.Local()
	prefix := netip.PrefixFrom(local, s.router.Subnet().Bits())
	addr := AddressInfo{
		Interface: s.adapterName(),
		IPv4:      prefix,
		Gateway:   HostAddressOf(s.router.Subnet()),
		MTU:       s.router.MTU(),
		// LanBaz never forwards traffic for anyone else and never installs a
		// default route, so it advertises no DNS: a game that resolved through
		// the tunnel would either fail or, worse, be answered by somebody else.
		DNS: nil,
	}
	if err := s.adapter.Create(ctx, s.adapterName(), s.router.MTU()); err != nil {
		s.setState(StateDegraded)
		s.setNote("the virtual adapter could not be created: " + err.Error())
		return err
	}
	if err := s.adapter.Configure(ctx, addr); err != nil {
		_ = s.adapter.Destroy(context.WithoutCancel(ctx))
		s.setState(StateDegraded)
		s.setNote("the virtual adapter could not be configured: " + err.Error())
		return err
	}
	applied, err := s.adapter.Address()
	if err != nil {
		applied = addr
	}
	s.mu.Lock()
	s.addr = applied
	s.startedAt = s.now()
	s.state = StateReady
	s.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.pumps.Add(3)
	go s.readAdapter(runCtx)
	go s.readTransport(runCtx)
	go s.writeAdapter(runCtx)

	s.log.Info("virtual network started",
		"adapter", applied.Interface, "address", local.String(),
		"subnet", s.router.Subnet().String(), "backend", s.backend, "host", s.isHost)
	s.publish()
	return nil
}

// adapterName is the interface name Windows will show.
//
// It carries the room's short id so two rooms on one machine get two visibly
// distinct adapters. A user with three rooms open can then tell which is which
// in the Windows network list without opening LanBaz.
func (s *Service) adapterName() string {
	id := s.roomID
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	return "LanBaz-" + id
}

// readAdapter drains packets a local game produced and forwards them.
func (s *Service) readAdapter(ctx context.Context) {
	defer s.wgDone()
	for {
		pkt, err := s.adapter.ReadPacket(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) && ctx.Err() == nil {
				// A dead reader means no game traffic leaves this machine, so
				// it must not be invisible: say so in the status payload.
				s.log.Warn("adapter read stopped; outgoing game traffic is not being carried", "error", err)
				s.setState(StateDegraded)
				s.setNote("the virtual adapter stopped delivering packets: " + err.Error())
				s.publish()
			}
			return
		}
		s.handleOutbound(ctx, pkt)
	}
}

// SetNameSource enables answering name lookups for the room's players.
func (s *Service) SetNameSource(fn func() map[string]netip.Addr) {
	s.mu.Lock()
	s.names = fn
	s.mu.Unlock()
}

// isNameQuery is a cheap pre-check before any parsing or map building.
func isNameQuery(b []byte) bool {
	if len(b) < 20 || b[9] != ProtocolUDP {
		return false
	}
	return b[16] == 224 && b[17] == 0 && b[18] == 0 && (b[19] == 251 || b[19] == 252)
}

// handleOutbound routes one packet from a local game.
func (s *Service) handleOutbound(ctx context.Context, pkt Packet) {
	if isNameQuery(pkt.Payload) {
		s.mu.RLock()
		names := s.names
		s.mu.RUnlock()
		if names != nil {
			if resp := answerNameQuery(pkt.Payload, names()); resp != nil {
				s.enqueue(ctx, Packet{Payload: resp, ReceivedAt: s.now()})
				return
			}
		}
	}
	d := s.router.Outbound(pkt)
	outcome := "dropped"
	defer func() { s.trace.note(pkt.Payload, "out", outcome, s.router.Subnet(), s.now()) }()
	switch d.Action {
	case ActionForward:
		target := transport.PeerID(d.Target)
		if !s.tr.Ready(target) {
			// A direct (mesh) link to another guest that is not up - still
			// connecting, or failed - is not a reason to drop: the host can
			// still carry the packet, just with one more hop.
			hub, ok := s.router.HubPeer()
			if s.isHost || !ok || hub == target || !s.tr.Ready(hub) {
				// No path at all. Dropping is correct: the game's own protocol
				// retransmits, and buffering would deliver stale traffic.
				return
			}
			target = hub
			outcome = "sent via the host"
		}
		if err := s.tr.Send(target, pkt.Payload); err != nil {
			var tooLarge *transport.PayloadTooLargeError
			if errors.As(err, &tooLarge) {
				s.warnOversized(err)
			} else {
				s.log.Debug("could not forward a packet", "peer", string(target), "error", err)
			}
			return
		}
		s.router.NoteForwarded(len(pkt.Payload))
		if outcome == "dropped" {
			outcome = "sent to the player"
		}
	case ActionRelay:
		if !s.relay(ctx, pkt, d) {
			outcome = "not sent: no player connected yet"
			return
		}
		outcome = "sent to the room"
	case ActionDeliver, ActionDrop:
		// Nothing to do: a packet for our own address was already handled by
		// the local stack, and a drop has been counted.
	}
}

// relay fans a packet out to every peer except one.
//
// The relay is the amplification vector, and the rate limiter in the router is
// what bounds it. The hub never echoes back to the sender, which is both a
// correctness property - the sender's own copy would loop - and the reason a
// hub-and-spoke relay cannot double-count its own traffic.
func (s *Service) relay(ctx context.Context, pkt Packet, d Decision) bool {
	exclude := transport.PeerID(d.Exclude)
	sent := false
	for _, id := range s.router.Peers() {
		if id == exclude {
			continue
		}
		if !s.tr.Ready(id) {
			continue
		}
		if err := s.tr.Send(id, pkt.Payload); err != nil {
			s.log.Debug("could not relay a packet", "peer", string(id), "error", err)
			continue
		}
		sent = true
	}
	if sent {
		s.router.NoteRelayed(len(pkt.Payload))
	}
	return sent
}

// readTransport turns packets arriving from peers into adapter writes.
func (s *Service) readTransport(ctx context.Context) {
	defer s.wgDone()
	for {
		select {
		case <-ctx.Done():
			return
		case pkt, ok := <-s.tr.Receive():
			if !ok {
				// The transport is shutting down, so no more packets will ever
				// arrive. Returning is correct; the room is tearing down too.
				return
			}
			s.handleInbound(ctx, pkt)
		}
	}
}

// handleInbound routes one packet that arrived from a peer.
func (s *Service) handleInbound(ctx context.Context, pkt transport.Packet) {
	// The addresses are filled in from the header the transport carried. The
	// transport left them empty on purpose - it only knows which link a packet
	// arrived on - and this is the layer that does know the virtual addresses.
	view := Packet{Peer: string(pkt.Peer), Payload: pkt.Payload, ReceivedAt: pkt.ReceivedAt}

	d := s.router.Inbound(transport.PeerID(pkt.Peer), view)
	out := Packet{
		Peer:       string(pkt.Peer),
		Payload:    pkt.Payload,
		ReceivedAt: pkt.ReceivedAt,
	}

	outcome := "dropped"
	defer func() { s.trace.note(pkt.Payload, "in", outcome, s.router.Subnet(), s.now()) }()
	switch d.Action {
	case ActionDeliver:
		if !s.enqueue(ctx, out) {
			outcome = "dropped: adapter queue full"
			return
		}
		outcome = "given to the games on this PC"
		s.router.NoteDelivered(len(pkt.Payload))
	case ActionForward:
		// Every LanBaz hop costs one, and the packet arrived from a remote host
		// rather than from the local stack, so this is the first decrement.
		relayed := Packet{Peer: string(pkt.Peer), Payload: clone(pkt.Payload)}
		if !s.router.DecrementTTL(&relayed) {
			return
		}
		if !s.tr.Ready(transport.PeerID(d.Target)) {
			return
		}
		if err := s.tr.Send(transport.PeerID(d.Target), relayed.Payload); err != nil {
			s.log.Debug("could not forward an inbound packet",
				"peer", string(d.Target), "error", err)
			return
		}
		s.router.NoteForwarded(len(relayed.Payload))
	case ActionRelay:
		// The host fans a broadcast out, and keeps a copy for its own games.
		// The local copy is the one that makes a game running on the host see the
		// room's discovery traffic; the fan-out is what carries it to the guests.
		outcome = "passed on to the room"
		if d.DeliverLocal {
			local := Packet{Peer: string(pkt.Peer), Payload: pkt.Payload, ReceivedAt: pkt.ReceivedAt}
			if s.enqueue(ctx, local) {
				s.router.NoteDelivered(len(local.Payload))
				outcome = "given to the games on this PC and passed on to the room"
			}
		}
		// Broadcast and local-scope multicast are relayed with their TTL
		// untouched. Discovery is sent with TTL 1 almost universally - Java's
		// MulticastSocket, for one, defaults to it - because on a real LAN it
		// never crosses a router. The hub is the LAN's switch, not a router, so
		// decrementing here would silently stop every guest from seeing another
		// guest's "Open to LAN". Loops are not possible: the hub never sends a
		// relayed copy back to its sender, and guests never relay.
		relayed := Packet{Peer: string(pkt.Peer), Payload: pkt.Payload}
		s.relayPacket(ctx, relayed, d)
	case ActionDrop:
	}
}

// relayPacket fans an already-decremented packet out to every peer but one.
func (s *Service) relayPacket(ctx context.Context, pkt Packet, d Decision) {
	exclude := transport.PeerID(d.Exclude)
	sent := false
	for _, id := range s.router.Peers() {
		if id == exclude {
			continue
		}
		if !s.tr.Ready(id) {
			continue
		}
		if err := s.tr.Send(id, pkt.Payload); err != nil {
			s.log.Debug("could not relay a packet", "peer", string(id), "error", err)
			continue
		}
		sent = true
	}
	if sent {
		s.router.NoteRelayed(len(pkt.Payload))
	}
}

// enqueue queues a packet for the adapter.
func (s *Service) enqueue(ctx context.Context, pkt Packet) bool {
	select {
	case s.sendQueue <- pkt:
		return true
	case <-ctx.Done():
		return false
	default:
		return false
	}
}

// writeAdapter is the only writer to the adapter.
func (s *Service) writeAdapter(ctx context.Context) {
	defer s.wgDone()
	for {
		select {
		case <-ctx.Done():
			return
		case pkt := <-s.sendQueue:
			if err := s.adapter.WritePacket(ctx, pkt); err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
					s.log.Debug("could not inject a packet", "error", err)
				}
			}
		}
	}
}

// wgDone marks the service's pumps as finished.
//
// It is a placeholder for a WaitGroup; the close of done happens once in Stop.
// Keeping it as a method documents which goroutines belong to the service.
func (s *Service) wgDone() { s.pumps.Done() }

// warnOversized logs, at most every 30 s, that a packet larger than the tunnel
// MTU was dropped. It is a warning because it is the one drop that means a
// misconfigured adapter rather than ordinary loss.
func (s *Service) warnOversized(err error) {
	s.mu.Lock()
	now := s.now()
	due := now.Sub(s.oversizedLogged) > 30*time.Second
	if due {
		s.oversizedLogged = now
	}
	s.mu.Unlock()
	if due {
		s.log.Warn("dropping packets larger than the tunnel MTU; check the adapter MTU", "error", err)
	}
}

// AddPeer registers a peer's address.
func (s *Service) AddPeer(id, addr string) error {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return protocol.NewErrorf(protocol.CodeBadRequest,
			"network: %q is not an address", addr)
	}
	if err := s.router.AddPeer(transport.PeerID(id), a); err != nil {
		return err
	}
	// A host route is installed so the operating system, not the router, decides
	// the packet is destined for a peer. The router only sees what the adapter
	// hands it.
	if sa, ok := s.adapter.(RouteInstaller); ok && s.router.IsHost() {
		if err := sa.AddRoute(ctx(), Route{
			Destination: netip.PrefixFrom(a, a.BitLen()),
			NextHop:     a,
			Managed:     true,
			Peer:        id,
		}); err != nil {
			// A route that cannot be installed is not fatal: the subnet route
			// already covers the address, so traffic still arrives and the
			// router still routes it. It is worth a warning and no more.
			s.log.Warn("could not install a peer route; the subnet route will carry it",
				"peer", id, "address", addr, "error", err)
		}
	}
	s.publish()
	return nil
}

// ctx returns a context for a short background operation. Route installation
// outlives the request that triggered it, so it must not inherit that request's
// cancellation.
func ctx() context.Context { return context.Background() }

// RemovePeer forgets a peer and its route.
func (s *Service) RemovePeer(id string) {
	// The address is read before the peer is forgotten: afterwards the router
	// no longer knows it, and the host route would be left behind to collide
	// with the next peer given the same lease.
	addr, found := s.router.AddressOf(transport.PeerID(id))
	s.router.RemovePeer(transport.PeerID(id))
	if sa, ok := s.adapter.(RouteInstaller); ok && found && s.router.IsHost() {
		_ = sa.DeleteRoute(ctx(), netip.PrefixFrom(addr, addr.BitLen()))
	}
	s.publish()
}

// Routes returns the routing table.
func (s *Service) Routes(_ context.Context) ([]Route, error) {
	s.mu.RLock()
	state := s.state
	s.mu.RUnlock()
	if state == StateStopped {
		return nil, ErrNotStarted
	}
	return s.router.Routes(), nil
}

// Metrics returns a snapshot of the packet counters.
func (s *Service) Metrics() Snapshot { return s.router.Metrics().Snapshot() }

// Status renders the network.status payload.
func (s *Service) Status(_ context.Context) (Status, error) {
	s.mu.RLock()
	state, addr, started, note, backendNote := s.state, s.addr, s.startedAt, s.note, s.backendNote
	s.mu.RUnlock()

	if state == StateStopped {
		return Status{
			State:   state,
			RoomID:  s.roomID,
			Adapter: s.backend,
			Address: addr,
			Routes:  []Route{},
			Peers:   []PeerStatus{},
			IsHost:  s.isHost,
		}, nil
	}
	out := Status{
		State:     state,
		RoomID:    s.roomID,
		Adapter:   s.backend,
		Address:   addr,
		Routes:    s.router.Routes(),
		StartedAt: started,
		Metrics:   s.router.Metrics().Snapshot(),
		Peers:     s.peerStatus(),
		IsHost:    s.isHost,
		Note:      joinNotes(note, backendNote, s.adapterNote()),
	}
	return out, nil
}

// peerStatus renders the address book, local entry first.
func (s *Service) peerStatus() []PeerStatus {
	out := make([]PeerStatus, 0, s.router.PeerCount()+1)
	out = append(out, PeerStatus{
		PeerID:  "",
		Address: s.router.Local(),
		Role:    "self",
		Local:   true,
		IsHost:  s.isHost,
	})
	for _, id := range s.router.Peers() {
		addr, _ := s.router.AddressOf(id)
		role := "peer"
		if addr == HostAddressOf(s.router.Subnet()) {
			role = "host"
		}
		out = append(out, PeerStatus{PeerID: string(id), Address: addr, Role: role})
	}
	return out
}

// Stop tears the adapter down and stops the pumps.
func (s *Service) Stop(ctx context.Context) error {
	var err error
	s.stopOnce.Do(func() {
		s.setState(StateStopping)
		if s.cancel != nil {
			s.cancel()
		}
		// Wait for the pumps before the adapter goes away; see pumps. The read
		// loop wakes at least every 250 ms, so this is bounded, but a context
		// deadline still caps it in case a driver call hangs.
		waited := make(chan struct{})
		go func() { s.pumps.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-ctx.Done():
			s.log.Warn("network pumps did not stop before the deadline")
		case <-time.After(3 * time.Second):
			s.log.Warn("network pumps did not stop within 3s")
		}
		close(s.done)
		s.mu.Lock()
		wasUp := s.state != StateStopped
		s.state = StateStopped
		s.mu.Unlock()
		if !wasUp {
			return
		}
		derr := s.adapter.Destroy(ctx)
		if derr != nil && !errors.Is(derr, ErrNotStarted) && !errors.Is(derr, ErrClosed) {
			err = derr
		}
		s.publish()
	})
	return err
}

// Done is closed once the pumps have stopped.
func (s *Service) Done() <-chan struct{} { return s.done }

// RouteInstaller is implemented by adapters that can install host routes
// themselves. The in-memory backend does not need to, and a backend that
// cannot is not a failure - the room's on-link subnet route already covers
// every address in it.
type RouteInstaller interface {
	AddRoute(ctx context.Context, r Route) error
	DeleteRoute(ctx context.Context, destination netip.Prefix) error
}

// setState records the lifecycle state and logs the change.
func (s *Service) setState(state State) {
	s.mu.Lock()
	prev := s.state
	s.state = state
	s.mu.Unlock()
	if prev != state {
		s.log.Debug("network state", "from", string(prev), "to", string(state))
	}
}

// setNote records a human readable warning.
func (s *Service) setNote(note string) {
	s.mu.Lock()
	s.note = note
	s.mu.Unlock()
}

// publish hands the current status to the change sink.
func (s *Service) publish() {
	if s.onChange == nil {
		return
	}
	st, err := s.Status(context.Background())
	if err != nil {
		return
	}
	s.onChange(st)
}

// SetBackendNote records why a backend is limited, e.g. that Wintun does not
// deliver fan-out and LanBaz relays it itself.
func (s *Service) SetBackendNote(note string) {
	s.mu.Lock()
	s.backendNote = note
	s.mu.Unlock()
	s.publish()
}

// adapterNote returns the backend's own explanation of a partial setup, such as
// a firewall rule it could not add.
func (s *Service) adapterNote() string {
	if n, ok := s.adapter.(interface{ Note() string }); ok {
		return n.Note()
	}
	return ""
}

// joinNotes renders the non-empty notes.
func joinNotes(notes ...string) string {
	out := ""
	for _, n := range notes {
		if n == "" {
			continue
		}
		if out != "" {
			out += "; "
		}
		out += n
	}
	return out
}

// clone copies a payload so a header rewrite cannot mutate the caller's bytes.
func clone(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

var _ VirtualNetwork = (*Service)(nil)
