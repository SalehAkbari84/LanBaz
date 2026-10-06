package room_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/network/memory"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// This file is the end-to-end proof of Phase 2's claim: a room that completed
// the serverless handshake of Phase 1 comes out the other side with an address in
// a subnet both machines agree on, and a packet a game sends reaches the other
// machine's stack.
//
// Everything runs over loopback with STUN off and the in-memory adapter, so the
// test is deterministic, offline and needs no administrator rights. That is a
// deliberate trade: the Wintun backend cannot be exercised here, but every
// decision that can be wrong - which subnet, which address, who relays, what
// happens when a source address does not belong to the sender - is made above the
// adapter and is fully covered. What the driver adds is the last hundred
// milliseconds, not the logic.

// netSide is one installation with a virtual network attached.
type netSide struct {
	name  string
	mgr   *room.Manager
	alloc *ipam.Allocator
	// adapters records the adapter each room got, so a test can inject a packet
	// as if a local game had sent it.
	adapters map[string]*memory.Adapter
}

// newNetSide builds a room manager with the in-memory virtual network wired in.
func newNetSide(t *testing.T, name string, events chan<- room.Notice) *netSide {
	t.Helper()
	side := &netSide{
		name:     name,
		alloc:    ipam.New(ipam.Pool),
		adapters: map[string]*memory.Adapter{},
	}
	alloc := side.alloc
	side.mgr = newSideWithNetwork(t, name, events, alloc, func(roomID string) *memory.Adapter {
		a := memory.New()
		side.adapters[roomID] = a
		return a
	})
	return side
}

// newSideWithNetwork is newSide with the virtual network factory injected.
func newSideWithNetwork(t *testing.T, name string, events chan<- room.Notice,
	alloc *ipam.Allocator, makeAdapter func(roomID string) *memory.Adapter) *room.Manager {
	t.Helper()
	id, err := generateIdentity()
	if err != nil {
		t.Fatalf("%s: generate identity: %v", name, err)
	}
	m, err := room.NewManager(room.ManagerOptions{
		LocalID:     protocol.PeerID(id.id),
		LocalKey:    id.key,
		DisplayName: name,
		Factory:     webrtcFactory(),
		// No STUN: this test must not touch the network.
		STUNServers: []string{},
		MTU:         1200,
		Allocator:   alloc,
		NetworkFactory: func(_ context.Context, roomID string, tr transport.Transport,
			subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
			return network.NewService(network.ServiceConfig{
				RoomID:    roomID,
				Subnet:    subnet,
				Local:     local,
				IsHost:    isHost,
				Transport: tr,
				Adapter:   makeAdapter(roomID),
				Backend:   "memory",
				MTU:       network.DefaultMTU,
				Logger:    quietLogger(),
			})
		},
		Logger:  quietLogger(),
		OnEvent: func(n room.Notice) { events <- n },
	})
	if err != nil {
		t.Fatalf("%s: new room manager: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

// The handshake must leave both sides with an address, in one agreed subnet,
// without any extra negotiation. This is the whole of Phase 2's addressing
// design: the host allocates and names the address in the pairing code, and the
// guest adopts it.
func TestHandshakeAssignsAddressesBothSidesAgreeOn(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()

	host := newNetSide(t, "host", events)
	guest := newNetSide(t, "guest", events)

	created, err := host.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "net"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	joined, err := guest.mgr.Join(context.Background(), protocol.RoomJoinRequest{
		PairingCode: created.PairingCode,
		DisplayName: "guest",
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := host.mgr.Accept(context.Background(), protocol.RoomAcceptRequest{
		RoomID:     created.Room.RoomID,
		AnswerCode: joined.AnswerCode,
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}

	roomID := created.Room.RoomID
	hostStatus, err := host.mgr.NetworkStatus(context.Background(), roomID)
	if err != nil {
		t.Fatalf("host network status: %v", err)
	}
	guestStatus, err := guest.mgr.NetworkStatus(context.Background(), roomID)
	if err != nil {
		t.Fatalf("guest network status: %v", err)
	}

	// The subnet has to be identical on both sides. If it is not, the two
	// machines are addressing themselves onto different wires and nothing can
	// work, and the symptom would be a LAN that looks connected but carries
	// nothing.
	if hostStatus.Subnet != guestStatus.Subnet || hostStatus.Subnet == "" {
		t.Fatalf("subnets disagree: host %q, guest %q", hostStatus.Subnet, guestStatus.Subnet)
	}
	if hostStatus.LocalAddress != ipam.HostAddress(netip.MustParsePrefix(hostStatus.Subnet)).String() {
		t.Errorf("the host is at %s, want the first host address of %s",
			hostStatus.LocalAddress, hostStatus.Subnet)
	}
	if guestStatus.LocalAddress == hostStatus.LocalAddress {
		t.Errorf("the guest is at the host's address %s", guestStatus.LocalAddress)
	}
	if guestStatus.LocalAddress == "" {
		t.Fatal("the guest has no address: the pairing code carried no addressing")
	}

	// Both sides must see a live virtual LAN, not a merely-planned one.
	for name, st := range map[string]protocol.NetworkStatus{"host": hostStatus, "guest": guestStatus} {
		if st.Adapter != "memory" {
			t.Errorf("%s adapter = %q, want memory", name, st.Adapter)
		}
		if st.Note != "" {
			t.Errorf("%s reported a problem: %s", name, st.Note)
		}
	}

	// And the host must have learned the guest's address from the reservation it
	// made when it issued the code, without any further exchange.
	if _, err := host.mgr.Get(roomID); err != nil {
		t.Fatalf("the host's room vanished: %v", err)
	}
	waitFor(t, "the host to route to the guest", netReadyTimeout, func() bool {
		routes, err := host.mgr.NetworkRoutes(context.Background(), roomID)
		if err != nil {
			return false
		}
		return len(routes) == 2
	})
}

// A packet injected as if a game sent it must appear on the other machine's
// adapter. That is the end-to-end claim: the whole path works, not just the
// addressing.
func TestAPacketCrossesBetweenRealDaemons(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()

	host := newNetSide(t, "host", events)
	guest := newNetSide(t, "guest", events)

	created, err := host.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "net"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	joined, err := guest.mgr.Join(context.Background(), protocol.RoomJoinRequest{
		PairingCode: created.PairingCode,
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := host.mgr.Accept(context.Background(), protocol.RoomAcceptRequest{
		RoomID:     created.Room.RoomID,
		AnswerCode: joined.AnswerCode,
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	roomID := created.Room.RoomID

	// Wait until both sides have a running network and a routed peer. The link
	// comes up asynchronously after room.accept, so addressing it here is the
	// difference between a test and a race.
	hostStatus := waitForStatus(t, host, roomID, func(st protocol.NetworkStatus) bool {
		return len(st.Routes) == 2 && st.LocalAddress != ""
	})
	guestStatus := waitForStatus(t, guest, roomID, func(st protocol.NetworkStatus) bool {
		return st.LocalAddress != "" && st.State == string(network.StateReady)
	})

	// The link is up; the routing table still has to have caught up, which the
	// room reconciles on its maintenance tick.
	waitFor(t, "the guest to route to the host", netReadyTimeout, func() bool {
		routes, err := guest.mgr.NetworkRoutes(context.Background(), roomID)
		return err == nil && len(routes) == 2
	})

	msg := []byte("lanbaz-game-packet")
	host.adapters[roomID].Inject(network.Packet{
		Payload: udpPacket(hostStatus.LocalAddress, guestStatus.LocalAddress, msg),
	})

	got, err := guest.adapters[roomID].Delivered(10 * time.Second)
	if err != nil {
		t.Fatalf("the guest's stack received nothing: %v", err)
	}
	h, err := network.ParseIPv4(got.Payload)
	if err != nil {
		t.Fatalf("the guest received something that is not IPv4: %v", err)
	}
	if h.Src.String() != hostStatus.LocalAddress || h.Dst.String() != guestStatus.LocalAddress {
		t.Fatalf("packet %s -> %s, want %s -> %s",
			h.Src, h.Dst, hostStatus.LocalAddress, guestStatus.LocalAddress)
	}
	// Host to guest is a direct link, so the packet must arrive as it was sent.
	// Charging a hop for it would mean the host treated its own peer like a
	// relayed one, and the symptom would be games failing to reach a host they
	// are directly connected to.
	if h.TTL != 64 {
		t.Errorf("the packet arrived with ttl %d, want 64: a direct hop must not cost a hop", h.TTL)
	}

	// And the reply has to come back, because a unidirectional LAN is exactly
	// the failure a user cannot diagnose from either machine's UI.
	reply := []byte("and back again")
	guest.adapters[roomID].Inject(network.Packet{
		Payload: udpPacket(guestStatus.LocalAddress, hostStatus.LocalAddress, reply),
	})
	back, err := host.adapters[roomID].Delivered(10 * time.Second)
	if err != nil {
		t.Fatalf("the host received no reply: %v", err)
	}
	if h, err := network.ParseIPv4(back.Payload); err != nil || h.Src.String() != guestStatus.LocalAddress {
		t.Fatalf("the reply came from %v (err %v), want %s", h.Src, err, guestStatus.LocalAddress)
	}
}

// Two guests cannot reach each other directly - that is the whole reason the
// host is a hub - so their traffic goes through it. This is the claim that makes
// the virtual LAN worth having over a mesh: a guest's game sees an ordinary
// local network with the other players on it, and the relaying is invisible.
//
// It is also the only case where a hop is spent, and the only one where an
// address the host never allocated could have been invented, so it is where a
// mis-addressed guest would show up first.
func TestAGuestPacketRelaysThroughTheHostToAnotherGuest(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()

	host := newNetSide(t, "host", events)
	first := newNetSide(t, "first", events)
	second := newNetSide(t, "second", events)

	created, err := host.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "net"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	roomID := created.Room.RoomID
	// The host has one live code at a time, which is the same rule a user
	// follows: invite one player, then reissue the code for the next.
	code := created.PairingCode
	join := func(side *netSide, name string) protocol.NetworkStatus {
		t.Helper()
		joined, err := side.mgr.Join(context.Background(), protocol.RoomJoinRequest{
			PairingCode: code, DisplayName: name,
		})
		if err != nil {
			t.Fatalf("%s join: %v", name, err)
		}
		if err := host.mgr.Accept(context.Background(), protocol.RoomAcceptRequest{
			RoomID: roomID, AnswerCode: joined.AnswerCode,
		}); err != nil {
			t.Fatalf("%s accept: %v", name, err)
		}
		reissued, err := host.mgr.RegeneratePairing(context.Background(),
			protocol.RoomRegeneratePairingRequest{RoomID: roomID})
		if err != nil {
			t.Fatalf("%s regenerate: %v", name, err)
		}
		code = reissued.PairingCode
		return waitForStatus(t, side, roomID, func(st protocol.NetworkStatus) bool {
			return st.LocalAddress != "" && st.State == string(network.StateReady)
		})
	}
	firstStatus := join(first, "first")
	secondStatus := join(second, "second")

	// The host must know both guests, or it cannot relay between them.
	waitForStatus(t, host, roomID, func(st protocol.NetworkStatus) bool {
		return len(st.Routes) == 3
	})
	if firstStatus.LocalAddress == secondStatus.LocalAddress {
		t.Fatalf("both guests were given the address %s", firstStatus.LocalAddress)
	}

	// first -> second, which first has no route for. The host's relay is the
	// only thing that can carry it.
	first.adapters[roomID].Inject(network.Packet{
		Payload: udpPacket(firstStatus.LocalAddress, secondStatus.LocalAddress, []byte("lanbaz-relayed")),
	})
	got, err := second.adapters[roomID].Delivered(15 * time.Second)
	if err != nil {
		hub, _ := host.mgr.NetworkStatus(context.Background(), roomID)
		t.Fatalf("the second guest received nothing: %v\nhub peers %+v routes %+v\nfirst %+v\nsecond %+v",
			err, hub.Peers, hub.Routes, firstStatus, secondStatus)
	}
	h, err := network.ParseIPv4(got.Payload)
	if err != nil {
		t.Fatalf("the second guest received something that is not IPv4: %v", err)
	}
	if h.Src.String() != firstStatus.LocalAddress || h.Dst.String() != secondStatus.LocalAddress {
		t.Fatalf("packet %s -> %s, want %s -> %s",
			h.Src, h.Dst, firstStatus.LocalAddress, secondStatus.LocalAddress)
	}
	// Exactly one hop: the hub. A packet that lost two would be looping, and one
	// that lost none would mean it never went near the machine that relayed it.
	if h.TTL != 63 {
		t.Errorf("the relayed packet arrived with ttl %d, want 63: the hub must cost exactly one hop", h.TTL)
	}
}

// The room summary is what the UI and the CLI read. A peer row without an
// address is the one thing a user cannot work around, so the summary has to
// carry it.
func TestRoomSummaryCarriesTheAddresses(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()

	host := newNetSide(t, "host", events)
	guest := newNetSide(t, "guest", events)

	created, err := host.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "net"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	joined, err := guest.mgr.Join(context.Background(), protocol.RoomJoinRequest{PairingCode: created.PairingCode})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := host.mgr.Accept(context.Background(), protocol.RoomAcceptRequest{
		RoomID: created.Room.RoomID, AnswerCode: joined.AnswerCode,
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	roomID := created.Room.RoomID

	waitFor(t, "the host to route to the guest", netReadyTimeout, func() bool {
		routes, err := host.mgr.NetworkRoutes(context.Background(), roomID)
		return err == nil && len(routes) == 2
	})

	r, err := host.mgr.Get(roomID)
	if err != nil {
		t.Fatal(err)
	}
	summary := r.Summary()
	if summary.Subnet == "" || summary.LocalAddress == "" {
		t.Fatalf("the room summary carries no addressing: %+v", summary)
	}
	var withAddress int
	for _, p := range summary.Peers {
		if p.VirtualAddress != "" {
			withAddress++
		}
	}
	if withAddress == 0 {
		t.Errorf("no peer in the summary carries an address: %+v", summary.Peers)
	}
}

// Two rooms on one machine must not claim the same subnet. It is the failure the
// shared allocator exists to prevent, and it is invisible from inside either
// room.
func TestTwoRoomsOnOneMachineGetDifferentSubnets(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()

	side := newNetSide(t, "host", events)
	first, err := side.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "one"})
	if err != nil {
		t.Fatalf("create one: %v", err)
	}
	second, err := side.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "two"})
	if err != nil {
		t.Fatalf("create two: %v", err)
	}
	a, err := side.mgr.NetworkStatus(context.Background(), first.Room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := side.mgr.NetworkStatus(context.Background(), second.Room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Subnet == b.Subnet {
		t.Fatalf("two rooms on one machine share the subnet %s", a.Subnet)
	}
	// And the guest-address reservation for the first room must not have leaked
	// into the second.
	if a.LocalAddress == b.LocalAddress {
		t.Errorf("both rooms put this machine at %s", a.LocalAddress)
	}
	// Each room holds exactly one address it promised to somebody: the host's
	// own and the guest its live pairing code names. More would be a superseded
	// code's reservation leaking; none would mean the code promises an address
	// nobody owns, and two guests would end up sharing one.
	firstGuest := guestLeaseOf(t, side.alloc, first.Room.RoomID)
	secondGuest := guestLeaseOf(t, side.alloc, second.Room.RoomID)
	if firstGuest == secondGuest {
		t.Errorf("both rooms promise their guest the address %s", firstGuest)
	}
}

// guestLeaseOf returns the address a room has promised its pending guest.
func guestLeaseOf(t *testing.T, alloc *ipam.Allocator, roomID string) netip.Addr {
	t.Helper()
	leases := alloc.Leases(roomID)
	peers := 0
	var guest netip.Addr
	for _, l := range leases {
		if l.Role != "peer" {
			continue
		}
		peers++
		guest = l.Address
	}
	if peers != 1 {
		t.Fatalf("room %s holds %d guest leases, want 1: %+v", roomID, peers, leases)
	}
	return guest
}

// Each code reserves its own address, so a host can send codes to several
// friends at once. Reservations are bounded by the room's capacity: issuing
// more codes than the room has seats withdraws the oldest unanswered one
// instead of leaking addresses or refusing the new code.
func TestPairingReservationsAreBoundedByCapacity(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()

	side := newNetSide(t, "host", events)
	created, err := side.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "net", MaxPeers: 3})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := side.alloc.Stats().Leases; got != 1 {
		t.Fatalf("leases after issuing a code = %d, want 1", got)
	}
	for i := 0; i < 8; i++ {
		if _, err := side.mgr.RegeneratePairing(context.Background(),
			protocol.RoomRegeneratePairingRequest{RoomID: created.Room.RoomID}); err != nil {
			t.Fatalf("regenerate %d: %v", i, err)
		}
	}
	if got := side.alloc.Stats().Leases; got != 3 {
		t.Errorf("leases after eight codes in a 3-seat room = %d, want 3", got)
	}
}

// A room with no virtual network at all still answers, because "why is there no
// LAN" is the question a user arrives with and an error would send them looking
// somewhere else.
func TestNetworkStatusWorksWithoutAVirtualNetwork(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()

	m, _ := newSide(t, "host", events)
	created, err := m.Create(context.Background(), protocol.RoomCreateRequest{Name: "no-net"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	st, err := m.NetworkStatus(context.Background(), created.Room.RoomID)
	if err != nil {
		t.Fatalf("network status on a room with no network: %v", err)
	}
	if st.State != string(network.StateStopped) {
		t.Errorf("state = %q, want stopped", st.State)
	}
	// The addressing is still known, because it is derived from the room id and
	// not from the adapter. That is what lets the UI explain what *would* be
	// configured.
	if st.Subnet == "" {
		t.Error("the room knows its id but reports no subnet")
	}
	if st.Note == "" {
		t.Error("no note explains why there is no virtual network")
	}
	if len(st.Peers) != 0 || len(st.Routes) != 0 {
		t.Errorf("a room with no network reported %d peers and %d routes", len(st.Peers), len(st.Routes))
	}
}
