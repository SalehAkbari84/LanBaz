package protocol

import "time"

// Playing during an international internet outage.
const (
	// MethodOutageCheck tells an international outage apart from no internet.
	MethodOutageCheck = "net.outage_check"
	// MethodFriendRelayStatus reports this PC's relay for friends.
	MethodFriendRelayStatus = "relay.friend.status"
	// MethodFriendRelaySet turns it on or off.
	MethodFriendRelaySet = "relay.friend.set"
	// MethodFriendRelayAdd adds a friend's relay from its share code.
	MethodFriendRelayAdd = "relay.friend.add"
)

// OutageStatus is the result of an outage check. Mode is "normal",
// "international" (only domestic hosts answer) or "offline".
type OutageStatus struct {
	Mode       string    `json:"mode"`
	ForeignOK  int       `json:"foreign_ok"`
	DomesticOK int       `json:"domestic_ok"`
	At         time.Time `json:"at"`
}

// FriendRelayStatus is this PC's relay for friends.
type FriendRelayStatus struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
	// Public is the address friends reach it at; Via says how ("public
	// address" or the router's port forwarding method).
	Public string `json:"public,omitempty"`
	Port   int    `json:"port"`
	Via    string `json:"via,omitempty"`
	// Code is the share code friends paste.
	Code      string `json:"code,omitempty"`
	CapMbps   int    `json:"cap_mbps"`
	RelayedMB int64  `json:"relayed_mb"`
	Error     string `json:"error,omitempty"`
}

// FriendRelaySetRequest turns the relay on or off.
type FriendRelaySetRequest struct {
	Enabled bool `json:"enabled"`
	CapMbps int  `json:"cap_mbps,omitempty"`
}

// FriendRelayAddRequest adds a friend's relay.
type FriendRelayAddRequest struct {
	Code string `json:"code"`
}
