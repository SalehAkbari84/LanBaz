package room

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Presence: what each player is running.
//
// Every node detects its own game (internal/games) and announces it to the
// room; others show "Ali is hosting Minecraft" with the address and port to
// join. A node re-announces periodically and whenever a new peer connects, so
// a late joiner learns what everyone is already playing.

const appKindPresence = "presence"

// LocalGame is this machine's detected game, independent of any room.
type LocalGame struct {
	GameID   string
	GameName string
	Exe      string
	Ports    []int
	// JoinURI builds a join link for a given host address and port.
	JoinURI func(host string, port int) string
}

type presenceBody struct {
	GameID   string `json:"g,omitempty"`
	GameName string `json:"n,omitempty"`
	Exe      string `json:"e,omitempty"`
	Hosting  bool   `json:"h,omitempty"`
	Ports    []int  `json:"p,omitempty"`
	JoinURI  string `json:"u,omitempty"`
}

type presenceState struct {
	mu     sync.Mutex
	local  presenceBody
	peers  map[string]presenceBody
	onEcho func(protocol.Presence)
}

func newPresenceState() *presenceState {
	return &presenceState{peers: map[string]presenceBody{}}
}

func (r *Room) installPresence() {
	r.onAppKind(appKindPresence, r.receivePresence)
}

// SetPresenceHandler installs the UI sink for presence changes.
func (r *Room) SetPresenceHandler(fn func(protocol.Presence)) {
	r.pres.mu.Lock()
	r.pres.onEcho = fn
	r.pres.mu.Unlock()
}

// SetLocalGame updates this machine's game and announces it if it changed.
// A nil game means "not playing anything".
func (r *Room) SetLocalGame(g *LocalGame) {
	body := presenceBody{}
	if g != nil {
		body = presenceBody{GameID: g.GameID, GameName: g.GameName, Exe: g.Exe, Hosting: len(g.Ports) > 0, Ports: g.Ports}
		if g.JoinURI != nil && len(g.Ports) > 0 {
			body.JoinURI = g.JoinURI(r.localAddr().String(), g.Ports[0])
		}
	}
	r.pres.mu.Lock()
	changed := !samePresence(r.pres.local, body)
	r.pres.local = body
	fn := r.pres.onEcho
	r.pres.mu.Unlock()
	if !changed {
		return
	}
	_ = r.broadcastApp(appKindPresence, body, "")
	if fn != nil {
		fn(r.presenceFor(r.pm.LocalID(), r.pm.LocalName(), body, true))
	}
}

// announcePresence re-sends this node's presence (periodic, and to newcomers).
func (r *Room) announcePresence(to string) {
	r.pres.mu.Lock()
	body := r.pres.local
	r.pres.mu.Unlock()
	if body.GameID == "" && body.GameName == "" {
		return
	}
	_ = r.broadcastApp(appKindPresence, body, to)
}

func (r *Room) receivePresence(from *peer.Peer, msg peer.AppMessage) {
	var body presenceBody
	if err := json.Unmarshal(msg.Body, &body); err != nil {
		return
	}
	body.GameName = cleanChat(body.GameName)
	body.Exe = cleanChat(body.Exe)
	if len(body.Ports) > 16 {
		body.Ports = body.Ports[:16]
	}
	r.pres.mu.Lock()
	prev, had := r.pres.peers[msg.Origin]
	r.pres.peers[msg.Origin] = body
	fn := r.pres.onEcho
	r.pres.mu.Unlock()
	if had && samePresence(prev, body) {
		return
	}
	if fn != nil {
		fn(r.peerPresence(msg.Origin, body))
	}
}

// peerPresence renders a remote player's presence with what is known now.
func (r *Room) peerPresence(origin string, body presenceBody) protocol.Presence {
	name := ""
	if p, err := r.pm.Lookup(origin); err == nil {
		name = p.DisplayName()
	}
	return r.presenceFor(protocol.PeerID(origin), name, body, false)
}

// refreshPresence re-emits a player's presence once its address is known, so
// the UI gets the endpoints without waiting for the next announcement.
func (r *Room) refreshPresence(origin string) {
	r.pres.mu.Lock()
	body, ok := r.pres.peers[origin]
	fn := r.pres.onEcho
	r.pres.mu.Unlock()
	if ok && fn != nil {
		fn(r.peerPresence(origin, body))
	}
}

// presenceFor renders a presence for the UI. Endpoints use the player's room
// address, which this node knows from the lease table or, for the host, by
// computation.
func (r *Room) presenceFor(id protocol.PeerID, name string, b presenceBody, self bool) protocol.Presence {
	out := protocol.Presence{
		PeerID:   id,
		RoomID:   r.id,
		Name:     name,
		GameID:   b.GameID,
		GameName: b.GameName,
		Exe:      b.Exe,
		Hosting:  b.Hosting,
		JoinURI:  b.JoinURI,
		Self:     self,
		At:       r.now().UTC(),
	}
	addr := ""
	if self {
		addr = r.localAddr().String()
	} else if p, err := r.pm.Lookup(string(id)); err == nil {
		if a, ok := r.Lease(p.ID()); ok {
			addr = a.String()
		}
	}
	if addr != "" {
		for _, port := range b.Ports {
			if port > 0 && port < 65536 {
				out.Endpoints = append(out.Endpoints, fmt.Sprintf("%s:%d", addr, port))
			}
		}
		// A join link carries the sender's idea of its address; rebuild the
		// host part from what this node knows, so a forged link cannot point
		// a click outside the room.
		if out.JoinURI != "" && !strings.Contains(out.JoinURI, addr) {
			out.JoinURI = ""
		}
	} else {
		out.JoinURI = ""
	}
	return out
}

// Presences returns everything known, this node included.
func (r *Room) Presences() []protocol.Presence {
	r.pres.mu.Lock()
	local := r.pres.local
	bodies := make(map[string]presenceBody, len(r.pres.peers))
	for k, v := range r.pres.peers {
		bodies[k] = v
	}
	r.pres.mu.Unlock()
	out := make([]protocol.Presence, 0, len(bodies)+1)
	for origin, b := range bodies {
		out = append(out, r.peerPresence(origin, b))
	}
	if local.GameID != "" || local.GameName != "" {
		out = append(out, r.presenceFor(r.pm.LocalID(), r.pm.LocalName(), local, true))
	}
	return out
}

func samePresence(a, b presenceBody) bool {
	if a.GameID != b.GameID || a.GameName != b.GameName || a.Hosting != b.Hosting || len(a.Ports) != len(b.Ports) {
		return false
	}
	for i := range a.Ports {
		if a.Ports[i] != b.Ports[i] {
			return false
		}
	}
	return true
}

// presenceTick re-announces this node's game so newcomers and anyone who
// missed a message catch up.
const presenceTick = 15 * time.Second
