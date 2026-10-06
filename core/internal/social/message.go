package social

import (
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

// Message types. Everything a friend can say.
const (
	TypeFriendRequest = "friend_request"
	TypeFriendAccept  = "friend_accept"
	TypeFriendRemove  = "friend_remove"
	TypePresence      = "presence"
	TypeJoinRequest   = "join_request"
	TypeJoinInvite    = "join_invite"
	TypeJoinAnswer    = "join_answer"
	TypeJoinReject    = "join_reject"
)

// Freshness. Gift wraps carry a deliberately fuzzed outer timestamp, so it
// comes from At, inside the encryption. Friend management may wait for a
// friend who is offline; everything about joining is only useful now.
const (
	maxAge       = 10 * time.Minute
	maxAgeFriend = 7 * 24 * time.Hour
)

// lifetime is how long a message type stays useful.
func lifetime(t string) time.Duration {
	switch t {
	case TypeFriendRequest, TypeFriendAccept, TypeFriendRemove:
		return maxAgeFriend
	}
	return maxAge
}

// Message is the encrypted payload ("rumor" content) of every LanBaz event.
type Message struct {
	Type string `json:"t"`
	// ID makes a message unique; replays are dropped by ID.
	ID string `json:"id"`
	// At is the sender's clock in unix seconds.
	At int64 `json:"at"`

	// Sender's profile, on friend messages.
	Name   string `json:"name,omitempty"`
	PeerID string `json:"peer,omitempty"`
	EdKey  string `json:"ed,omitempty"` // LanBaz Ed25519 public key, base64

	// Rooms and joins.
	Room     string `json:"room,omitempty"`
	RoomName string `json:"room_name,omitempty"`
	Code     string `json:"code,omitempty"` // pairing invite or reply code
	Reason   string `json:"reason,omitempty"`

	// Presence.
	Online  bool   `json:"on,omitempty"`
	Hosting bool   `json:"host,omitempty"`
	Seats   int    `json:"seats,omitempty"`
	Game    string `json:"game,omitempty"`
}

// maxCode bounds an embedded pairing code (the long text form is ~2.2 KB).
const maxCode = 8192

// validate is the boundary check for everything that arrives from a relay:
// known type, sane sizes, printable text, fresh timestamp.
func (m Message) validate(now time.Time) error {
	switch m.Type {
	case TypeFriendRequest, TypeFriendAccept, TypeFriendRemove, TypePresence,
		TypeJoinRequest, TypeJoinInvite, TypeJoinAnswer, TypeJoinReject:
	default:
		return errors.New("unknown message type")
	}
	if len(m.ID) < 8 || len(m.ID) > 64 || !idShape(m.ID) {
		return errors.New("bad message id")
	}
	at := time.Unix(m.At, 0)
	if now.Sub(at) > lifetime(m.Type) || at.Sub(now) > 2*time.Minute {
		return errors.New("stale message")
	}
	if !textOK(m.Name, 40) || !textOK(m.RoomName, 60) || !textOK(m.Reason, 120) || !textOK(m.Game, 60) {
		return errors.New("bad text field")
	}
	if !idShape(m.PeerID) || len(m.PeerID) > 64 || !idShape(m.Room) || len(m.Room) > 64 {
		return errors.New("bad identifier")
	}
	if len(m.EdKey) > 64 || len(m.Code) > maxCode || m.Seats < 0 || m.Seats > 64 {
		return errors.New("field too large")
	}
	return nil
}

func idShape(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func textOK(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func decodeMessage(raw string) (Message, error) {
	if len(raw) > maxCode+4096 {
		return Message{}, errors.New("message too large")
	}
	var m Message
	err := json.Unmarshal([]byte(raw), &m)
	return m, err
}
