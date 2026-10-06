package protocol

import "time"

// NetworkStatus is the payload of network.status.
//
// It is deliberately a plain value with no pointers and no nesting beyond one
// level: the UI polls it, and a shape that changes shape depending on which
// backend is in use is a shape the UI has to defend against on every render.
type NetworkStatus struct {
	// RoomID scopes the status. One virtual LAN exists per room, so a client
	// that wants the whole picture asks once per room rather than guessing.
	RoomID string `json:"room_id,omitempty"`
	// State is stopped|starting|ready|degraded|stopping.
	State string `json:"state"`
	// IsHost reports whether this machine owns the room, which decides who
	// relays broadcast and multicast traffic for everybody else.
	IsHost bool `json:"is_host"`
	// Adapter names the backend, e.g. "wintun" or "memory". It is the single
	// most useful field when something is not working, because it says whether
	// the driver is in play at all.
	Adapter string `json:"adapter,omitempty"`
	// Interface is the name Windows shows in its network list.
	Interface string `json:"interface,omitempty"`
	// Subnet is the room's address space, e.g. "10.200.17.0/24".
	Subnet string `json:"subnet,omitempty"`
	// LocalAddress is this machine's address inside it.
	LocalAddress string `json:"local_address,omitempty"`
	// MTU is the adapter's packet size bound.
	MTU int `json:"mtu,omitempty"`
	// Broadcast and Multicast report whether the adapter backend delivers fan-out
	// traffic itself. A Wintun backend reports false because it is a layer 3
	// adapter; LanBaz relays broadcast and multicast in that case, so the LAN
	// still works, but the answer is a true statement about the driver rather
	// than a claim that everything is fine.
	Broadcast bool `json:"broadcast"`
	Multicast bool `json:"multicast"`
	// StartedAt is when the interface came up.
	StartedAt time.Time `json:"started_at,omitempty"`
	// Peers is the address book, local entry first.
	Peers []NetworkPeer `json:"peers"`
	// Routes is the routing table LanBaz installed.
	Routes []RouteEntry `json:"routes"`
	// Metrics are the packet counters.
	Metrics NetworkMetrics `json:"metrics"`
	// Note carries a human readable warning. It is the field that turns "the LAN
	// is not working" into "the virtual network could not be created:
	// WINTUN_PERMISSION_DENIED".
	Note string `json:"note,omitempty"`
}

// NetworkPeer is one address in the room's subnet.
type NetworkPeer struct {
	PeerID      string `json:"peer_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Address     string `json:"address"`
	// Role is self|host|peer.
	Role   string `json:"role"`
	IsHost bool   `json:"is_host"`
	// Local marks this machine's own entry, so the UI can render "you" in the
	// same list as everyone else.
	Local bool `json:"local"`
}

// RouteEntry is one installed route.
type RouteEntry struct {
	Destination string `json:"destination"`
	NextHop     string `json:"next_hop,omitempty"`
	// Peer names the link a host route points at.
	Peer string `json:"peer,omitempty"`
	// Managed marks routes LanBaz created and must remove on shutdown.
	Managed bool   `json:"managed"`
	Note    string `json:"note,omitempty"`
}

// NetworkMetrics are the packet counters.
//
// Every field is a count a user can act on. The two that matter most are
// Dropped, which says the LAN is shedding traffic, and Spoofed, which should
// never be non-zero and means a peer tried to speak as somebody else.
type NetworkMetrics struct {
	Delivered      uint64 `json:"delivered"`
	Forwarded      uint64 `json:"forwarded"`
	Relayed        uint64 `json:"relayed"`
	Dropped        uint64 `json:"dropped"`
	Malformed      uint64 `json:"malformed"`
	Ignored        uint64 `json:"ignored"`
	Oversized      uint64 `json:"oversized"`
	Spoofed        uint64 `json:"spoofed"`
	NoRoute        uint64 `json:"no_route"`
	RateLimited    uint64 `json:"rate_limited"`
	Expired        uint64 `json:"expired"`
	BytesDelivered uint64 `json:"bytes_delivered"`
	BytesForwarded uint64 `json:"bytes_forwarded"`
}

// NetworkEvent is pushed on network.changed.
type NetworkEvent struct {
	RoomID string        `json:"room_id"`
	Status NetworkStatus `json:"status"`
	At     time.Time     `json:"at"`
}

// NetworkInterface is the payload of network.interface: just enough to answer
// "which address do I type into the game, and what is it called".
type NetworkInterface struct {
	RoomID    string    `json:"room_id"`
	Interface string    `json:"interface,omitempty"`
	Adapter   string    `json:"adapter,omitempty"`
	Subnet    string    `json:"subnet,omitempty"`
	Address   string    `json:"address,omitempty"`
	MTU       int       `json:"mtu,omitempty"`
	Broadcast bool      `json:"broadcast"`
	Multicast bool      `json:"multicast"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at,omitempty"`
	IsHost    bool      `json:"is_host"`
	CheckedAt time.Time `json:"checked_at"`
}
