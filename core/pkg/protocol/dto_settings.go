package protocol

// PairingInspectRequest asks what a pasted code is.
type PairingInspectRequest struct {
	Code string `json:"code"`
}

// PairingInfo describes a code without acting on it, so the UI can say "Ali
// invites you to Friday" or "Reply from Sara" and pick the right action.
type PairingInfo struct {
	// Kind is "invite" or "reply".
	Kind      string `json:"kind"`
	RoomID    string `json:"room_id"`
	RoomName  string `json:"room_name,omitempty"`
	HostName  string `json:"host_name,omitempty"`
	GuestName string `json:"guest_name,omitempty"`
	ExpiresAt string `json:"expires_at"`
	// Mode is the room mode, "l3" or "l2".
	Mode string `json:"mode,omitempty"`
	// HostedHere is true when this machine hosts the room (a reply is for us).
	HostedHere bool `json:"hosted_here"`
	// JoinedHere is true when this machine is already a guest in the room.
	JoinedHere bool `json:"joined_here"`
	// Valid is false for an expired or tampered code; Problem says why.
	Valid   bool   `json:"valid"`
	Problem string `json:"problem,omitempty"`
}

// RelayServer is one TURN server a user configured.
type RelayServer struct {
	// URL is a turn: or turns: URL, e.g. "turn:relay.example.com:3478".
	URL        string `json:"url"`
	Username   string `json:"username,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// Settings are the user-editable daemon settings, persisted in the state
// directory and applied to rooms created or joined afterwards.
type Settings struct {
	// DisplayName is what other players see for this machine. Empty uses the
	// computer name.
	DisplayName string `json:"display_name"`
	// STUNServers discover this machine's public address. Empty uses the
	// built-in defaults.
	STUNServers []string `json:"stun_servers"`
	// TURNServers relay traffic when a direct connection is impossible (for
	// example both players behind carrier-grade NAT). Only used when
	// AllowRelay is true.
	TURNServers []RelayServer `json:"turn_servers"`
	// AllowRelay enables the TURN servers above.
	AllowRelay bool `json:"allow_relay"`
	// Network holds the advanced network options. Nil means all defaults.
	Network *NetworkSettings `json:"network,omitempty"`
	// Metered is a free Metered.ca relay account. When set, LanBaz fetches
	// its relay servers itself and uses them as the fallback path.
	Metered *MeteredRelay `json:"metered,omitempty"`
}

// MeteredRelay identifies a Metered.ca TURN application.
type MeteredRelay struct {
	// App is the application name: <app>.metered.live.
	App string `json:"app"`
	// Key is the TURN API key from the Metered dashboard.
	Key string `json:"key"`
}

// Room modes.
const (
	RoomModeL3 = "l3"
	RoomModeL2 = "l2"
)

// NetworkSettings are the advanced network options. Zero numbers mean "use
// the default"; DefaultNetworkSettings has the defaults for the booleans.
type NetworkSettings struct {
	// MTU of the virtual adapter and the tunnel; 0 = 1200.
	MTU int `json:"mtu"`
	// InterfacePriority gives the LanBaz adapter the lowest interface metric so
	// broadcast and multicast discovery leave through it.
	InterfacePriority bool `json:"interface_priority"`
	// RelayDiscovery relays broadcast/multicast between players.
	RelayDiscovery bool `json:"relay_discovery"`
	// BroadcastRate is the per-player fan-out budget, packets/s; 0 = 100.
	BroadcastRate int `json:"broadcast_rate"`
	// NameResolution answers <name>.local for players in the room.
	NameResolution bool `json:"name_resolution"`
	// Mesh connects guests directly to each other.
	Mesh bool `json:"mesh"`
	// RelayOnly forces every connection through the configured TURN relay.
	RelayOnly bool `json:"relay_only"`
	// PortMin/PortMax restrict the local UDP ports ICE uses; 0 = any.
	PortMin int `json:"port_min"`
	PortMax int `json:"port_max"`
	// RoomMode is the default mode for rooms this machine creates: "l3"/"l2".
	RoomMode string `json:"room_mode"`
}

// DefaultNetworkSettings returns the defaults.
func DefaultNetworkSettings() NetworkSettings {
	return NetworkSettings{
		InterfacePriority: true,
		RelayDiscovery:    true,
		NameResolution:    true,
		Mesh:              true,
		RoomMode:          RoomModeL3,
	}
}

// Capabilities says what this engine can do right now.
type Capabilities struct {
	// ClassicLAN is true when the TAP-Windows driver is installed.
	ClassicLAN       bool   `json:"classic_lan"`
	ClassicLANReason string `json:"classic_lan_reason,omitempty"`
}
