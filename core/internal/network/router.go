package network

import (
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Action is what the router decided to do with one packet.
type Action int

const (
	// ActionDrop means the packet has nowhere legitimate to go.
	ActionDrop Action = iota
	// ActionDeliver means the packet is for a local game.
	ActionDeliver
	// ActionForward means the packet goes to exactly one peer.
	ActionForward
	// ActionRelay means the packet goes to every peer except the one it came
	// from.
	ActionRelay
)

// String renders an action for logs and metrics labels.
func (a Action) String() string {
	switch a {
	case ActionDeliver:
		return "deliver"
	case ActionForward:
		return "forward"
	case ActionRelay:
		return "relay"
	default:
		return "drop"
	}
}

// Decision is the router's verdict on one packet.
type Decision struct {
	Action Action
	// DeliverLocal says the packet must also be injected into this machine's own
	// adapter. It is set on top of ActionRelay: the host both keeps the broadcast
	// for its own games and fans it out to the room, and collapsing the two into
	// one action would have meant the service had to know which machine it was on
	// to get the local half right.
	DeliverLocal bool
	// Target is the peer a forwarded packet goes to.
	Target transport.PeerID
	// Exclude is the peer a relayed packet must not go back to. It is the peer
	// the packet arrived on, and setting it is what stops a relayed broadcast
	// from being echoed to its sender.
	Exclude transport.PeerID
	// Src and Dst are the addresses read out of the header.
	Src, Dst netip.Addr
	// Protocol is the IP protocol number, for logs.
	Protocol uint8
	// LimitedBroadcast records that the destination was 255.255.255.255 rather
	// than the room's own broadcast address. The distinction is invisible in a
	// status payload and obvious in a bug report, so it is tracked.
	LimitedBroadcast bool
}

// Metrics are the router's counters. They are the only way a user learns that
// their game is not working because packets are being dropped, and they are
// cheap enough to keep forever.
type Metrics struct {
	// Delivered counts packets handed to the local adapter.
	Delivered atomic.Uint64
	// Forwarded counts packets sent to exactly one peer.
	Forwarded atomic.Uint64
	// Relayed counts packets fanned out to every other peer.
	Relayed atomic.Uint64
	// Dropped counts packets with no legitimate destination.
	Dropped atomic.Uint64
	// Malformed counts packets that were not readable as IPv4 at all. A
	// non-zero value means something is sending garbage, or a transport is
	// delivering framed data where raw packets were expected.
	Malformed atomic.Uint64
	// Oversized counts packets larger than the transport will carry.
	Oversized atomic.Uint64
	// Ignored counts packets that are not IPv4 at all - in practice IPv6
	// neighbour discovery and router solicitations Windows sends on every
	// interface. They are not an error and are kept out of Malformed so that
	// counter keeps meaning "something is sending garbage".
	Ignored atomic.Uint64
	// Spoofed counts packets whose source address did not belong to the peer
	// they arrived on. It should always be zero; a non-zero value is a peer
	// impersonating another, which is exactly what the check exists to catch.
	Spoofed atomic.Uint64
	// NoRoute counts packets addressed to an address no peer holds.
	NoRoute atomic.Uint64
	// RateLimited counts broadcast and multicast packets dropped by the
	// amplification guard.
	RateLimited atomic.Uint64
	// Expired counts packets dropped because their TTL ran out.
	Expired atomic.Uint64
	// BytesDelivered and BytesForwarded are the payload totals, which is what a
	// bandwidth graph reads.
	BytesDelivered atomic.Uint64
	BytesForwarded atomic.Uint64
}

// Snapshot is a plain-value copy of Metrics, safe to serialise.
type Snapshot struct {
	Delivered      uint64 `json:"delivered"`
	Forwarded      uint64 `json:"forwarded"`
	Relayed        uint64 `json:"relayed"`
	Dropped        uint64 `json:"dropped"`
	Malformed      uint64 `json:"malformed"`
	Oversized      uint64 `json:"oversized"`
	Ignored        uint64 `json:"ignored"`
	Spoofed        uint64 `json:"spoofed"`
	NoRoute        uint64 `json:"no_route"`
	RateLimited    uint64 `json:"rate_limited"`
	Expired        uint64 `json:"expired"`
	BytesDelivered uint64 `json:"bytes_delivered"`
	BytesForwarded uint64 `json:"bytes_forwarded"`
}

// Snapshot reads the counters.
func (m *Metrics) Snapshot() Snapshot {
	return Snapshot{
		Delivered:      m.Delivered.Load(),
		Forwarded:      m.Forwarded.Load(),
		Relayed:        m.Relayed.Load(),
		Dropped:        m.Dropped.Load(),
		Malformed:      m.Malformed.Load(),
		Oversized:      m.Oversized.Load(),
		Ignored:        m.Ignored.Load(),
		Spoofed:        m.Spoofed.Load(),
		NoRoute:        m.NoRoute.Load(),
		RateLimited:    m.RateLimited.Load(),
		Expired:        m.Expired.Load(),
		BytesDelivered: m.BytesDelivered.Load(),
		BytesForwarded: m.BytesForwarded.Load(),
	}
}

// Router decides where each packet goes.
//
// # Why this is a pure decision, not a pipeline
//
// Route takes a parsed header and returns a verdict. It performs no I/O, holds
// no goroutine and sends nothing itself. That is what makes the interesting
// cases - a broadcast from a guest, a spoofed source address, a packet with no
// owner - testable without a network, and it is why the Service can change the
// transport implementation without touching a single routing rule.
//
// # The topology
//
// LanBaz is hub and spoke. Every guest has exactly one link, to the host, and
// the host has one link per guest. A guest that wants to reach another guest
// sends the packet to the host, which relays it. That is not an optimisation
// left for later; it is what keeps NAT traversal to one negotiated pair per
// guest and keeps relay logic in one place.
type Router struct {
	roomID string
	subnet netip.Prefix
	// local is this machine's address in the room's subnet.
	local netip.Addr
	// isHost marks the room owner, which is the only peer that relays.
	isHost bool
	log    *slog.Logger

	// peers maps address to peer and peer to address. Both directions are kept
	// because routing needs one and the UI needs the other, and deriving one
	// from the other on every packet would put a lock in the hot path.
	mu     sync.RWMutex
	peers  map[netip.Addr]transport.PeerID
	byPeer map[transport.PeerID]netip.Addr

	metrics Metrics

	// mtu bounds a single packet. A packet above it cannot be carried by the
	// transport, so dropping it is correct: the alternative is fragmenting a
	// packet the remote host will reassemble into something its game never sent.
	mtu int

	// now is injectable so the amplification guard can be tested without
	// sleeping.
	now func() time.Time
	// broadcast is the per-peer rate limiter state.
	broadcast *broadcastLimiter
	// noDiscovery disables broadcast/multicast relay.
	noDiscovery bool
}

// RouterConfig configures a Router.
type RouterConfig struct {
	RoomID string
	// Subnet is the room's address space.
	Subnet netip.Prefix
	// Local is this machine's address inside it.
	Local netip.Addr
	// IsHost marks the room owner.
	IsHost bool
	// MTU bounds a single packet; 0 selects DefaultMTU.
	MTU int
	// NoDiscoveryRelay keeps broadcast and multicast on this machine.
	NoDiscoveryRelay bool
	// BroadcastRate is the per-source fan-out budget; 0 = broadcastRate.
	BroadcastRate float64
	Logger        *slog.Logger
	Now           func() time.Time
}

// DefaultMTU is the packet size the virtual adapter is created with.
//
// It is below 1500 on purpose. A tunnel over an arbitrary Internet path cannot
// promise a full-size frame: the path may carry a PPPoE 1492-byte MTU, a
// 1280-byte IPv6 minimum, or nothing larger at all. An adapter that advertises
// 1500 and drops what does not fit produces a LAN that works between two
// machines on the same Wi-Fi and breaks the moment one of them is on mobile
// data.
//
// 1200 rather than the IPv6 minimum of 1280 because the packet is carried inside
// a WebRTC data channel - UDP, DTLS and SCTP headers on top - and it must match
// the transport's own frame limit exactly. A network MTU above the transport's
// lets the OS build packets the transport then refuses, which is invisible
// loss on every full-size TCP segment.
const DefaultMTU = 1200

// NewRouter builds a router for one room.
func NewRouter(cfg RouterConfig) (*Router, error) {
	if !cfg.Subnet.IsValid() || !cfg.Subnet.Addr().Is4() {
		return nil, protocol.NewError(protocol.CodeConfigInvalid,
			"network: a room needs a valid IPv4 subnet")
	}
	if !cfg.Local.IsValid() || !cfg.Subnet.Contains(cfg.Local) {
		return nil, protocol.NewError(protocol.CodeConfigInvalid,
			"network: the local address must be inside the room subnet")
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
	return &Router{
		noDiscovery: cfg.NoDiscoveryRelay,
		roomID:      cfg.RoomID,
		subnet:      cfg.Subnet.Masked(),
		local:       cfg.Local,
		isHost:      cfg.IsHost,
		log:         log.With("room", cfg.RoomID),
		peers:       make(map[netip.Addr]transport.PeerID),
		byPeer:      make(map[transport.PeerID]netip.Addr),
		mtu:         mtu,
		now:         now,
		broadcast:   newBroadcastLimiter(rate, rate, now),
	}, nil
}

// Subnet returns the room's address space.
func (r *Router) Subnet() netip.Prefix { return r.subnet }

// Local returns this machine's address in the room.
func (r *Router) Local() netip.Addr { return r.local }

// IsHost reports whether this machine owns the room.
func (r *Router) IsHost() bool { return r.isHost }

// MTU returns the packet size bound.
func (r *Router) MTU() int { return r.mtu }

// Metrics returns the live counters.
func (r *Router) Metrics() *Metrics { return &r.metrics }

// AddPeer registers a peer's address.
//
// Registering a peer a second time with a different address replaces the old
// mapping rather than being refused: a peer that reconnects after a daemon
// restart comes back with a new lease, and the old address must not stay in the
// table routing traffic into a hole.
func (r *Router) AddPeer(id transport.PeerID, addr netip.Addr) error {
	if id == "" {
		return protocol.NewError(protocol.CodeConfigInvalid, "network: a peer id is required")
	}
	if !addr.IsValid() || addr == r.local {
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"network: %s is not a usable peer address", addr)
	}
	if !r.subnet.Contains(addr) {
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"network: %s is outside the room subnet %s", addr, r.subnet)
	}
	if SubnetBroadcast(r.subnet) == addr {
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"network: %s is the room broadcast address and cannot be assigned", addr)
	}
	// The subnet's own base address is refused for the same reason the broadcast
	// address is: it is inside the prefix, so a bounds check would accept it, but
	// no host on the subnet can own it. A peer holding it would answer traffic
	// addressed to the network itself.
	if addr == r.subnet.Addr() {
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"network: %s is the room network address and cannot be assigned", addr)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, held := r.peers[addr]; held && previous != id {
		return protocol.NewErrorf(protocol.CodeIPAMExhausted,
			"network: %s is already held by peer %s", addr, previous)
	}
	if old, held := r.byPeer[id]; held && old != addr {
		delete(r.peers, old)
	}
	r.peers[addr] = id
	r.byPeer[id] = addr
	return nil
}

// RemovePeer forgets a peer. Packets addressed to it afterwards are dropped as
// unroutable rather than being delivered to whichever peer took the address.
func (r *Router) RemovePeer(id transport.PeerID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if addr, held := r.byPeer[id]; held {
		delete(r.byPeer, id)
		delete(r.peers, addr)
	}
}

// PeerFor returns the peer holding an address.
func (r *Router) PeerFor(addr netip.Addr) (transport.PeerID, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.peers[addr]
	return id, ok
}

// AddressOf returns a peer's address.
func (r *Router) AddressOf(id transport.PeerID) (netip.Addr, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addr, ok := r.byPeer[id]
	return addr, ok
}

// Peers returns every peer with an address, ordered by peer id.
func (r *Router) Peers() []transport.PeerID {
	r.mu.RLock()
	out := make([]transport.PeerID, 0, len(r.byPeer))
	for id := range r.byPeer {
		out = append(out, id)
	}
	r.mu.RUnlock()
	return out
}

// PeerCount returns how many peers hold an address.
func (r *Router) PeerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byPeer)
}

// Routes returns the routing table for the status payload: one entry per peer,
// plus the subnet itself.
//
// LanBaz never installs a default route. A game that resolves or dials outside
// the room subnet must keep using the physical adapter, because a tunnel that
// claims default routing silently swallows a user's internet traffic.
func (r *Router) Routes() []Route {
	r.mu.RLock()
	out := make([]Route, 0, len(r.peers)+2)
	out = append(out, Route{
		Destination: r.subnet,
		NextHop:     r.local,
		Managed:     true,
		Note:        "room subnet, on-link",
	})
	for addr, id := range r.peers {
		out = append(out, Route{
			Destination: netip.PrefixFrom(addr, addr.BitLen()),
			NextHop:     addr,
			Managed:     true,
			Peer:        string(id),
		})
	}
	r.mu.RUnlock()
	sortRoutes(out)
	return out
}

// Inbound decides what to do with a packet that arrived from a peer.
//
// # What each machine is allowed to check
//
// The source-address rule differs by role, and the difference is not an
// oversight - it is forced by the topology.
//
// The host has a link to every peer and a lease for every address, so it can
// bind a packet's source to the link it arrived on exactly. That is the strong
// check, and it is the one that stops any guest from impersonating any other.
//
// A guest does not. Every packet reaches it over its single link to the host,
// including packets the host relayed on behalf of other guests, so requiring the
// source to match the host's address would reject every guest-to-guest packet in
// the room. A guest has no roster to check against and no link to ask, so all it
// can honestly require is that the source is an address in the room's subnet
// that is not its own. The host has already done the real verification before
// relaying; a guest re-checking it would be theatre.
func (r *Router) Inbound(from transport.PeerID, pkt Packet) Decision {
	h, size, err := r.parse(pkt)
	if err != nil {
		return ActionDropReason(r, ActionDrop, err.Error())
	}
	expected, known := r.AddressOf(from)
	if !known {
		// A packet from a peer the router does not know arrived on a link that
		// should not exist. Dropping is the only safe reading: the alternative
		// is delivering traffic with an unverifiable source.
		r.metrics.NoRoute.Add(1)
		r.metrics.Dropped.Add(1)
		r.log.Warn("dropping a packet from an unaddressed peer", "peer", string(from))
		return Decision{Action: ActionDrop, Src: h.Src, Dst: h.Dst, Protocol: h.Protocol}
	}
	if !r.sourceAcceptable(h.Src, expected) {
		r.metrics.Spoofed.Add(1)
		r.metrics.Dropped.Add(1)
		r.log.Warn("dropping a packet with an unacceptable source address",
			"peer", string(from), "claimed", h.Src.String(), "expected", expected.String())
		return Decision{Action: ActionDrop, Src: h.Src, Dst: h.Dst, Protocol: h.Protocol}
	}

	d := Decision{
		Src:              h.Src,
		Dst:              h.Dst,
		Protocol:         h.Protocol,
		LimitedBroadcast: h.IsLimitedBroadcast(),
	}

	// A packet for this machine, or for the whole subnet, is delivered locally
	// and - where the host is concerned - relayed onwards.
	switch {
	case h.Dst == r.local:
		d.Action = ActionDeliver
	case h.IsBroadcast(r.subnet) || h.IsMulticast():
		d.Action = ActionDeliver
		if r.isHost && !r.noDiscovery {
			// The fan-out is the amplification vector, so the per-source
			// budget is enforced here, where a guest's broadcast is about to be
			// multiplied by the size of the room. Over budget, the host still
			// keeps its own copy; only the fan-out is refused.
			if !r.broadcast.allow(h.Src, r.now()) {
				r.metrics.RateLimited.Add(1)
				return d
			}
			d.Action = ActionRelay
			d.DeliverLocal = true
			d.Exclude = from
		}
	default:
		target, ok := r.PeerFor(h.Dst)
		if !ok {
			// The host is the authority on addressing. An address it does not
			// know belongs to nobody, so the packet is dropped rather than
			// offered to every peer - which is what would happen on a naive
			// fallback, and would turn a stale cache entry into a packet storm.
			r.metrics.NoRoute.Add(1)
			r.metrics.Dropped.Add(1)
			return Decision{Action: ActionDrop, Src: h.Src, Dst: h.Dst, Protocol: h.Protocol}
		}
		if target == from {
			// A peer sent a packet to itself through us. Delivering it is
			// harmless but forwarding it would be a loop.
			d.Action = ActionDeliver
			break
		}
		d.Action = ActionForward
		d.Target = target
	}
	_ = size
	return d
}

// sourceAcceptable reports whether a packet's source address may be believed on
// this machine. See Inbound for why the rule differs between host and guest.
func (r *Router) sourceAcceptable(src, linkAddr netip.Addr) bool {
	if r.isHost {
		// Exact binding: the source must be the address leased to the peer the
		// packet physically arrived on.
		return src == linkAddr
	}
	// A guest can only insist that the source is somebody in the room and is not
	// itself. Anything narrower would reject the host's relayed traffic, and
	// anything looser would let a guest hand the host a packet from an address
	// outside the room entirely.
	return r.subnet.Contains(src) && src != r.local
}

// Outbound decides what to do with a packet a local game produced.
//
// # Why a guest forwards what it cannot resolve
//
// A guest holds exactly one link, to the host, and only ever learns its own
// address and the host's. It has no way to know that 10.200.17.7 belongs to
// somebody, and dropping every address it cannot resolve would make guest to
// guest play impossible - which is the entire point of a LAN.
//
// So on a guest, anything inside the room subnet that is not ours goes to the
// host, which is the only participant that knows the full membership and is the
// only one that gets to decide an address belongs to nobody. The host is the
// authority on addressing; a guest is a pure forwarder towards it.
func (r *Router) Outbound(pkt Packet) Decision {
	h, _, err := r.parse(pkt)
	if err != nil {
		return ActionDropReason(r, ActionDrop, err.Error())
	}
	d := Decision{
		Src:              h.Src,
		Dst:              h.Dst,
		Protocol:         h.Protocol,
		LimitedBroadcast: h.IsLimitedBroadcast(),
	}
	switch {
	case h.Dst == r.local:
		// Our own address. The operating system already delivered this
		// locally; reading it back would be a loop.
		d.Action = ActionDrop
	case h.IsBroadcast(r.subnet) || h.IsMulticast():
		if r.noDiscovery {
			d.Action = ActionDrop
			return d
		}
		if !r.broadcast.allow(h.Src, r.now()) {
			r.metrics.RateLimited.Add(1)
			r.metrics.Dropped.Add(1)
			return d
		}
		if r.isHost {
			d.Action = ActionRelay
		} else {
			// Send it to the host and let the host do the fan-out. Picking the
			// host explicitly rather than "some peer" matters: a guest with two
			// links would otherwise pick one at random and half the room would
			// hear a broadcast the other half did not.
			d.Action = r.viaHub(&d)
		}
	default:
		if target, ok := r.PeerFor(h.Dst); ok {
			d.Action = ActionForward
			d.Target = target
			return d
		}
		if !r.subnet.Contains(h.Dst) {
			// Outside the room subnet there is nowhere to send it. LanBaz is not
			// a gateway, so a packet for the internet stays with the physical
			// adapter rather than being pushed into a tunnel that cannot carry
			// it.
			r.metrics.NoRoute.Add(1)
			r.metrics.Dropped.Add(1)
			d.Action = ActionDrop
			return d
		}
		if !r.isHost {
			d.Action = r.viaHub(&d)
			return d
		}
		// The host knows every peer, so an in-subnet address nobody holds is
		// genuinely unroutable and is dropped rather than fanned out to
		// everybody.
		r.metrics.NoRoute.Add(1)
		r.metrics.Dropped.Add(1)
		d.Action = ActionDrop
	}
	return d
}

// viaHub sets a decision to forward to the room's relay, or to drop when the
// relay is not a known peer.
func (r *Router) viaHub(d *Decision) Action {
	hub, ok := r.hubPeer()
	if !ok {
		d.Action = ActionDrop
		r.metrics.Dropped.Add(1)
		return ActionDrop
	}
	d.Action = ActionForward
	d.Target = hub
	return ActionForward
}

// HubPeer returns the room host's link, from a guest's point of view.
func (r *Router) HubPeer() (transport.PeerID, bool) { return r.hubPeer() }

// hubPeer returns the peer that is the room's relay, which is the host's own
// link from a guest's point of view.
func (r *Router) hubPeer() (transport.PeerID, bool) {
	addr := HostAddressOf(r.subnet)
	id, ok := r.PeerFor(addr)
	return id, ok
}

// parse reads the header and applies the size checks every direction shares.
func (r *Router) parse(pkt Packet) (IPv4, int, error) {
	if len(pkt.Payload) == 0 {
		r.metrics.Malformed.Add(1)
		r.metrics.Dropped.Add(1)
		return IPv4{}, 0, ErrShortPacket
	}
	if len(pkt.Payload) > r.mtu {
		r.metrics.Oversized.Add(1)
		r.metrics.Dropped.Add(1)
		return IPv4{}, 0, &transport.PayloadTooLargeError{Size: len(pkt.Payload), Max: r.mtu}
	}
	if v := pkt.Payload[0] >> 4; v != Version {
		r.metrics.Ignored.Add(1)
		return IPv4{}, 0, ErrNotIPv4
	}
	h, err := ParseIPv4(pkt.Payload)
	if err != nil {
		r.metrics.Malformed.Add(1)
		r.metrics.Dropped.Add(1)
		return IPv4{}, 0, err
	}
	return h, len(pkt.Payload), nil
}

// DecrementTTL applies the hop limit to a packet about to be forwarded and
// reports whether it may still go.
//
// Every relay is a router, so every relay costs a hop. Not decrementing would
// let two guests feeding each other's broadcasts keep them alive indefinitely,
// and would break any game that measures round trips by TTL.
func (r *Router) DecrementTTL(pkt *Packet) bool {
	if decrementTTL(pkt.Payload) {
		return true
	}
	r.metrics.Expired.Add(1)
	r.metrics.Dropped.Add(1)
	return false
}

// ActionDropReason builds a drop decision and counts it.
//
// The counters for the specific reason were already bumped by parse, which also
// counted the drop; this only builds the verdict.
func ActionDropReason(_ *Router, a Action, _ string) Decision {
	return Decision{Action: a}
}

// Note records that a packet left the machine, for the byte counters.
func (r *Router) NoteForwarded(n int) {
	r.metrics.Forwarded.Add(1)
	r.metrics.BytesForwarded.Add(uint64(n))
}

// NoteRelayed records one relayed packet.
func (r *Router) NoteRelayed(n int) {
	r.metrics.Relayed.Add(1)
	r.metrics.BytesForwarded.Add(uint64(n))
}

// NoteDelivered records one locally delivered packet.
func (r *Router) NoteDelivered(n int) {
	r.metrics.Delivered.Add(1)
	r.metrics.BytesDelivered.Add(uint64(n))
}

// sortRoutes orders routes by destination so the status payload is stable.
func sortRoutes(rs []Route) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && routeLess(rs[j], rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func routeLess(a, b Route) bool {
	if a.Destination.Addr() != b.Destination.Addr() {
		return a.Destination.Addr().Less(b.Destination.Addr())
	}
	return a.Destination.Bits() < b.Destination.Bits()
}
