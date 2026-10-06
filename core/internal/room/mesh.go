package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Mesh: direct guest-to-guest links.
//
// The room is hub and spoke by construction - a guest is admitted through the
// host - but traffic between two guests does not have to go through the host.
// Once a guest is active, the host introduces it to every other active guest;
// the newcomer then negotiates a direct WebRTC link to each of them, with the
// offer and answer relayed over the existing control channels. No human has
// to carry a second code.
//
// Routing follows automatically: a guest's router forwards to a peer it holds
// an address for, and falls back to the host whenever the direct link is not
// usable (network.Service). Broadcast and multicast keep going through the
// host, which is the only node that sees the whole room, so nothing is ever
// delivered twice.
//
// A failed mesh link costs nothing but the attempt: traffic simply keeps using
// the host, as it did before.

const (
	appKindMesh = "mesh"

	meshIntro  = "intro"
	meshOffer  = "offer"
	meshAnswer = "answer"

	// meshLinkPrefix keeps mesh link ids apart from the ids the host mints for
	// guests and from the host's own id on a guest.
	meshLinkPrefix = "m-"

	meshNegotiateTimeout = 30 * time.Second
)

type meshPeerInfo struct {
	ID   string `json:"id"`
	Addr string `json:"a"`
	Name string `json:"n,omitempty"`
}

type meshBody struct {
	Op    string         `json:"op"`
	Peers []meshPeerInfo `json:"p,omitempty"`
	SDP   string         `json:"s,omitempty"`
	Addr  string         `json:"a,omitempty"`
}

type meshState struct {
	mu sync.Mutex
	// addrs maps a mesh link to the peer's room address, learned from the
	// host's introduction or the peer's own offer.
	addrs map[transport.PeerID]netip.Addr
	// pending are links this node offered and is waiting to hear back about.
	pending map[transport.PeerID]time.Time
	// introduced records, on the host, which pairs it already introduced.
	introduced map[string]bool
}

func newMeshState() *meshState {
	return &meshState{
		addrs:      map[transport.PeerID]netip.Addr{},
		pending:    map[transport.PeerID]time.Time{},
		introduced: map[string]bool{},
	}
}

func meshLink(id string) transport.PeerID { return transport.PeerID(meshLinkPrefix + id) }

// installMesh registers the mesh message handler.
func (r *Room) installMesh() {
	r.onAppKind(appKindMesh, r.handleMesh)
}

// meshAddr returns the room address of a mesh peer, if known.
func (r *Room) meshAddr(link transport.PeerID) (netip.Addr, bool) {
	r.meshSt.mu.Lock()
	defer r.meshSt.mu.Unlock()
	a, ok := r.meshSt.addrs[link]
	return a, ok
}

// meshObserve runs on the host for every peer event and introduces a guest
// that just became active to the others.
func (r *Room) meshObserve(ev peer.Event) {
	if !r.isHost || ev.Kind != protocol.EventPeerConnected || ev.Body.PeerID == "" {
		return
	}
	go r.meshIntroduce(ev.Body)
}

func (r *Room) meshIntroduce(newcomer protocol.PeerEvent) {
	newID := string(newcomer.PeerID)
	var others []meshPeerInfo
	for _, p := range r.pm.List() {
		if p.PeerID == "" || string(p.PeerID) == newID || p.IsSelf || !isLive(p.State) {
			continue
		}
		pair := pairKey(newID, string(p.PeerID))
		r.meshSt.mu.Lock()
		done := r.meshSt.introduced[pair]
		r.meshSt.introduced[pair] = true
		r.meshSt.mu.Unlock()
		if done {
			continue
		}
		addr, ok := r.Lease(transport.PeerID(p.TransportPeerID))
		if !ok {
			continue
		}
		others = append(others, meshPeerInfo{ID: string(p.PeerID), Addr: addr.String(), Name: p.DisplayName})
	}
	if len(others) == 0 {
		return
	}
	if err := r.broadcastApp(appKindMesh, meshBody{Op: meshIntro, Peers: others}, newID); err != nil {
		r.log.Debug("could not send a mesh introduction", "to", newID, "error", err)
		return
	}
	r.log.Info("introduced a guest to the room for direct links", "guest", newID, "peers", len(others))
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func isLive(s protocol.PeerState) bool {
	return s == protocol.PeerActive || s == protocol.PeerDegraded || s == protocol.PeerNetworkReady
}

// handleMesh processes mesh signalling.
func (r *Room) handleMesh(from *peer.Peer, msg peer.AppMessage) {
	var body meshBody
	if err := json.Unmarshal(msg.Body, &body); err != nil {
		return
	}
	switch body.Op {
	case meshIntro:
		// Only the host introduces; a guest cannot make others dial it.
		if !from.IsHost() || !r.meshEnabled() {
			return
		}
		for _, p := range body.Peers {
			p := p
			if p.ID == "" || p.ID == string(r.pm.LocalID()) {
				continue
			}
			go r.meshConnect(p)
		}
	case meshOffer:
		if r.isHost || !r.meshEnabled() {
			return
		}
		go r.meshAccept(msg.Origin, body)
	case meshAnswer:
		go r.meshComplete(msg.Origin, body)
	}
}

func (r *Room) meshEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mesh
}

func (r *Room) signalling() (transport.Signalling, bool) {
	sig, ok := r.tr.(transport.Signalling)
	return sig, ok && supportsSignalling(r.tr)
}

// meshConnect is the newcomer's side: offer a direct link to one peer.
func (r *Room) meshConnect(p meshPeerInfo) {
	link := meshLink(p.ID)
	if _, err := r.pm.Get(link); err == nil {
		return // already linked
	}
	addr, err := netip.ParseAddr(p.Addr)
	subnet, okSub := r.roomSubnet()
	if err != nil || !okSub || !subnet.Contains(addr) {
		return
	}
	sig, ok := r.signalling()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), meshNegotiateTimeout)
	defer cancel()
	offer, err := sig.CreateOffer(ctx, transport.PeerInfo{ID: link, RoomID: r.id})
	if err != nil {
		r.log.Debug("mesh offer failed", "peer", p.ID, "error", err)
		_ = r.tr.Close(link)
		return
	}
	r.meshSt.mu.Lock()
	r.meshSt.addrs[link] = addr
	r.meshSt.pending[link] = r.now()
	r.meshSt.mu.Unlock()

	local := r.localAddr()
	if err := r.broadcastApp(appKindMesh, meshBody{
		Op:   meshOffer,
		SDP:  base64.RawURLEncoding.EncodeToString(offer.Data),
		Addr: local.String(),
	}, p.ID); err != nil {
		r.meshForget(link)
		return
	}
	// Give up on a peer that never answers, so its sockets do not linger.
	time.AfterFunc(meshNegotiateTimeout, func() {
		r.meshSt.mu.Lock()
		_, still := r.meshSt.pending[link]
		r.meshSt.mu.Unlock()
		if still {
			r.log.Debug("a mesh offer was not answered", "peer", p.ID)
			r.meshForget(link)
		}
	})
}

// meshAccept is the existing guest's side: answer the newcomer's offer.
func (r *Room) meshAccept(origin string, body meshBody) {
	link := meshLink(origin)
	if _, err := r.pm.Get(link); err == nil {
		return
	}
	addr, err := netip.ParseAddr(body.Addr)
	subnet, okSub := r.roomSubnet()
	if err != nil || !okSub || !subnet.Contains(addr) || addr == r.localAddr() {
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(body.SDP)
	if err != nil {
		return
	}
	sig, ok := r.signalling()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), meshNegotiateTimeout)
	defer cancel()
	answer, err := sig.AcceptOffer(ctx, transport.PeerInfo{ID: link, RoomID: r.id},
		transport.Description{Kind: transport.DescriptionOffer, Data: raw})
	if err != nil {
		r.log.Debug("mesh answer failed", "peer", origin, "error", err)
		_ = r.tr.Close(link)
		return
	}
	r.meshSt.mu.Lock()
	r.meshSt.addrs[link] = addr
	r.meshSt.mu.Unlock()
	if _, err := r.pm.Add(link, false, false); err != nil {
		r.meshForget(link)
		return
	}
	r.pm.ExpectIdentity(link, protocol.PeerID(origin))
	if err := r.broadcastApp(appKindMesh, meshBody{
		Op:  meshAnswer,
		SDP: base64.RawURLEncoding.EncodeToString(answer.Data),
	}, origin); err != nil {
		r.meshForget(link)
	}
}

// meshComplete is the newcomer's side again: apply the answer.
func (r *Room) meshComplete(origin string, body meshBody) {
	link := meshLink(origin)
	r.meshSt.mu.Lock()
	_, pending := r.meshSt.pending[link]
	delete(r.meshSt.pending, link)
	r.meshSt.mu.Unlock()
	if !pending {
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(body.SDP)
	if err != nil {
		r.meshForget(link)
		return
	}
	sig, ok := r.signalling()
	if !ok {
		return
	}
	if _, err := r.pm.Add(link, false, false); err != nil {
		r.meshForget(link)
		return
	}
	r.pm.ExpectIdentity(link, protocol.PeerID(origin))
	ctx, cancel := context.WithTimeout(context.Background(), meshNegotiateTimeout)
	defer cancel()
	if err := sig.ApplyAnswer(ctx, transport.PeerInfo{ID: link, RoomID: r.id},
		transport.Description{Kind: transport.DescriptionAnswer, Data: raw}); err != nil {
		r.log.Info("a direct link to another player could not be established; traffic stays via the host",
			"peer", origin, "error", err)
		r.pm.Remove(link)
		r.meshForget(link)
		return
	}
	r.log.Info("direct link to another player established", "peer", origin)
	r.reconcile()
}

func (r *Room) meshForget(link transport.PeerID) {
	r.meshSt.mu.Lock()
	delete(r.meshSt.addrs, link)
	delete(r.meshSt.pending, link)
	r.meshSt.mu.Unlock()
	_ = r.tr.Close(link)
}
