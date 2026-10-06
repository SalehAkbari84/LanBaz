package room

import (
	"context"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// The room's virtual LAN.
//
// A room owns one network.Service for its whole life. It is created with the
// room and torn down with it, because the two are one thing from the user's
// point of view: joining a room is what gives a machine an address in it, and
// leaving is what takes it away.
//
// # Addressing is the host's to give
//
// The room id deterministically picks a /24 out of 10.200.0.0/16, so both sides
// can compute the subnet without negotiating. Inside that subnet the host is
// the authority: it is the only participant that knows the full membership, so
// it allocates each guest's address when it issues a pairing code and names that
// address in the code. A guest therefore learns its own address the moment it
// decodes, with no extra round trip through a handshake that already needs a
// human to carry two blobs.
//
// # Failure is not fatal
//
// A room whose adapter cannot be created is still a room. Pairing, liveness and
// the control API all keep working; only the packet path is missing, and
// network.status says so with the driver's own error. Failing the join instead
// would leave a user unable to see their friends, unable to diagnose why, and
// unable to do anything except uninstall.

// NetworkFactory builds the per-room virtual network. It is injected so this
// package never learns that Wintun exists.
type NetworkFactory func(ctx context.Context, roomID string, tr transport.Transport, subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error)

// subnetString renders the room's address space for a pairing code.
func (r *Room) subnetString() string {
	if s, ok := r.roomSubnet(); ok {
		return s.String()
	}
	return ""
}

// allocateGuestAddr reserves an address for a guest that has not arrived yet.
//
// The reservation is held against the guest's transport id rather than its
// announced identity, because the id is minted here and travels in the code. An
// identity-based reservation could not be made at all: at issue time the host
// has never seen the guest's key.
func (r *Room) allocateGuestAddr(peerID, identity string) (netip.Addr, error) {
	r.netMu.RLock()
	alloc := r.allocator
	subnet := r.subnet
	prefer := r.leaseHints[identity]
	r.netMu.RUnlock()
	if alloc != nil && identity != "" {
		// A friend invited by name: their remembered address, or the one their
		// identity hashes to, so they keep the same IP every time.
		if !prefer.IsValid() || !subnet.Contains(prefer) {
			prefer = ipam.PreferredFor(subnet, identity)
		}
		return alloc.AllocatePreferred(r.id, peerID, prefer)
	}
	if alloc == nil {
		// No allocator means this build has no virtual LAN. Handing back the
		// host's own address would be wrong, so the code simply carries no
		// addressing and the guest derives what it can.
		return netip.Addr{}, nil
	}
	return alloc.Allocate(r.id, peerID)
}

// dropPending forgets a pending guest and hands its address back.
//
// It is the failure half of IssuePairing: the reservation is made before the
// offer is minted, so every early return after it has to undo the reservation or
// the room silently loses an address per failed attempt.
func (r *Room) dropPending(peerID transport.PeerID) {
	p := r.takePending(peerID)
	if p == nil {
		return
	}
	// The offer link was built for this guest alone; nobody else can ever
	// answer it, so its ICE sockets are closed with the reservation.
	_ = r.tr.Close(peerID)
	if !p.addr.IsValid() {
		return
	}
	r.netMu.RLock()
	alloc := r.allocator
	r.netMu.RUnlock()
	if alloc != nil {
		alloc.Release(r.id, string(peerID))
	}
}

// observePeer keeps the addressing in step with the peer manager.
//
// Departures are handled directly, by link id: the lease was taken out against
// the link, so releasing anything else leaves the address held and the route
// installed for a peer that is gone.
//
// Arrivals go through reconcile rather than being addressed at the point of
// creation. A peer record is created in several places - a join, an accept - and
// a leak in addressing from missing one of them is invisible until a room fills
// up. Reconcile-and-sweep is the shape that cannot drift, and it runs here as
// well as on the tick because the tick is thirty seconds long: a guest that
// joined but had no route to the host yet is a room that looks joined and plays
// like it is not, and half a minute of that is the bug users report as "it did
// not work and then it started working".
func (r *Room) observePeer(ev peer.Event) {
	r.meshObserve(ev)
	if ev.Kind == protocol.EventPeerConnected && ev.Body.PeerID != "" {
		// Tell a newly connected player what we are playing right away.
		go r.announcePresence(string(ev.Body.PeerID))
	}
	if ev.Kind == protocol.EventPeerLeft && ev.Body.PeerID != "" {
		r.pres.mu.Lock()
		_, had := r.pres.peers[string(ev.Body.PeerID)]
		delete(r.pres.peers, string(ev.Body.PeerID))
		fn := r.pres.onEcho
		r.pres.mu.Unlock()
		if had && fn != nil {
			fn(protocol.Presence{PeerID: ev.Body.PeerID, RoomID: r.id, At: r.now().UTC()})
		}
	}
	if ev.Kind == protocol.EventPeerLeft {
		if id := ev.Body.TransportPeerID; id != "" {
			r.unbindPeer(transport.PeerID(id))
			if strings.HasPrefix(string(id), meshLinkPrefix) {
				r.meshSt.mu.Lock()
				delete(r.meshSt.addrs, transport.PeerID(id))
				r.meshSt.mu.Unlock()
			}
		}
		if !r.isHost && ev.Body.Peer != nil && ev.Body.Peer.IsHost && r.onHostGone != nil {
			reason := ev.Body.Reason
			if reason == "" || reason == "removed" {
				reason = "host_left"
			}
			go r.onHostGone(reason)
		}
		return
	}
	if ev.Kind == protocol.EventPeerJoined || ev.Kind == protocol.EventPeerState {
		r.reconcile()
	}
}

// ReserveAddressing claims the room's subnet in the shared allocator.
//
// Only a host calls it, and only when it actually becomes one: a guest's subnet
// is the one its pairing code names, and claiming the derived one on its way
// would let a guest briefly hold a range it does not use - which on a machine
// with several rooms is exactly the range a real host is about to ask for.
func (r *Room) ReserveAddressing() error {
	r.netMu.RLock()
	alloc := r.allocator
	subnet, ok := r.subnet, r.subnet.IsValid()
	r.netMu.RUnlock()
	if alloc == nil || !ok {
		return nil
	}
	if want := r.wantSubnet; r.isHost && want.IsValid() && want != subnet {
		if _, err := alloc.Adopt(r.id, want); err == nil {
			r.log.Info("kept network reopened on its own subnet, so everybody keeps their IP", "subnet", want.String())
			return r.retargetAddressing(want, false)
		} else {
			r.log.Warn("the kept network's subnet is taken on this PC; using another one, so IPs change this time",
				"wanted", want.String(), "error", err)
		}
	} else if want.IsValid() && want == subnet {
		if _, err := alloc.Adopt(r.id, want); err == nil {
			return nil
		}
	}
	claimed, probed, err := alloc.Reserve(r.id)
	if err != nil {
		return err
	}
	r.netMu.Lock()
	r.subnet = claimed
	r.netMu.Unlock()
	if claimed != subnet {
		// The derived subnet belonged to a room already open here, so this one
		// moved. Both the reservation and the pairing code have to say so, or two
		// machines would configure different LANs and then find each other
		// missing.
		if err := r.retargetAddressing(claimed, probed); err != nil {
			return err
		}
	}
	return nil
}

// adoptAddressing installs the addressing a pairing code named.
//
// Only a guest calls this, and only after the code's signature has been
// verified: the subnet is authenticated by the room secret, so adopting it
// trusts the host rather than the code's author.
func (r *Room) adoptAddressing(subnet netip.Prefix, local netip.Addr) error {
	if !subnet.IsValid() || !subnet.Addr().Is4() {
		return protocol.NewError(protocol.CodePairingInvalid,
			"room: the pairing code carries an unusable subnet")
	}
	if !local.IsValid() || !subnet.Contains(local) {
		return protocol.NewError(protocol.CodePairingInvalid,
			"room: the pairing code carries a guest address outside its own subnet")
	}
	r.netMu.RLock()
	alloc := r.allocator
	r.netMu.RUnlock()
	if alloc != nil {
		probed, err := alloc.Adopt(r.id, subnet)
		if err != nil {
			return err
		}
		if probed {
			r.log.Info("the room subnet is not the one the room id derives to",
				"room", r.id, "subnet", subnet.String())
		}
	}
	r.netMu.Lock()
	r.subnet, r.local = subnet.Masked(), local
	r.netMu.Unlock()
	return nil
}

// retargetAddressing moves a host onto a probed subnet and reissues its code.
//
// This is the ugly case and it is worth being explicit about: two rooms on one
// machine hashed to the same index. The second one moves, and any code already
// handed out names the wrong subnet - so it is invalidated and a fresh one is
// issued. The alternative is leaving a user with a code that produces a LAN at
// an address nobody else is using.
func (r *Room) retargetAddressing(subnet netip.Prefix, probed bool) error {
	r.netMu.Lock()
	r.subnet = subnet
	// The host's own address moves with the subnet. Leaving it behind would
	// put the host outside its own room, and the router refuses to start with
	// a local address outside the subnet - so the host would get no LAN at all.
	if r.isHost {
		r.local = ipam.HostAddress(subnet)
	}
	r.netMu.Unlock()
	if !probed {
		return nil
	}
	_, err := r.IssuePairing(context.Background(), DefaultPairingTTL)
	return err
}

// attachTimeout bounds creating and configuring the adapter.
const attachTimeout = 90 * time.Second

// isClosed reports whether Close has started.
func (r *Room) isClosed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// attachNet starts the room's virtual LAN.
//
// The addressing is whatever the room has already settled on. For a host that is
// the subnet it reserved; for a guest it is the subnet the host's pairing code
// named. Either way the caller has already verified it, so this function's job
// is to hand it to the network layer and report honestly if the adapter cannot
// be brought up.
func (r *Room) attachNet(ctx context.Context, factory NetworkFactory) {
	if factory == nil {
		// A build with no adapter backend still has to explain itself. Recording
		// the reason here rather than leaving it empty is what turns "the LAN
		// does not work" into a sentence the user can act on.
		r.netMu.Lock()
		if r.netErr == nil {
			msg := "room: this build was configured without a virtual network backend"
			if r.Mode() == protocol.RoomModeL2 {
				msg = "this is a classic LAN (L2) room, which needs the TAP-Windows driver; install it from the LanBaz installer or OpenVPN's TAP-Windows package"
			}
			r.netErr = protocol.NewError(protocol.CodeUnsupportedVersion, msg)
		}
		r.netMu.Unlock()
		return
	}
	// attachMu serialises this with detachNetwork. Without it a room closed
	// while its adapter was still being configured (a guest whose join failed
	// a moment later) left the adapter behind: Close found no network yet,
	// and the attach finished afterwards with nobody left to remove it. The
	// next join of the same room then hit "file already exists".
	r.attachMu.Lock()
	defer r.attachMu.Unlock()
	if r.isClosed() {
		return
	}
	r.netMu.RLock()
	existing := r.net
	subnet, ok := r.subnet, r.subnet.IsValid()
	local := r.local
	r.netMu.RUnlock()
	if existing != nil || !ok {
		return
	}
	// The request that created the room must not cancel the adapter setup half
	// way: a cancelled PowerShell step leaves a half-configured adapter.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attachTimeout)
	defer cancel()

	svc, err := factory(ctx, r.id, r.tr, subnet, local, r.isHost)
	if err != nil {
		// Nothing above this line changes: the room exists, the peers connect and
		// the UI works. What is lost is the packet path, and that is reported
		// rather than swallowed.
		r.log.Warn("the virtual network could not be created; the room works without it",
			"room", r.id, "error", err)
		r.netMu.Lock()
		r.netErr = err
		r.netMu.Unlock()
		return
	}
	if err := svc.Start(ctx); err != nil {
		r.log.Warn("the virtual network could not be started; the room works without it",
			"room", r.id, "error", err)
		r.netMu.Lock()
		r.netErr = err
		r.netMu.Unlock()
		return
	}
	if r.isClosed() {
		r.log.Info("the room closed while its network was starting; removing it", "room", r.id)
		if err := svc.Stop(ctx); err != nil {
			r.log.Warn("the virtual network did not shut down cleanly", "room", r.id, "error", err)
		}
		return
	}
	r.netMu.Lock()
	r.net = svc
	r.netErr = nil
	r.netMu.Unlock()
	if r.nameRes {
		if ns, ok := svc.(interface {
			SetNameSource(func() map[string]netip.Addr)
		}); ok {
			ns.SetNameSource(r.playerNames)
		}
	}
	r.log.Info("virtual network attached",
		"room", r.id, "subnet", subnet.String(), "address", local.String(), "host", r.isHost)
	r.publishNetworkEvent(protocol.EventNetworkChanged)
}

// Network returns the room's virtual LAN, or nil when none could be created.
func (r *Room) Network() network.VirtualNetwork {
	r.netMu.RLock()
	defer r.netMu.RUnlock()
	return r.net
}

// CreatedAt returns when the room was created, used to pick a stable "first"
// room for a query that does not name one.
func (r *Room) CreatedAt() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.createdAt
}

// NetworkError returns why the virtual LAN is missing, if it is.
func (r *Room) NetworkError() error {
	r.netMu.RLock()
	defer r.netMu.RUnlock()
	return r.netErr
}

// bindPeer gives a peer its address in this room's subnet and installs it in the
// routing table.
//
// The host allocates; a guest is told. That asymmetry is the point - see the
// comment at the top of this file - and it is why this function takes the
// address rather than computing one.
func (r *Room) bindPeer(peerID transport.PeerID, addr netip.Addr) {
	r.netMu.RLock()
	svc := r.net
	r.netMu.RUnlock()
	if svc == nil {
		return
	}
	if err := svc.AddPeer(string(peerID), addr.String()); err != nil {
		// The peer stays connected and the UI still lists it; it simply has no
		// address. Dropping the link would be a far worse answer to a
		// configuration mistake.
		r.log.Warn("a peer could not be given an address",
			"room", r.id, "peer", string(peerID), "address", addr.String(), "error", err)
		return
	}
	r.leasesMu.Lock()
	r.leases[peerID] = addr
	r.leasesMu.Unlock()
	if p, err := r.pm.Get(peerID); err == nil && p.Announced() != "" {
		go r.refreshPresence(string(p.Announced()))
	}
	r.log.Info("peer addressed",
		"room", r.id, "peer", string(peerID), "address", addr.String())
	r.publishNetworkEvent(protocol.EventNetworkChanged)
}

// unbindPeer releases a peer's address.
func (r *Room) unbindPeer(peerID transport.PeerID) {
	r.leasesMu.Lock()
	_, had := r.leases[peerID]
	delete(r.leases, peerID)
	r.leasesMu.Unlock()
	if !had {
		return
	}
	r.netMu.RLock()
	svc := r.net
	alloc := r.allocator
	r.netMu.RUnlock()
	if svc != nil {
		svc.RemovePeer(string(peerID))
	}
	// The lease goes back to the pool with the peer. Only a host allocates; a
	// guest's single binding is the host's computed address, which was never
	// taken from the allocator, and releasing an id it does not hold is a no-op.
	if alloc != nil && r.isHost {
		alloc.Release(r.id, string(peerID))
	}
	r.publishNetworkEvent(protocol.EventNetworkChanged)
}

// Lease returns a peer's address in this room, if it has one.
func (r *Room) Lease(peerID transport.PeerID) (netip.Addr, bool) {
	r.leasesMu.RLock()
	defer r.leasesMu.RUnlock()
	addr, ok := r.leases[peerID]
	return addr, ok
}

// Leases returns every peer address in this room.
func (r *Room) Leases() map[string]string {
	r.leasesMu.RLock()
	defer r.leasesMu.RUnlock()
	out := make(map[string]string, len(r.leases))
	for id, addr := range r.leases {
		out[string(id)] = addr.String()
	}
	return out
}

// reconcile makes the addressing match the peer manager.
//
// It runs from the room's maintenance tick rather than from every mutation
// site. Peer records are created and removed in several places - a join, an
// accept, a goodbye, a liveness failure, a kick - and hooking each one would
// mean the routing table could drift if any of them were ever missed.
// Reconciling against the authority makes that class of bug impossible: whatever
// happened to the peer list, the table catches up.
func (r *Room) reconcile() {
	r.netMu.RLock()
	alloc := r.allocator
	svc := r.net
	r.netMu.RUnlock()
	if alloc == nil || svc == nil {
		return
	}
	for _, p := range r.pm.List() {
		peerID := transport.PeerID(p.TransportPeerID)
		if peerID == "" {
			continue
		}
		if _, bound := r.Lease(peerID); bound {
			continue
		}
		// Only a link that is actually usable gets an address. Handing one to a
		// peer that never connects would slowly fill the subnet with addresses
		// nobody can reach, which is indistinguishable from the IPAM being
		// exhausted when the room finally fills up.
		if !r.pm.Ready(peerID) {
			continue
		}
		// Who hands out addresses decides what they mean. A host allocates from
		// the pool; a guest has exactly one address to give, the host's, and it
		// is not allocated but computed - see hostAddress.
		if !r.isHost {
			if p.IsHost {
				if addr, ok := r.hostAddress(); ok {
					r.bindPeer(peerID, addr)
				}
			} else if addr, ok := r.meshAddr(peerID); ok {
				// A direct link to another guest: route to it without the host.
				r.bindPeer(peerID, addr)
			}
			// Guests reach each other through the hub, so nothing else in the
			// list needs a route. Giving one anyway would invent an address the
			// host has already promised to somebody else.
			continue
		}
		addr, err := alloc.Allocate(r.id, string(peerID))
		if err != nil {
			r.log.Warn("no address is available for a peer",
				"room", r.id, "peer", string(peerID), "error", err)
			continue
		}
		r.bindPeer(peerID, addr)
	}
	// Anything the peer manager no longer lists has lost its address. The sweep
	// walks the room's own lease table, not the router: unbindPeer removes a
	// peer from the router first, so a router-based sweep never sees exactly the
	// entries it exists to clean up.
	r.leasesMu.RLock()
	bound := make([]transport.PeerID, 0, len(r.leases))
	for id := range r.leases {
		bound = append(bound, id)
	}
	r.leasesMu.RUnlock()
	for _, id := range bound {
		if _, err := r.pm.Get(id); err != nil {
			r.unbindPeer(id)
		}
	}
	_ = svcPeers
}

// hostAddress returns the room host's address in this room's subnet.
//
// It is computed, never allocated. Both sides can derive it from the subnet the
// pairing code named, and they must agree: allocating the host an address from
// the guest's own pool would put it at .2 on one machine while it believes it is
// at .1 on the other, and every packet between them would then be dropped on
// arrival as a spoof of an address nobody assigned it.
func (r *Room) hostAddress() (netip.Addr, bool) {
	subnet, ok := r.roomSubnet()
	if !ok {
		return netip.Addr{}, false
	}
	addr := ipam.HostAddress(subnet)
	return addr, addr.IsValid()
}

// svcPeers returns the peers the routing table currently holds.
func svcPeers(svc network.VirtualNetwork) []transport.PeerID {
	router, ok := svc.(interface{ Router() *network.Router })
	if !ok {
		return nil
	}
	return router.Router().Peers()
}

// detachNetwork tears the virtual LAN down. It is called from Room.Close after
// the peers have said goodbye, so a peer's last packets still have somewhere to
// go.
func (r *Room) detachNetwork(ctx context.Context) {
	// Wait for an attach in flight; it sees the room closed and cleans up
	// after itself, or finishes and is removed here.
	r.attachMu.Lock()
	defer r.attachMu.Unlock()
	r.netMu.Lock()
	svc := r.net
	r.net = nil
	r.netMu.Unlock()
	if svc == nil {
		return
	}
	if err := svc.Stop(ctx); err != nil {
		r.log.Warn("the virtual network did not shut down cleanly", "room", r.id, "error", err)
	}
	r.log.Info("virtual network detached", "room", r.id)
	r.publishNetworkEvent(protocol.EventNetworkChanged)
}

// releaseLeases hands every address in this room back to the allocator.
func (r *Room) releaseLeases() {
	r.netMu.RLock()
	alloc := r.allocator
	r.netMu.RUnlock()
	if alloc != nil {
		alloc.ReleaseRoom(r.id)
	}
}

// NetworkStatus renders the room's network.status payload.
//
// A room with no network still answers, because "why is there no LAN" is exactly
// the question a user arrives with, and an error would send them looking for a
// failure that is not there.
func (r *Room) NetworkStatus(ctx context.Context) protocol.NetworkStatus {
	out := protocol.NetworkStatus{
		RoomID:    r.id,
		IsHost:    r.isHost,
		State:     string(network.StateStopped),
		Peers:     []protocol.NetworkPeer{},
		Routes:    []protocol.RouteEntry{},
		StartedAt: time.Time{},
	}
	if subnet, ok := r.roomSubnet(); ok {
		out.Subnet = subnet.String()
		out.LocalAddress = r.localAddr().String()
		out.Interface = adapterNameFor(r.id)
	}
	svc := r.Network()
	if svc == nil {
		out.Note = r.networkNote()
		return out
	}
	st, err := svc.Status(ctx)
	if err != nil {
		out.Note = "the virtual network could not be read: " + err.Error()
		return out
	}
	out.State = string(st.State)
	out.Adapter = st.Adapter
	out.MTU = st.Address.MTU
	out.Broadcast = st.Address.Broadcast
	out.Multicast = st.Address.Multicast
	out.StartedAt = st.StartedAt
	out.Metrics = protocol.NetworkMetrics{
		Delivered:      st.Metrics.Delivered,
		Forwarded:      st.Metrics.Forwarded,
		Relayed:        st.Metrics.Relayed,
		Dropped:        st.Metrics.Dropped,
		Spoofed:        st.Metrics.Spoofed,
		RateLimited:    st.Metrics.RateLimited,
		Malformed:      st.Metrics.Malformed,
		Ignored:        st.Metrics.Ignored,
		Oversized:      st.Metrics.Oversized,
		NoRoute:        st.Metrics.NoRoute,
		BytesDelivered: st.Metrics.BytesDelivered,
		BytesForwarded: st.Metrics.BytesForwarded,
	}
	for _, r := range st.Routes {
		out.Routes = append(out.Routes, protocol.RouteEntry{
			Destination: r.Destination.String(),
			NextHop:     r.NextHop.String(),
			Peer:        r.Peer,
			Managed:     r.Managed,
			Note:        r.Note,
		})
	}
	names := r.displayNames()
	for _, p := range st.Peers {
		entry := protocol.NetworkPeer{
			PeerID:  p.PeerID,
			Address: p.Address.String(),
			Role:    p.Role,
			IsHost:  p.IsHost,
			Local:   p.Local,
		}
		entry.DisplayName = names[p.PeerID]
		if p.PeerID == "" {
			entry.DisplayName = r.displayName
		}
		out.Peers = append(out.Peers, entry)
	}
	out.Note = joinNotes(st.Note, r.networkNote())
	return out
}

// networkNote explains a missing virtual LAN.
func (r *Room) networkNote() string {
	if err := r.NetworkError(); err != nil {
		return "no virtual network: " + err.Error()
	}
	return ""
}

// displayNames maps peer ids to the names the peer manager knows.
func (r *Room) displayNames() map[string]string {
	out := map[string]string{}
	for _, p := range r.pm.List() {
		out[string(p.PeerID)] = p.DisplayName
	}
	return out
}

// publishNetworkEvent notifies the UI that the addressing changed.
func (r *Room) publishNetworkEvent(kind string) {
	r.netMu.RLock()
	onEvent := r.onNetworkEvent
	r.netMu.RUnlock()
	if onEvent == nil {
		return
	}
	onEvent(kind, protocol.NetworkEvent{
		RoomID: r.id,
		Status: r.NetworkStatus(context.Background()),
		At:     time.Now().UTC(),
	})
}

// adapterNameFor mirrors the name the service derives, so a room that has not
// managed to create its adapter yet can still say what it would have been.
func adapterNameFor(roomID string) string {
	id := roomID
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	return "LanBaz-" + id
}

// joinNotes renders the non-empty notes.
func joinNotes(notes ...string) string {
	out := ""
	for _, n := range notes {
		if n == "" {
			continue
		}
		if out != "" {
			out += "; "
		}
		out += n
	}
	return out
}

// First returns the room a room-less network query applies to.
//
// There is no "primary" room in LanBaz - a machine can host some and be a guest
// in others at once - so this picks the oldest. The choice only affects which
// room a query with no id describes, and the answer names the room either way,
// so a client can never mistake one room's addressing for another's.
func (m *Manager) First() (string, bool) {
	rooms := m.All()
	if len(rooms) == 0 {
		return "", false
	}
	sort.Slice(rooms, func(i, j int) bool {
		return rooms[i].CreatedAt().Before(rooms[j].CreatedAt())
	})
	return rooms[0].ID(), true
}

// RoomIDs returns every open room id, in creation order.
func (m *Manager) RoomIDs() []string {
	rooms := m.All()
	sort.Slice(rooms, func(i, j int) bool {
		return rooms[i].CreatedAt().Before(rooms[j].CreatedAt())
	})
	out := make([]string, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.ID())
	}
	return out
}

// NetworkStatus renders one room's virtual LAN.
func (m *Manager) NetworkStatus(ctx context.Context, roomID string) (protocol.NetworkStatus, error) {
	r, err := m.Get(roomID)
	if err != nil {
		return protocol.NetworkStatus{}, err
	}
	return r.NetworkStatus(ctx), nil
}

// NetworkRoutes renders one room's routing table.
func (m *Manager) NetworkRoutes(ctx context.Context, roomID string) ([]protocol.RouteEntry, error) {
	r, err := m.Get(roomID)
	if err != nil {
		return nil, err
	}
	st := r.Network()
	if st == nil {
		return []protocol.RouteEntry{}, nil
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.RouteEntry, 0, len(routes))
	for _, rt := range routes {
		out = append(out, protocol.RouteEntry{
			Destination: rt.Destination.String(),
			NextHop:     rt.NextHop.String(),
			Peer:        rt.Peer,
			Managed:     rt.Managed,
			Note:        rt.Note,
		})
	}
	return out, nil
}

// sync is the small amount of mutual exclusion the network fields need.
//
// They live apart from Room.mu because the room's own lock is held while the
// peer manager calls back into it during a close, and taking the same lock from
// the network path would be a deadlock waiting for the right interleaving.

// playerNames maps each player's name label to their room address, for the
// name.local responder.
func (r *Room) playerNames() map[string]netip.Addr {
	out := map[string]netip.Addr{}
	for _, p := range r.pm.List() {
		if p.IsSelf || p.DisplayName == "" {
			continue
		}
		label := network.NameLabel(p.DisplayName)
		if label == "" {
			continue
		}
		if addr, ok := r.Lease(transport.PeerID(p.TransportPeerID)); ok {
			out[label] = addr
		}
	}
	return out
}
