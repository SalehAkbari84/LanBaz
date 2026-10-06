package protocol

import "time"

// Friend system methods.
const (
	MethodFriendsList    = "friends.list"
	MethodFriendsAdd     = "friends.add"
	MethodFriendsRespond = "friends.respond"
	MethodFriendsRemove  = "friends.remove"
	MethodFriendsTrust   = "friends.trust"
	MethodJoinRequest    = "join.request"
	MethodJoinInvite     = "join.invite"
	MethodJoinRespond    = "join.respond"
	// MethodNetworkKeep keeps a room (reopen/rejoin automatically) or stops.
	MethodNetworkKeep = "network.keep"
	// MethodNetworkKept lists the kept networks.
	MethodNetworkKept = "network.kept"
	// MethodLogsTail returns the developer log after a sequence number.
	MethodLogsTail = "logs.tail"
)

// Friend system events.
const (
	// EventFriendUpdate carries a FriendEvent whenever a friend is added,
	// accepted, removed or reports presence.
	EventFriendUpdate = "friend.update"
	// EventJoinPrompt carries a JoinPrompt the user must answer.
	EventJoinPrompt = "join.prompt"
	// EventJoinStatus carries a JoinStatus as a friend join progresses.
	EventJoinStatus = "join.status"
)

// Friend states.
const (
	FriendPendingOut = "pending_out"
	FriendPendingIn  = "pending_in"
	FriendConfirmed  = "friend"
)

// Friend is one entry of the friend list as the UI sees it.
type Friend struct {
	Pub      string    `json:"pub"`
	Code     string    `json:"code"`
	Name     string    `json:"name"`
	PeerID   PeerID    `json:"peer_id,omitempty"`
	State    string    `json:"state"`
	Trusted  bool      `json:"trusted"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen,omitempty"`
	Hosting  bool      `json:"hosting,omitempty"`
	RoomID   string    `json:"room_id,omitempty"`
	RoomName string    `json:"room_name,omitempty"`
	Seats    int       `json:"seats,omitempty"`
	Game     string    `json:"game,omitempty"`
}

// FriendsList is the friends.list result.
type FriendsList struct {
	// Code is this installation's friend code, to give to friends.
	Code    string   `json:"code"`
	Enabled bool     `json:"enabled"`
	Friends []Friend `json:"friends"`
}

// FriendEvent is the payload of friend.update. Why is one of added, request,
// accepted, declined, removed, trusted, presence.
type FriendEvent struct {
	Friend Friend `json:"friend"`
	Why    string `json:"why"`
}

// FriendAddRequest is the friends.add payload.
type FriendAddRequest struct {
	Code string `json:"code"`
}

// FriendKeyRequest names one friend (friends.remove).
type FriendKeyRequest struct {
	Pub string `json:"pub"`
}

// FriendRespondRequest is the friends.respond payload.
type FriendRespondRequest struct {
	Pub    string `json:"pub"`
	Accept bool   `json:"accept"`
}

// FriendTrustRequest is the friends.trust payload.
type FriendTrustRequest struct {
	Pub     string `json:"pub"`
	Trusted bool   `json:"trusted"`
}

// JoinFriendRequest is the payload of join.request (ask to join a friend's
// room) and join.invite (invite a friend into one of our rooms).
type JoinFriendRequest struct {
	Pub    string `json:"pub"`
	RoomID string `json:"room_id,omitempty"`
}

// JoinPrompt asks the user to accept a join request or an invitation.
type JoinPrompt struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"` // "request" or "invite"
	Friend   Friend `json:"friend"`
	RoomID   string `json:"room_id"`
	RoomName string `json:"room_name,omitempty"`
}

// JoinRespondRequest answers a JoinPrompt. Always also marks the friend as
// trusted, so the next request is accepted without asking.
type JoinRespondRequest struct {
	ID     string `json:"id"`
	Accept bool   `json:"accept"`
	Always bool   `json:"always,omitempty"`
}

// JoinStatus reports progress of a friend join: sent, invited, joining,
// connected, rejected, failed, reconnecting.
type JoinStatus struct {
	Friend  Friend `json:"friend"`
	RoomID  string `json:"room_id,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// KeepRequest marks a room as kept (reopened or rejoined automatically) or
// stops keeping it.
type KeepRequest struct {
	RoomID string `json:"room_id"`
	Keep   bool   `json:"keep"`
}

// KeptNetworks is what network.kept returns.
type KeptNetworks struct {
	// Rooms are the open rooms that are kept.
	Rooms []string `json:"rooms"`
	// Hosting is the kept room this PC hosts, if any.
	Hosting *KeptHosting `json:"hosting,omitempty"`
	// Hosts are the friends whose rooms this PC keeps joining.
	Hosts []KeptHost `json:"hosts"`
}

// KeptHosting describes the kept room this PC hosts.
type KeptHosting struct {
	Name    string `json:"name"`
	Mode    string `json:"mode,omitempty"`
	Members int    `json:"members"`
}

// KeptHost is a friend whose room is kept.
type KeptHost struct {
	Pub    string `json:"pub"`
	Name   string `json:"name,omitempty"`
	Online bool   `json:"online"`
	RoomID string `json:"room_id,omitempty"`
}
