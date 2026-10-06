// Package transport defines the pluggable link layer of LanBaz.
//
// The Virtual LAN, the packet router, the room and the peer manager only ever
// see the Transport interface declared here. WebRTC is the first implementation
// (Phase 1); a direct UDP transport and a relay transport are planned. Swapping
// implementations must not require touching Room, Peer, IPAM, Router, the
// VirtualNetwork, the game profiles or the UI API - that is the architectural
// rule this package exists to enforce.
package transport

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// PeerID identifies a remote peer on a transport link.
type PeerID string

// Packet is one unit of traffic on the virtual network. It is opaque to the
// transport: an IP packet in the MVP, a game frame in a later phase.
type Packet struct {
	// Peer identifies the link the packet arrived on. It is always set by the
	// transport and is the only field the router may trust for routing.
	//
	// Peer is deliberately separate from Src: Src and Dst are virtual addresses
	// on the LanBaz subnet, which do not exist until the virtual LAN lands, so
	// in Phase 1 they are empty. Folding the peer id into Src would work for one
	// phase and then silently collide with a virtual address in the next.
	Peer PeerID
	// Src and Dst are the virtual addresses on the LanBaz subnet. Transports
	// leave them empty: assigning one is the router's job, not the link's.
	Src string
	Dst string
	// Payload is the raw packet. Transports must not retain it after Send
	// returns; callers hand over ownership.
	Payload []byte
	// ReceivedAt is set by the transport on receive.
	ReceivedAt time.Time
}

// PeerInfo is everything a transport needs to reach a peer.
type PeerInfo struct {
	ID PeerID
	// Addr is a transport specific address (host:port for UDP, an SDP blob
	// for WebRTC).
	Addr string
	// PublicKey is the peer's identity key fingerprint, used to reject a peer
	// that does not belong to the room.
	PublicKey string
	// RoomID scopes the link to one room.
	RoomID string
}

// TransportStats is the per-peer telemetry surfaced to the UI.
type TransportStats struct {
	// Transport names the implementation, e.g. "webrtc", "udp", "relay".
	Transport string
	// Direct reports whether traffic flows peer-to-peer rather than through a
	// relay.
	Direct bool
	// RTT is the smoothed round trip time.
	RTT time.Duration
	// Jitter is the packet delay variation.
	Jitter time.Duration
	// PacketLoss is a 0..1 ratio.
	PacketLoss    float64
	BytesSent     uint64
	BytesReceived uint64
	// ConnectedAt is when the link reached the ready state.
	ConnectedAt time.Time
}

// Transport is one link to the network. Implementations must be safe for
// concurrent use and must honour context cancellation.
type Transport interface {
	// Name identifies the implementation, e.g. "webrtc".
	Name() string
	// Connect establishes a link to a single peer and blocks until the link is
	// ready, the context is cancelled or an error occurs.
	Connect(ctx context.Context, peer PeerInfo) error
	// Close tears down the link to one peer. Closing an unknown peer is not an
	// error.
	Close(peer PeerID) error
	// Send delivers one packet to a peer.
	Send(peer PeerID, packet []byte) error
	// Receive returns the inbound packet stream. The channel is closed when
	// the transport is closed.
	Receive() <-chan Packet
	// Stats reports telemetry for a peer.
	Stats(peer PeerID) (TransportStats, error)
	// Ready reports whether a peer is usable.
	Ready(peer PeerID) bool
	// Shutdown tears down the whole transport and closes Receive. It is
	// distinct from Close(peer), which drops a single link.
	Shutdown(ctx context.Context) error
}

// Factory constructs transports from configuration. The daemon keeps a
// registry of factories so a room can pick an implementation per room (P2P by
// default, relay when punching fails) without any other layer noticing.
type Factory interface {
	Name() string
	New(ctx context.Context, cfg Config) (Transport, error)
}

// Config is the transport construction parameter.
type Config struct {
	// RoomID is the room this transport serves.
	RoomID string
	// LocalPeerID is the identity presented to remote peers.
	LocalPeerID PeerID
	// PrivateKey is the local identity key material. It is never logged.
	PrivateKey []byte
	// STUNServers are the configured STUN URLs, e.g. stun:host:3478.
	STUNServers []string
	// TURNServers are relays for peers that cannot be punched directly. They are
	// only used when AllowRelay is set: a relay sees (encrypted) game traffic,
	// so it is something a user opts into, never a silent default.
	TURNServers []RelayServer
	// MTU bounds a single packet; 0 selects the implementation default.
	MTU int
	// AllowRelay enables relay fallback for peers that cannot be punched.
	AllowRelay bool
	// RelayOnly forces every link through a TURN relay.
	RelayOnly bool
	// PortMin/PortMax restrict local UDP ports; 0 means any.
	PortMin, PortMax uint16
	// Logger receives the implementation's structured diagnostics. It is part of
	// the construction parameter rather than something an implementation reaches
	// for globally, because a daemon that configured a file sink and a level
	// would otherwise have every transport bypass both and write to stderr.
	// Nil selects slog.Default.
	Logger *slog.Logger
}

// RelayServer is one TURN server and its credentials.
type RelayServer struct {
	URL        string `json:"url"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Errors shared by every implementation. Callers match with errors.Is so that
// a swap of implementation does not change behaviour at the call sites.
var (
	// ErrNotConnected is returned when a peer has no usable link yet.
	ErrNotConnected = errors.New("transport: peer not connected")
	// ErrClosed is returned once the transport has been closed.
	ErrClosed = errors.New("transport: closed")
	// ErrPeerUnknown is returned for a peer the transport never saw.
	ErrPeerUnknown = errors.New("transport: unknown peer")
	// ErrPacketTooLarge is returned when a packet exceeds the transport MTU.
	ErrPacketTooLarge = errors.New("transport: packet exceeds mtu")
	// ErrUnsupported reports a capability the implementation lacks.
	ErrUnsupported = errors.New("transport: unsupported operation")
)
