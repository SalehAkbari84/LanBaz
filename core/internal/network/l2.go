package network

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Classic LAN (L2) mode.
//
// Some games - the IPX era, and a few later titles that discover each other
// with raw Ethernet or ARP tricks - only work on a real Ethernet segment. In an
// L2 room the virtual adapter is a TAP device that exchanges whole Ethernet
// frames, and this switch moves them between players the way a hardware switch
// would: it learns which MAC address lives behind which link, forwards unicast
// to that link, and floods broadcast, multicast and unknown destinations.
//
// The topology is the same as L3: the host is the hub and guests may hold
// direct (mesh) links to each other. A guest sends unicast to a learned link
// when it has one and everything else to the host; the host floods to the rest
// of the room. Broadcast fan-out is rate limited per source MAC exactly like
// the L3 router's.

// MAC is an Ethernet address.
type MAC [6]byte

func (m MAC) isGroup() bool { return m[0]&1 == 1 } // broadcast or multicast

// macsPerLink bounds how many source MACs one link may claim. A real player
// has one; a handful covers VMs or emulators behind it. Without a bound a
// hostile peer could fill the table and turn every frame into a flood.
const macsPerLink = 8

// macTTL is how long a learned address is trusted without traffic.
const macTTL = 5 * time.Minute

// MinFrameLen is an Ethernet header.
const MinFrameLen = 14

type macEntry struct {
	link transport.PeerID
	seen time.Time
}

// SwitchConfig configures NewSwitch.
type SwitchConfig struct {
	RoomID    string
	IsHost    bool
	Transport transport.Transport
	Adapter   Adapter
	Backend   string
	// Subnet/Local are configured on the TAP adapter so games' IP stacks work.
	Subnet netip.Prefix
	Local  netip.Addr
	MTU    int
	// BroadcastRate is the per-source flood budget, frames/s; 0 = default.
	BroadcastRate float64
	Logger        *slog.Logger
	Now           func() time.Time
}

// Switch is one room's Ethernet segment.
type Switch struct {
	cfg SwitchConfig
	log *slog.Logger
	now func() time.Time
	mtu int

	mu      sync.RWMutex
	macs    map[MAC]macEntry
	perLink map[transport.PeerID]int
	links   map[transport.PeerID]netip.Addr // peers and their room addresses
	hub     transport.PeerID
	// local are the MACs this machine sends from (its TAP adapter, VMs).
	local map[MAC]time.Time

	limiter *broadcastLimiter
	state   atomic.Value // State
	started time.Time
	note    string

	delivered, forwarded, flooded, dropped, limited atomic.Uint64
	bytesIn, bytesOut                               atomic.Uint64

	cancel context.CancelFunc
	pumps  sync.WaitGroup
	once   sync.Once
	stop   sync.Once
	queue  chan []byte
}

// NewSwitch builds an L2 room network.
func NewSwitch(cfg SwitchConfig) (*Switch, error) {
	if cfg.Adapter == nil || cfg.Transport == nil || cfg.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "network: a switch needs a room, an adapter and a transport")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	mtu := cfg.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	rate := cfg.BroadcastRate
	if rate <= 0 {
		rate = broadcastRate
	}
	s := &Switch{
		cfg:     cfg,
		log:     log.With("room", cfg.RoomID, "mode", "l2"),
		now:     now,
		mtu:     mtu,
		macs:    map[MAC]macEntry{},
		perLink: map[transport.PeerID]int{},
		links:   map[transport.PeerID]netip.Addr{},
		local:   map[MAC]time.Time{},
		limiter: newBroadcastLimiter(rate, rate, now),
		queue:   make(chan []byte, outboundDepth),
	}
	s.state.Store(StateStopped)
	return s, nil
}

// RoomID implements VirtualNetwork.
func (s *Switch) RoomID() string { return s.cfg.RoomID }

// Start implements VirtualNetwork.
func (s *Switch) Start(ctx context.Context) error {
	var err error
	s.once.Do(func() {
		s.state.Store(StateStarting)
		name := "LanBaz-" + shortID(s.cfg.RoomID)
		// The tunnel carries frames, so the IP MTU on the adapter is the frame
		// budget minus the Ethernet header.
		if err = s.cfg.Adapter.Create(ctx, name, s.mtu-MinFrameLen); err != nil {
			s.state.Store(StateDegraded)
			s.note = "the classic LAN adapter could not be created: " + err.Error()
			return
		}
		addr := AddressInfo{Interface: name, IPv4: netip.PrefixFrom(s.cfg.Local, s.cfg.Subnet.Bits()), MTU: s.mtu - MinFrameLen, Broadcast: true, Multicast: true}
		if err = s.cfg.Adapter.Configure(ctx, addr); err != nil {
			_ = s.cfg.Adapter.Destroy(context.WithoutCancel(ctx))
			s.state.Store(StateDegraded)
			s.note = "the classic LAN adapter could not be configured: " + err.Error()
			return
		}
		run, cancel := context.WithCancel(context.WithoutCancel(ctx))
		s.cancel = cancel
		s.started = s.now()
		s.state.Store(StateReady)
		s.pumps.Add(3)
		go s.readAdapter(run)
		go s.readTransport(run)
		go s.writeAdapter(run)
		s.log.Info("classic LAN switch started", "adapter", name)
	})
	return err
}

// Stop implements VirtualNetwork.
func (s *Switch) Stop(ctx context.Context) error {
	var err error
	s.stop.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		done := make(chan struct{})
		go func() { s.pumps.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		if s.state.Load() != StateStopped {
			err = s.cfg.Adapter.Destroy(ctx)
		}
		s.state.Store(StateStopped)
	})
	return err
}

// AddPeer implements VirtualNetwork. In L2 the address is informational (the
// UI shows it); forwarding is by learned MAC.
func (s *Switch) AddPeer(id, addr string) error {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return protocol.NewErrorf(protocol.CodeBadRequest, "network: %q is not an address", addr)
	}
	s.mu.Lock()
	s.links[transport.PeerID(id)] = a
	if s.cfg.Subnet.IsValid() && a == HostAddressOf(s.cfg.Subnet) {
		s.hub = transport.PeerID(id)
	}
	s.mu.Unlock()
	return nil
}

// RemovePeer implements VirtualNetwork.
func (s *Switch) RemovePeer(id string) {
	link := transport.PeerID(id)
	s.mu.Lock()
	delete(s.links, link)
	for mac, e := range s.macs {
		if e.link == link {
			delete(s.macs, mac)
		}
	}
	delete(s.perLink, link)
	if s.hub == link {
		s.hub = ""
	}
	s.mu.Unlock()
}

// Routes implements VirtualNetwork; an Ethernet segment has one on-link route.
func (s *Switch) Routes(context.Context) ([]Route, error) {
	return []Route{{Destination: s.cfg.Subnet, NextHop: s.cfg.Local, Managed: true, Note: "classic LAN segment"}}, nil
}

// Metrics implements VirtualNetwork.
func (s *Switch) Metrics() Snapshot {
	return Snapshot{
		Delivered:      s.delivered.Load(),
		Forwarded:      s.forwarded.Load(),
		Relayed:        s.flooded.Load(),
		Dropped:        s.dropped.Load(),
		RateLimited:    s.limited.Load(),
		BytesDelivered: s.bytesIn.Load(),
		BytesForwarded: s.bytesOut.Load(),
	}
}

// Status implements VirtualNetwork.
func (s *Switch) Status(context.Context) (Status, error) {
	st, _ := s.state.Load().(State)
	addr, _ := s.cfg.Adapter.Address()
	out := Status{
		State:     st,
		RoomID:    s.cfg.RoomID,
		Adapter:   s.cfg.Backend,
		Address:   addr,
		StartedAt: s.started,
		Metrics:   s.Metrics(),
		IsHost:    s.cfg.IsHost,
		Note:      s.note,
	}
	out.Routes, _ = s.Routes(context.Background())
	out.Peers = []PeerStatus{{Address: s.cfg.Local, Role: "self", Local: true, IsHost: s.cfg.IsHost}}
	s.mu.RLock()
	for id, a := range s.links {
		role := "peer"
		if id == s.hub {
			role = "host"
		}
		out.Peers = append(out.Peers, PeerStatus{PeerID: string(id), Address: a, Role: role})
	}
	s.mu.RUnlock()
	return out, nil
}

// ------------------------------------------------------------------ pumps --

func (s *Switch) readAdapter(ctx context.Context) {
	defer s.pumps.Done()
	for {
		pkt, err := s.cfg.Adapter.ReadPacket(ctx)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, ErrClosed) {
				s.log.Warn("classic LAN adapter read stopped", "error", err)
				s.state.Store(StateDegraded)
			}
			return
		}
		s.outbound(pkt.Payload)
	}
}

func (s *Switch) readTransport(ctx context.Context) {
	defer s.pumps.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case pkt, ok := <-s.cfg.Transport.Receive():
			if !ok {
				return
			}
			s.inbound(pkt.Peer, pkt.Payload)
		}
	}
}

func (s *Switch) writeAdapter(ctx context.Context) {
	defer s.pumps.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-s.queue:
			if err := s.cfg.Adapter.WritePacket(ctx, Packet{Payload: f}); err != nil && ctx.Err() == nil {
				s.dropped.Add(1)
			}
		}
	}
}

// ---------------------------------------------------------------- switching --

func frameMACs(f []byte) (dst, src MAC, ok bool) {
	if len(f) < MinFrameLen {
		return dst, src, false
	}
	copy(dst[:], f[0:6])
	copy(src[:], f[6:12])
	return dst, src, true
}

// outbound handles a frame from the local machine.
func (s *Switch) outbound(f []byte) {
	if len(f) > s.mtu {
		s.dropped.Add(1)
		return
	}
	dst, src, ok := frameMACs(f)
	if !ok {
		s.dropped.Add(1)
		return
	}
	s.mu.Lock()
	s.local[src] = s.now()
	s.mu.Unlock()
	if !dst.isGroup() {
		if link, known := s.lookup(dst); known && s.send(link, f) {
			return
		}
	} else if !s.limiter.allow(macKey(src), s.now()) {
		s.limited.Add(1)
		return
	}
	s.flood(f, "")
}

// inbound handles a frame that arrived from a peer link.
func (s *Switch) inbound(from transport.PeerID, f []byte) {
	if len(f) > s.mtu {
		s.dropped.Add(1)
		return
	}
	dst, src, ok := frameMACs(f)
	if !ok || src.isGroup() {
		s.dropped.Add(1)
		return
	}
	if s.isLocal(src) || !s.learn(src, from) {
		s.dropped.Add(1)
		return
	}
	if s.isLocal(dst) {
		s.deliver(f)
		return
	}
	local := dst.isGroup()
	if !local {
		if link, known := s.lookup(dst); known {
			if s.cfg.IsHost && link != from {
				if s.send(link, f) {
					s.forwarded.Add(1)
				}
				return
			}
		}
		// Unknown unicast, or for us: deliver locally (the OS discards what is
		// not its own), and the host floods unknowns onward.
		local = true
	}
	if local {
		s.deliver(f)
	}
	if s.cfg.IsHost && (dst.isGroup() || !s.known(dst)) {
		if dst.isGroup() && !s.limiter.allow(macKey(src), s.now()) {
			s.limited.Add(1)
			return
		}
		s.flood(f, from)
	}
}

func (s *Switch) deliver(f []byte) {
	c := append([]byte(nil), f...)
	select {
	case s.queue <- c:
		s.delivered.Add(1)
		s.bytesIn.Add(uint64(len(f)))
	default:
		s.dropped.Add(1)
	}
}

// flood sends to the host (guest) or every other link (host).
func (s *Switch) flood(f []byte, except transport.PeerID) {
	if !s.cfg.IsHost {
		s.mu.RLock()
		hub := s.hub
		s.mu.RUnlock()
		if hub != "" && s.send(hub, f) {
			s.flooded.Add(1)
		}
		return
	}
	s.mu.RLock()
	targets := make([]transport.PeerID, 0, len(s.links))
	for id := range s.links {
		if id != except {
			targets = append(targets, id)
		}
	}
	s.mu.RUnlock()
	sent := false
	for _, id := range targets {
		if s.send(id, f) {
			sent = true
		}
	}
	if sent {
		s.flooded.Add(1)
	}
}

func (s *Switch) send(link transport.PeerID, f []byte) bool {
	if !s.cfg.Transport.Ready(link) {
		return false
	}
	if err := s.cfg.Transport.Send(link, f); err != nil {
		return false
	}
	s.bytesOut.Add(uint64(len(f)))
	return true
}

// learn records that src lives behind link. A MAC already owned by another
// live link is refused: on a switch that would be a station moving, but
// between players it is one impersonating another.
func (s *Switch) learn(src MAC, link transport.PeerID) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.macs[src]; ok {
		if e.link == link || now.Sub(e.seen) > macTTL {
			if e.link != link {
				s.perLink[e.link]--
				s.perLink[link]++
			}
			s.macs[src] = macEntry{link: link, seen: now}
			return true
		}
		return false
	}
	if s.perLink[link] >= macsPerLink {
		return false
	}
	s.macs[src] = macEntry{link: link, seen: now}
	s.perLink[link]++
	return true
}

func (s *Switch) isLocal(m MAC) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.local[m]
	return ok && s.now().Sub(t) < macTTL
}

func (s *Switch) lookup(m MAC) (transport.PeerID, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.macs[m]
	if !ok || s.now().Sub(e.seen) > macTTL {
		return "", false
	}
	return e.link, true
}

func (s *Switch) known(m MAC) bool {
	_, ok := s.lookup(m)
	return ok
}

// macKey reuses the address-keyed broadcast limiter for MACs.
func macKey(m MAC) netip.Addr {
	var b [16]byte
	copy(b[10:], m[:])
	return netip.AddrFrom16(b)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

var _ VirtualNetwork = (*Switch)(nil)
