package room

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Room-level messages: chat now, presence and mesh signalling later.
//
// # Delivery
//
// The room is hub and spoke: guests hold one link, to the host. A message is
// sent by its origin to every link it has; the host relays anything it
// receives to every other peer. Each node remembers message ids for a while,
// so a message that reaches it twice (once directly, once relayed, when mesh
// links exist) is handled once.
//
// # Trust
//
// A message arriving directly from its origin must carry that link's announced
// identity as Origin, so a guest cannot speak as somebody else. Messages a
// guest receives from the host may have any origin: the host has already done
// that check before relaying.

const (
	appKindChat = "chat"

	// seenTTL is how long message ids are remembered for deduplication.
	seenTTL = 2 * time.Minute
	// seenMax caps the dedup memory.
	seenMax = 4096
	// chatHistory is how many messages a room keeps for the UI.
	chatHistory = 200
	// chatRate/chatBurst bound chat per origin: a flood from one guest must not
	// become a flood for everybody through the host's relay.
	chatRate  = 2.0
	chatBurst = 8.0
)

type appState struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	chat    []protocol.ChatMessage
	buckets map[string]*tokenBucket
	// onChat is the UI sink.
	onChat func(protocol.ChatMessage)
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newAppState() *appState {
	return &appState{seen: map[string]time.Time{}, buckets: map[string]*tokenBucket{}}
}

// firstSight records id and reports whether it is new.
func (a *appState) firstSight(id string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, dup := a.seen[id]; dup {
		return false
	}
	if len(a.seen) >= seenMax {
		cutoff := now.Add(-seenTTL)
		for k, t := range a.seen {
			if t.Before(cutoff) {
				delete(a.seen, k)
			}
		}
		if len(a.seen) >= seenMax {
			// Still full: forget everything rather than grow without bound. A
			// few duplicates are the worst outcome.
			a.seen = map[string]time.Time{}
		}
	}
	a.seen[id] = now
	return true
}

func (a *appState) allow(origin string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.buckets[origin]
	if !ok {
		a.buckets[origin] = &tokenBucket{tokens: chatBurst - 1, last: now}
		return true
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens += el * chatRate
		if b.tokens > chatBurst {
			b.tokens = chatBurst
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func newMessageID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// installAppHandler wires the peer manager's app frames to this room.
func (r *Room) installAppHandler() {
	r.pm.SetAppHandler(r.handleApp)
}

// SetChatHandler installs the UI sink for chat messages.
func (r *Room) SetChatHandler(fn func(protocol.ChatMessage)) {
	r.app.mu.Lock()
	r.app.onChat = fn
	r.app.mu.Unlock()
}

// handleApp is the entry point for every inbound room-level message.
func (r *Room) handleApp(from *peer.Peer, msg peer.AppMessage) {
	now := r.now()
	if !from.IsHost() && msg.Origin != string(from.Announced()) {
		// A guest may only speak for itself. (The host is a guest's only
		// link and relays for everybody, so its frames are exempt.)
		r.log.Warn("dropping a room message with a forged origin",
			"peer", string(from.ID()), "claimed", msg.Origin)
		return
	}
	if !r.app.firstSight(msg.ID, now) {
		return
	}
	if msg.Origin == string(r.pm.LocalID()) {
		return // our own message coming back around
	}

	// The hub relays before acting, so a slow handler cannot delay delivery.
	if r.isHost {
		if msg.To == "" {
			r.pm.BroadcastApp(msg, from.ID())
		} else if msg.To != string(r.pm.LocalID()) {
			if p, err := r.pm.Lookup(msg.To); err == nil {
				_ = r.pm.SendApp(p.ID(), msg)
			}
			return
		}
	}
	if msg.To != "" && msg.To != string(r.pm.LocalID()) {
		return
	}

	switch msg.Kind {
	case appKindChat:
		r.receiveChat(msg, now)
	default:
		r.dispatchApp(from, msg)
	}
}

// dispatchApp is the hook for kinds defined in other files (presence, mesh).
func (r *Room) dispatchApp(from *peer.Peer, msg peer.AppMessage) {
	r.appHandlersMu.RLock()
	fn := r.appHandlers[msg.Kind]
	r.appHandlersMu.RUnlock()
	if fn != nil {
		fn(from, msg)
	}
}

// onAppKind registers a handler for one message kind.
func (r *Room) onAppKind(kind string, fn func(from *peer.Peer, msg peer.AppMessage)) {
	r.appHandlersMu.Lock()
	if r.appHandlers == nil {
		r.appHandlers = map[string]func(*peer.Peer, peer.AppMessage){}
	}
	r.appHandlers[kind] = fn
	r.appHandlersMu.Unlock()
}

// broadcastApp sends a message this node originates to every link.
func (r *Room) broadcastApp(kind string, body any, to string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return protocol.NewErrorf(protocol.CodeInternal, "room: encode %s: %v", kind, err)
	}
	msg := peer.AppMessage{
		Kind:   kind,
		ID:     newMessageID(),
		Origin: string(r.pm.LocalID()),
		To:     to,
		Body:   raw,
	}
	r.app.firstSight(msg.ID, r.now())
	if to != "" {
		// Directly if we have a link to the target, else through the host.
		if p, err := r.pm.Lookup(to); err == nil {
			return r.pm.SendApp(p.ID(), msg)
		}
	}
	r.pm.BroadcastApp(msg)
	return nil
}

// --------------------------------------------------------------------- chat --

type chatBody struct {
	Name string `json:"n"`
	Text string `json:"x"`
	At   int64  `json:"a"`
}

// SendChat posts a message to everyone in the room.
func (r *Room) SendChat(text string) (protocol.ChatMessage, error) {
	text = cleanChat(text)
	if text == "" {
		return protocol.ChatMessage{}, protocol.NewError(protocol.CodeBadRequest, "room: empty message")
	}
	now := r.now()
	body := chatBody{Name: r.pm.LocalName(), Text: text, At: now.UnixMilli()}
	raw, _ := json.Marshal(body)
	msg := peer.AppMessage{
		Kind:   appKindChat,
		ID:     newMessageID(),
		Origin: string(r.pm.LocalID()),
		Body:   raw,
	}
	r.app.firstSight(msg.ID, now)
	r.pm.BroadcastApp(msg)
	out := protocol.ChatMessage{
		ID:     msg.ID,
		RoomID: r.id,
		From:   r.pm.LocalID(),
		Name:   body.Name,
		Text:   text,
		At:     now.UTC(),
		Self:   true,
	}
	r.recordChat(out)
	return out, nil
}

func (r *Room) receiveChat(msg peer.AppMessage, now time.Time) {
	if !r.app.allow(msg.Origin, now) {
		return
	}
	var body chatBody
	if err := json.Unmarshal(msg.Body, &body); err != nil {
		return
	}
	text := cleanChat(body.Text)
	if text == "" {
		return
	}
	name := cleanChat(body.Name)
	if p, err := r.pm.Lookup(msg.Origin); err == nil && p.DisplayName() != "" {
		// Prefer the name the peer announced in its signed-session hello.
		name = p.DisplayName()
	}
	if utf8.RuneCountInString(name) > 32 {
		name = string([]rune(name)[:32])
	}
	r.recordChat(protocol.ChatMessage{
		ID:     msg.ID,
		RoomID: r.id,
		From:   protocol.PeerID(msg.Origin),
		Name:   name,
		Text:   text,
		// The receiver's clock, not the sender's: a wrong clock on one PC
		// must not reorder everybody's chat.
		At: now.UTC(),
	})
}

func (r *Room) recordChat(m protocol.ChatMessage) {
	r.app.mu.Lock()
	r.app.chat = append(r.app.chat, m)
	if len(r.app.chat) > chatHistory {
		r.app.chat = append([]protocol.ChatMessage(nil), r.app.chat[len(r.app.chat)-chatHistory:]...)
	}
	fn := r.app.onChat
	r.app.mu.Unlock()
	if fn != nil {
		fn(m)
	}
}

// ChatHistory returns the room's recent messages, oldest first.
func (r *Room) ChatHistory() []protocol.ChatMessage {
	r.app.mu.Lock()
	defer r.app.mu.Unlock()
	return append([]protocol.ChatMessage(nil), r.app.chat...)
}

// cleanChat trims a message and strips control characters.
func cleanChat(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	n := 0
	for _, c := range s {
		if c < 0x20 && c != '\n' || c == 0x7f {
			continue
		}
		b.WriteRune(c)
		n++
		if n >= protocol.MaxChatText {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
