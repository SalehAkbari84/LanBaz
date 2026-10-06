// Package network defines the virtual LAN that games see as an ordinary network
// interface.
//
// The MVP is a layer 3 design built on Wintun (Phase 2): a game sends IP
// packets, the packet router decides which peer owns the destination address,
// and a transport carries the bytes. There is no Ethernet bridging, no ARP
// emulation and no MAC learning in the MVP.
//
// Layer 2 caveat (see docs/architecture.md): Wintun is an L3 adapter, so
// broadcast and multicast delivery is not guaranteed by the adapter itself.
// Minecraft LAN discovery depends on it. The Adapter interface below is
// therefore intentionally transport agnostic, so Plan B (a TAP-Windows6 L2
// adapter) can replace the Wintun backend without touching the router, the
// transports, the room or the UI.
//
// # What this package does not know
//
// It does not know what a room is, what a peer is, or which transport carries
// the traffic. It knows that packets have sources and destinations, that some
// destinations are broadcast, and that a packet must not claim an address it
// does not own. Everything else - pairing, liveness, ICE - belongs to the
// layers above.
package network

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// Packet is one IP packet on the virtual LAN.
//
// The payload is a complete IPv4 datagram, header included, and nothing wraps
// it. Wrapping would be tempting - a length prefix would make a stream
// transport work - but the transports are datagram transports, and a wrapper
// that only one side implemented would be a silent interop failure. Raw packets
// also mean the bytes a game sent are the bytes a peer receives, which is what
// makes the tunnel debuggable with ordinary packet tools.
type Packet struct {
	// Peer identifies the link a packet arrived on or is destined for. It is set
	// by the transport and by the router, never by the adapter.
	Peer string
	// Payload is the raw IPv4 datagram. It belongs to whoever holds the Packet
	// and must not be retained after a Send returns.
	Payload []byte
	// IfIndex is the virtual interface the packet arrived on or is destined
	// for. Zero means "the only LanBaz interface".
	IfIndex uint32
	// ReceivedAt is set by the reader.
	ReceivedAt time.Time
}

// Size returns the packet's payload length in bytes.
func (p Packet) Size() int { return len(p.Payload) }

// AddressInfo is the configured addressing of the virtual interface.
type AddressInfo struct {
	Interface string
	IPv4      netip.Prefix
	// Gateway is the room host's address. It is informational: LanBaz never
	// installs a default route, so no traffic outside the room subnet is sent
	// here.
	Gateway netip.Addr
	MTU     int
	DNS     []netip.Addr
	// Broadcast and Multicast report whether the backend can deliver L2
	// fan-out traffic. A Wintun backend reports false; a TAP backend reports
	// true. The router relays fan-out itself either way, so a false here is a
	// performance note rather than a functional one - with one exception,
	// documented on the relay path in router.go.
	Broadcast bool
	Multicast bool
	// LUID is the Windows interface identifier, used to install routes without
	// guessing at an index. Zero on backends that do not have one.
	LUID uint64
}

// Adapter is the OS facing virtual interface. Implementations are the only
// place in the codebase allowed to touch Wintun (or a future TAP driver).
type Adapter interface {
	// Name identifies the backend, e.g. "wintun" or "memory".
	Name() string
	// Create makes the virtual interface with the given name and MTU.
	Create(ctx context.Context, name string, mtu int) error
	// Configure applies the address, routes and DNS to the interface.
	Configure(ctx context.Context, addr AddressInfo) error
	// ReadPacket blocks until one packet is available, the adapter is stopped,
	// or ctx is done.
	ReadPacket(ctx context.Context) (Packet, error)
	// WritePacket injects one packet towards a local process.
	WritePacket(ctx context.Context, pkt Packet) error
	// Address returns the current addressing.
	Address() (AddressInfo, error)
	// Routes returns the routes currently installed by LanBaz.
	Routes() ([]Route, error)
	// Destroy removes the interface and its routes.
	Destroy(ctx context.Context) error
}

// Route is one entry of the routing table LanBaz installs.
type Route struct {
	Destination netip.Prefix
	NextHop     netip.Addr
	IfIndex     uint32
	// Managed marks routes LanBaz created and must remove on shutdown.
	Managed bool
	// Peer names the link a host route points at. It is a status-payload
	// convenience; nothing routes on it.
	Peer string
	// Note is a short human-readable qualifier.
	Note string
}

// VirtualNetwork owns the adapter, the routing table and the packet path
// between local games and remote peers. It knows nothing about WebRTC, UDP or
// relays: it only moves packets.
//
// It is one per room rather than one per machine. Two rooms on one machine are
// two virtual LANs with two subnets and two sets of hosts, and a single shared
// table could not express which peers a packet was allowed to reach.
//
// The interface does not expose the adapter's read and write channels. An
// earlier draft did, on the assumption that a caller would want to drive the
// pump; in practice nothing above this package does, and exporting the channels
// would have meant every implementation had to hand its queues to a stranger
// who could close them.
type VirtualNetwork interface {
	// RoomID is the room this network serves.
	RoomID() string
	// Start brings the adapter up and begins pumping packets.
	Start(ctx context.Context) error
	// Stop shuts the adapter down and removes managed routes.
	Stop(ctx context.Context) error
	// Status reports the current state of the virtual interface.
	Status(ctx context.Context) (Status, error)
	// Routes returns the routing table.
	Routes(ctx context.Context) ([]Route, error)
	// AddPeer registers a peer's address in the room subnet.
	AddPeer(id, addr string) error
	// RemovePeer forgets a peer.
	RemovePeer(id string)
	// Metrics returns a snapshot of the packet counters.
	Metrics() Snapshot
}

// State is the lifecycle state of the virtual network.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateDegraded State = "degraded"
	StateStopping State = "stopping"
)

// Status is the payload of network.status.
type Status struct {
	State     State        `json:"state"`
	RoomID    string       `json:"room_id,omitempty"`
	Adapter   string       `json:"adapter"`
	Address   AddressInfo  `json:"address"`
	Routes    []Route      `json:"routes"`
	StartedAt time.Time    `json:"started_at,omitempty"`
	Metrics   Snapshot     `json:"metrics"`
	Peers     []PeerStatus `json:"peers"`
	// IsHost reports whether this machine owns the room. A guest's status is
	// the mirror of the host's, minus the relay duty.
	IsHost bool `json:"is_host"`
	// Note carries a human readable warning, e.g. when broadcast delivery is
	// unavailable on the current backend.
	Note string `json:"note,omitempty"`
}

// PeerStatus is one peer's address in the room.
type PeerStatus struct {
	PeerID      string     `json:"peer_id"`
	Address     netip.Addr `json:"address"`
	DisplayName string     `json:"display_name,omitempty"`
	// Role is "self", "host" or "peer". It is what lets the UI render this
	// machine's own entry and the room owner differently without re-deriving
	// either from the other fields.
	Role string `json:"role"`
	// IsHost marks the room owner.
	IsHost bool `json:"is_host"`
	// Local marks this machine's own entry.
	Local bool `json:"local"`
	// State is the peer manager's view, when the caller supplied one. The
	// network layer does not own it and does not interpret it.
	State string `json:"state,omitempty"`
}

// HostAddressOf returns the room host's address in a subnet: the first host
// address, which is fixed so a guest can find the host without being told.
//
// It is duplicated in the ipam package, which owns allocation, because the
// router must be able to identify the hub without importing the allocator. The
// two are checked against each other in ipam_test.go so they cannot drift.
func HostAddressOf(subnet netip.Prefix) netip.Addr { return HostAddr(subnet, HostHostOffset) }

// HostHostOffset is the offset of the host's address within a room subnet.
//
// It lives here rather than only in ipam because the router needs it to find
// the hub on a guest, and a router that imported the allocator would invert the
// dependency: allocation is a service of the network, not the other way round.
const HostHostOffset = 1

// Errors shared by every backend.
var (
	// ErrAdapterExists is returned when the virtual interface is already up.
	ErrAdapterExists = errors.New("network: virtual adapter already exists")
	// ErrPermissionDenied is returned when the process lacks the rights to
	// create or configure the virtual adapter (Windows: administrator or the
	// LanBaz service account).
	ErrPermissionDenied = errors.New("network: permission denied")
	// ErrAdapterFailed is returned when the driver could not be loaded.
	ErrAdapterFailed = errors.New("network: failed to create virtual adapter")
	// ErrRouteFailed is returned when a route could not be installed.
	ErrRouteFailed = errors.New("network: failed to install route")
	// ErrNotStarted is returned when the network is used before Start.
	ErrNotStarted = errors.New("network: not started")
	// ErrClosed is returned after Stop.
	ErrClosed = errors.New("network: closed")
)
