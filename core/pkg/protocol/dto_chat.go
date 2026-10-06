package protocol

import (
	"encoding/json"
	"time"
)

// MaxChatText bounds one chat message, in runes.
const MaxChatText = 500

// ChatMessage is one message in a room's chat.
type ChatMessage struct {
	ID     string    `json:"id"`
	RoomID string    `json:"room_id"`
	From   PeerID    `json:"from"`
	Name   string    `json:"name"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	// Self is true for messages this machine sent.
	Self bool `json:"self"`
}

// ChatSendRequest posts a message to a room.
type ChatSendRequest struct {
	RoomID string `json:"room_id"`
	Text   string `json:"text"`
}

// Presence is what a player is running, shared with the room.
type Presence struct {
	PeerID   PeerID `json:"peer_id"`
	RoomID   string `json:"room_id,omitempty"`
	Name     string `json:"name,omitempty"`
	GameID   string `json:"game_id,omitempty"`
	GameName string `json:"game_name,omitempty"`
	Exe      string `json:"exe,omitempty"`
	Hosting  bool   `json:"hosting"`
	// Endpoints are "address:port" strings inside the room.
	Endpoints []string  `json:"endpoints,omitempty"`
	JoinURI   string    `json:"join_uri,omitempty"`
	Self      bool      `json:"self,omitempty"`
	At        time.Time `json:"at"`
}

// GameInfo is a supported game, for the library view.
type GameInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Ports     []int  `json:"ports"`
	Discovery string `json:"discovery"`
	JoinHint  string `json:"join_hint,omitempty"`
	NeedsL2   bool   `json:"needs_l2,omitempty"`
	Notes     string `json:"notes,omitempty"`
	// Availability is "free" and/or "drm_free" for games that need no Steam.
	Availability []string `json:"availability,omitempty"`
	Requires     string   `json:"requires,omitempty"`
	MaxLanRTTMs  int      `json:"max_lan_rtt_ms,omitempty"`
}

// VoiceSignal carries WebRTC signalling for voice chat between two players'
// apps. Data is opaque to the daemon.
type VoiceSignal struct {
	RoomID string          `json:"room_id"`
	From   PeerID          `json:"from,omitempty"`
	To     PeerID          `json:"to,omitempty"`
	Data   json.RawMessage `json:"data"`
}

// File transfer methods and events.
const (
	MethodFileOffer   = "file.offer"
	MethodFileRespond = "file.respond"
	MethodFileCancel  = "file.cancel"
	MethodFileList    = "file.list"
	// MethodDiagBundle writes a diagnostics zip and returns {"path": ...}.
	MethodDiagBundle = "diag.bundle"
	// EventFileUpdate carries a FileTransfer whenever one changes.
	EventFileUpdate = "file.update"
)

// FileOfferRequest sends a file on this PC to a player in a room.
type FileOfferRequest struct {
	RoomID string `json:"room_id"`
	PeerID string `json:"peer_id"`
	Path   string `json:"path"`
}

// FileRespondRequest accepts or declines an incoming file (and, for
// file.cancel, names the transfer to stop).
type FileRespondRequest struct {
	ID     string `json:"id"`
	Accept bool   `json:"accept,omitempty"`
}

// FileTransfer is one file going to or coming from a player.
type FileTransfer struct {
	ID       string `json:"id"`
	RoomID   string `json:"room_id"`
	PeerID   PeerID `json:"peer_id"`
	PeerName string `json:"peer_name,omitempty"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Done     int64  `json:"done"`
	// Rate is bytes per second over the last moment of the transfer.
	Rate int64 `json:"rate,omitempty"`
	// Direction is "in" or "out".
	Direction string `json:"direction"`
	// State: offered, incoming, transferring, done, declined, canceled,
	// expired, failed.
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// Path is the source (out) or, once done, where the file was saved (in).
	Path string `json:"path,omitempty"`
}

// Game firewall methods and event.
const (
	MethodGameFirewall    = "game.firewall"
	MethodGameFirewallFix = "game.firewall_fix"
	EventGameFirewall     = "game.firewall"
)

// FirewallRule is one Windows Firewall rule.
type FirewallRule struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
}

// GameFirewall says whether Windows Firewall blocks the running game.
type GameFirewall struct {
	GameID   string         `json:"game_id,omitempty"`
	GameName string         `json:"game_name,omitempty"`
	Path     string         `json:"path,omitempty"`
	Blocked  []FirewallRule `json:"blocked,omitempty"`
}

// MethodGameApply sets up everything LanBaz can for one game.
const MethodGameApply = "game.apply"

// GameApplyRequest names the game and, when LanBaz could not find it, the
// path of its program.
type GameApplyRequest struct {
	GameID string `json:"game_id"`
	Path   string `json:"path,omitempty"`
}

// GameApplyStep is one thing Apply did: "firewall", "network" or "gfwl".
type GameApplyStep struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// GameApplyResult reports every step. NeedsPath asks the UI to let the user
// pick the game's program (one of Executables) and apply again.
type GameApplyResult struct {
	GameID      string          `json:"game_id"`
	GameName    string          `json:"game_name"`
	Steps       []GameApplyStep `json:"steps"`
	NeedsPath   bool            `json:"needs_path,omitempty"`
	Executables []string        `json:"executables,omitempty"`
}
