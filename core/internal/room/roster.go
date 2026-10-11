package room

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Roster: who else is in the room, as seen from a guest.
//
// A guest's peer manager only holds the links it has - the host, plus any
// direct mesh links - so without help a guest's peer list shows the host and
// nobody else. The host is the only node that knows the full membership; it
// sends the list to every guest whenever it changes and on the maintenance
// tick, and a guest merges the players it has no link to into its summary as
// "via host" rows. Traffic to them already goes through the host (the router
// falls back to it), so those rows are exactly as reachable as they look.

const appKindRoster = "roster"

// LinkHub marks a player a guest reaches through the host.
const LinkHub = "hub"

type rosterEntry struct {
	ID   string             `json:"id"`
	Name string             `json:"n,omitempty"`
	Addr string             `json:"a,omitempty"`
	FP   string             `json:"f,omitempty"`
	Key  string             `json:"k,omitempty"`
	St   protocol.PeerState `json:"s,omitempty"`
}

type rosterBody struct {
	Peers []rosterEntry `json:"p"`
}

type rosterState struct {
	mu    sync.Mutex
	peers []rosterEntry
	// sent is the last list the host sent, so an unchanged list is not
	// re-broadcast on every peer event.
	sent string
}

func (r *Room) installRoster() {
	r.onAppKind(appKindRoster, r.receiveRoster)
}

// rosterNow is the host's view of the room's live members.
func (r *Room) rosterNow() []rosterEntry {
	var out []rosterEntry
	for _, p := range r.pm.List() {
		if p.PeerID == "" || p.IsSelf || !isLive(p.State) {
			continue
		}
		e := rosterEntry{ID: string(p.PeerID), Name: p.DisplayName, FP: p.Fingerprint, Key: p.PublicKey, St: p.State}
		if addr, ok := r.Lease(transport.PeerID(p.TransportPeerID)); ok {
			e.Addr = addr.String()
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sendRoster broadcasts the membership to every guest. force sends even an
// unchanged list (the periodic refresh, for a guest that missed one).
func (r *Room) sendRoster(force bool) {
	if !r.isHost {
		return
	}
	list := r.rosterNow()
	raw, _ := json.Marshal(list)
	r.roster.mu.Lock()
	same := r.roster.sent == string(raw)
	r.roster.sent = string(raw)
	r.roster.mu.Unlock()
	if same && !force {
		return
	}
	if len(list) == 0 {
		return
	}
	if err := r.broadcastApp(appKindRoster, rosterBody{Peers: list}, ""); err != nil {
		r.log.Debug("could not send the member list", "error", err)
	}
}

func (r *Room) receiveRoster(from *peer.Peer, msg peer.AppMessage) {
	if r.isHost || !from.IsHost() {
		return // only the host knows the membership
	}
	var b rosterBody
	if json.Unmarshal(msg.Body, &b) != nil {
		return
	}
	self := string(r.pm.LocalID())
	var keep []rosterEntry
	for _, e := range b.Peers {
		if e.ID == "" || e.ID == self || len(keep) >= MaxPeersCeiling {
			continue
		}
		e.Name = cleanName(e.Name)
		keep = append(keep, e)
	}
	before, _ := json.Marshal(r.rosterCopy())
	r.roster.mu.Lock()
	r.roster.peers = keep
	r.roster.mu.Unlock()
	after, _ := json.Marshal(keep)
	if string(before) != string(after) && r.onRosterChange != nil {
		go r.onRosterChange()
	}
}

func (r *Room) rosterCopy() []rosterEntry {
	r.roster.mu.Lock()
	defer r.roster.mu.Unlock()
	return append([]rosterEntry(nil), r.roster.peers...)
}

// withRoster adds the players a guest has no link to.
func (r *Room) withRoster(peers []protocol.PeerSummary) []protocol.PeerSummary {
	if r.isHost {
		return peers
	}
	have := map[string]bool{}
	for _, p := range peers {
		if p.PeerID != "" {
			have[string(p.PeerID)] = true
		}
	}
	for _, e := range r.rosterCopy() {
		if have[e.ID] {
			continue
		}
		st := e.St
		if st == "" {
			st = protocol.PeerActive
		}
		peers = append(peers, protocol.PeerSummary{
			PeerID:         protocol.PeerID(e.ID),
			DisplayName:    e.Name,
			State:          st,
			LinkID:         "hub-" + e.ID,
			VirtualAddress: e.Addr,
			LinkKind:       LinkHub,
			PublicKey:      e.Key,
			Fingerprint:    e.FP,
		})
	}
	return peers
}
