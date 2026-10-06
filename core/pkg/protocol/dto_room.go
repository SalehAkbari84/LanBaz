package protocol

import "time"

// PeerState is the lifecycle of one peer's link, mirroring the state machine in
// the peer package. It is deliberately explicit so the UI can render progress
// instead of a binary connected/disconnected flag.
type PeerState string

const (
	// PeerNew is a peer that has been accepted but has no transport yet.
	PeerNew PeerState = "new"
	// PeerDiscovering means STUN is gathering candidates.
	PeerDiscovering PeerState = "discovering"
	// PeerSignaling means the offer/answer exchange is in flight.
	PeerSignaling PeerState = "signaling"
	// PeerICEChecking means ICE connectivity checks are running.
	PeerICEChecking PeerState = "ice_checking"
	// PeerConnected means the selected candidate pair is live.
	PeerConnected PeerState = "connected"
	// PeerDTLS means the DTLS handshake finished and the link is authenticated.
	PeerDTLS PeerState = "dtls"
	// PeerDataChannel means at least one data channel is open.
	PeerDataChannel PeerState = "data_channel"
	// PeerNetworkReady means the link can carry virtual LAN traffic. In Phase 1
	// this is equivalent to PeerDataChannel because there is no virtual LAN yet.
	PeerNetworkReady PeerState = "network_ready"
	// PeerActive is a healthy link.
	PeerActive PeerState = "active"
	// PeerDegraded means the link is up but impaired (e.g. relaying).
	PeerDegraded PeerState = "degraded"
	// PeerFailed means the link could not be established.
	PeerFailed PeerState = "failed"
	// PeerDisconnected means a previously healthy link dropped.
	PeerDisconnected PeerState = "disconnected"
)

// Terminal reports whether s is a state from which no further progress is made.
func (s PeerState) Terminal() bool {
	switch s {
	case PeerFailed, PeerDisconnected:
		return true
	default:
		return false
	}
}

// Valid reports whether s is a state this protocol version knows about.
func (s PeerState) Valid() bool {
	switch s {
	case PeerNew, PeerDiscovering, PeerSignaling, PeerICEChecking,
		PeerConnected, PeerDTLS, PeerDataChannel, PeerNetworkReady,
		PeerActive, PeerDegraded, PeerFailed, PeerDisconnected:
		return true
	default:
		return false
	}
}

// ValidLinkKind reports whether k is a transport kind this version knows.
func ValidLinkKind(k string) bool {
	switch k {
	case "direct", "relay", "unknown":
		return true
	default:
		return false
	}
}

// PeerSummary is the UI-facing view of one peer.
//
// It carries the peer's virtual address, which is what a user actually types
// into a game's server list. The address lives here rather than only in
// network.status because the peer list is the screen a user is already looking at
// when they ask "what is their IP".
type PeerSummary struct {
	PeerID      PeerID    `json:"peer_id"`
	DisplayName string    `json:"display_name,omitempty"`
	State       PeerState `json:"state"`
	// TransportPeerID is the link-level id this peer is reached on, which is
	// not PeerID: the identity a peer announces and the id the transport keys
	// its connection by are minted separately. Internal callers that need to
	// correlate a peer with its link - the room, matching a peer to the address
	// it was leased - read this. It never crosses the wire.
	TransportPeerID string `json:"-"`
	// LinkID is a stable per-link key for clients. PeerID is empty until the
	// peer's hello arrives, so two peers still connecting would share a key;
	// LinkID never is. It is opaque and has no meaning beyond identity.
	LinkID string `json:"link_id,omitempty"`
	// VirtualAddress is the peer's address inside the room subnet, e.g.
	// "10.200.17.2". It is empty until the peer holds a lease, which is the
	// normal state for a peer whose link has not finished coming up.
	VirtualAddress string `json:"virtual_address,omitempty"`
	// LinkKind is "direct" when the selected candidate pair is host-to-host,
	// "relay" when it goes through TURN, and "unknown" before ICE completes.
	LinkKind string `json:"link_kind"`
	// PublicKey is the peer's Ed25519 public key, base64url.
	PublicKey string `json:"public_key"`
	// Fingerprint is a short human-comparable form of the public key.
	Fingerprint string  `json:"fingerprint"`
	IsHost      bool    `json:"is_host"`
	IsSelf      bool    `json:"is_self"`
	RTTMillis   float64 `json:"rtt_ms"`
	JitterMs    float64 `json:"jitter_ms"`
	PacketLoss  float64 `json:"packet_loss"`
	BytesSent   uint64  `json:"bytes_sent"`
	BytesRecv   uint64  `json:"bytes_received"`
	// RoundTrips counts completed ping round trips, which is what makes the RTT
	// a measurement rather than an estimate.
	RoundTrips uint64    `json:"round_trips"`
	Since      time.Time `json:"since"`
}

// RoomSummary is the UI-facing view of one room.
type RoomSummary struct {
	RoomID string `json:"room_id"`
	Name   string `json:"name"`
	Owner  PeerID `json:"owner"`
	IsHost bool   `json:"is_host"`
	// Subnet is the room's address space. It is what makes two rooms with the
	// same peer list distinguishable, and it is what a user needs in order to
	// understand why two machines cannot see each other.
	Subnet string `json:"subnet,omitempty"`
	// LocalAddress is this machine's address in that subnet.
	LocalAddress string        `json:"local_address,omitempty"`
	Peers        []PeerSummary `json:"peers"`
	PeerCount    int           `json:"peer_count"`
	MaxPeers     int           `json:"max_peers"`
	CreatedAt    time.Time     `json:"created_at"`
	GameProfile  string        `json:"game_profile,omitempty"`
	// PairingExpiresAt is when the current pairing code stops being accepted.
	// Zero means no code is currently issued.
	PairingExpiresAt time.Time `json:"pairing_expires_at,omitempty"`
	// PairingSpent is true once the issued code has been redeemed. A spent code
	// is refused, which is the replay protection.
	PairingSpent bool `json:"pairing_spent"`
	// Mode is "l3" (standard) or "l2" (classic LAN, Ethernet frames).
	Mode string `json:"mode,omitempty"`
}

// RoomCreateRequest is the payload of room.create.
type RoomCreateRequest struct {
	Name string `json:"name,omitempty"`
	// MaxPeers bounds the room. 0 selects the default of 8, which matches the
	// spirit of Radmin's unlimited rooms while keeping a rogue peer from
	// exhausting the host.
	MaxPeers int `json:"max_peers,omitempty"`
	// PairingTTLSeconds is how long the generated code stays valid. 0 selects
	// the default of 10 minutes.
	PairingTTLSeconds int `json:"pairing_ttl_seconds,omitempty"`
	// GameProfile names a profile under profiles/. Validated in Phase 5.
	GameProfile string `json:"game_profile,omitempty"`
	// Mode is "l3" or "l2"; empty uses the configured default.
	Mode string `json:"mode,omitempty"`
	// Subnet asks for this exact subnet ("10.200.17.0/24"), used by kept
	// networks so addresses survive a restart. Empty derives one.
	Subnet string `json:"subnet,omitempty"`
	// Leases are remembered addresses by player identity.
	Leases map[string]string `json:"leases,omitempty"`
}

// RoomCreateResponse is returned by room.create and contains the one thing the
// user needs next: the pairing code to hand to a guest.
type RoomCreateResponse struct {
	Room RoomSummary `json:"room"`
	// PairingCode is the encoded, signed, expiring code.
	PairingCode string `json:"pairing_code"`
	// PairingURI is the same code as a clickable lanbaz://join/... link.
	PairingURI string    `json:"pairing_uri"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// RoomJoinRequest is the payload of room.join.
type RoomJoinRequest struct {
	// PairingCode is what the guest pasted. The daemon verifies the signature
	// and expiry before using any field in it.
	PairingCode string `json:"pairing_code"`
	DisplayName string `json:"display_name,omitempty"`
}

// RoomJoinResponse is returned by room.join.
//
// In the common serverless case the guest is not yet connected: it has produced
// an answer but has no way to deliver it, because that would require a server.
// The answer is returned to the UI, which shows it for the user to send back to
// the host (a reply pairing code). The host then calls room.accept.
type RoomJoinResponse struct {
	Room RoomSummary `json:"room"`
	// AnswerCode carries the guest's signalling answer back to the host.
	AnswerCode string `json:"answer_code,omitempty"`
	// Pending is true when the link is not up yet and needs room.accept.
	Pending bool `json:"pending"`
	// LocalPeer is the guest's own identity in this room.
	LocalPeer PeerSummary `json:"local_peer"`
}

// RoomAcceptRequest is the payload of room.accept: the host handing the guest's
// answer code back to complete the serverless handshake.
type RoomAcceptRequest struct {
	RoomID     string `json:"room_id"`
	AnswerCode string `json:"answer_code"`
	// GuestPeerID optionally pins which guest the answer belongs to. When empty
	// the answer's embedded guest id is used.
	GuestPeerID PeerID `json:"guest_peer_id,omitempty"`
	// ExpectPeer, when set, is the identity the guest's hello must announce.
	// The friend system sets it to the friend's stored identity, so a code
	// that reached anybody else cannot be used to join in their place.
	ExpectPeer PeerID `json:"expect_peer,omitempty"`
}

// RoomRegeneratePairingRequest is the payload of room.regenerate_pairing.
type RoomRegeneratePairingRequest struct {
	RoomID string `json:"room_id"`
	// TTLSeconds overrides the room's configured pairing lifetime.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
	// ForPeer is the LanBaz identity of the player the code is for, when
	// known (friend invites): the code then carries their fixed address.
	ForPeer string `json:"for_peer,omitempty"`
}

// PairingResponse is returned by room.regenerate_pairing.
type PairingResponse struct {
	PairingCode string    `json:"pairing_code"`
	PairingURI  string    `json:"pairing_uri"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// PeerPingRequest is the payload of peer.ping.
type PeerPingRequest struct {
	PeerID PeerID `json:"peer_id"`
	// Count is how many probes to send. 0 selects the default of 4.
	Count int `json:"count,omitempty"`
	// TimeoutMillis bounds the whole measurement. 0 selects the default.
	TimeoutMillis int `json:"timeout_ms,omitempty"`
}

// PeerPingResult is returned by peer.ping.
type PeerPingResult struct {
	PeerID PeerID `json:"peer_id"`
	// Sent and Received count probes that came back.
	Sent      int     `json:"sent"`
	Received  int     `json:"received"`
	RTTMillis float64 `json:"rtt_ms"`
	JitterMs  float64 `json:"jitter_ms"`
	// Loss is a 0..1 ratio computed from Sent and Received.
	Loss      float64 `json:"loss"`
	MinMillis float64 `json:"min_ms"`
	MaxMillis float64 `json:"max_ms"`
}

// PeerEvent is pushed for peer.state and peer.stats transitions.
type PeerEvent struct {
	PeerID PeerID    `json:"peer_id"`
	RoomID string    `json:"room_id"`
	State  PeerState `json:"state,omitempty"`
	// TransportPeerID is the link-level id, for the same reason as
	// PeerSummary.TransportPeerID: an event about a peer that has left names
	// the identity, and only the link id can be matched to what was torn down.
	TransportPeerID PeerID       `json:"-"`
	Peer            *PeerSummary `json:"peer,omitempty"`
	// Reason explains a terminal transition, e.g. "ice_failed".
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// RoomEvent is pushed for room lifecycle transitions.
type RoomEvent struct {
	RoomID string       `json:"room_id"`
	Name   string       `json:"name,omitempty"`
	Reason string       `json:"reason,omitempty"`
	Room   *RoomSummary `json:"room,omitempty"`
	At     time.Time    `json:"at"`
}
