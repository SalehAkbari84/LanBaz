package network

import (
	"net/netip"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/transport"
)

// testRoom builds a router for 10.200.17.0/24. The host sits at .1 and a guest
// at .2, because that is the addressing the pairing code hands out; a guest that
// shared the host's address would be addressing itself on the host's behalf.
func testRoom(t *testing.T, isHost bool) *Router {
	t.Helper()
	subnet := netip.MustParsePrefix("10.200.17.0/24")
	local := addr(2)
	if isHost {
		local = HostAddressOf(subnet)
	}
	r, err := NewRouter(RouterConfig{
		RoomID: "lbzroom-test",
		Subnet: subnet,
		Local:  local,
		IsHost: isHost,
		MTU:    DefaultMTU,
	})
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	return r
}

func addr(n int) netip.Addr {
	return netip.AddrFrom4([4]byte{10, 200, 17, byte(n)})
}

// addPeer registers a peer from the string forms a test reads more naturally,
// and turns an unparseable address into the zero value so the router's own
// validation - not the test's - is what refuses it.
func addPeer(r *Router, id, a string) error {
	parsed, err := netip.ParseAddr(a)
	if err != nil {
		parsed = netip.Addr{}
	}
	return r.AddPeer(transport.PeerID(id), parsed)
}

func TestRouterRejectsAddressesItCannotOwn(t *testing.T) {
	r := testRoom(t, true)
	subnet := r.Subnet()

	// The subnet's own broadcast address is not an address a peer can hold: a
	// peer at .255 would receive every broadcast in the room and answer all of
	// them, which is a denial of service dressed as a lease.
	if err := addPeer(r, "peer", SubnetBroadcast(subnet).String()); err == nil {
		t.Error("the subnet broadcast address was accepted as a peer lease")
	}
	// Nor is the local address: a peer claiming it would intercept traffic
	// addressed to this machine.
	if err := addPeer(r, "peer", subnet.Addr().String()); err == nil {
		t.Error("the subnet's own network address was accepted as a peer lease")
	}
	if err := addPeer(r, "peer", "10.200.17.1"); err == nil && r.Local().String() != "10.200.17.1" {
		t.Error("the local address was accepted as a peer lease")
	}
	if err := addPeer(r, "peer", "10.201.99.5"); err == nil {
		t.Error("an address outside the room subnet was accepted")
	}
	if err := addPeer(r, "peer", "not-an-address"); err == nil {
		t.Error("an unparseable address was accepted")
	}
	if err := addPeer(r, "", "10.200.17.4"); err == nil {
		t.Error("an empty peer id was accepted")
	}
}

// Two peers at the same address would make routing ambiguous, and the loser
// would silently stop receiving traffic. It has to be refused loudly.
func TestRouterRefusesADuplicateAddress(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatalf("first peer: %v", err)
	}
	if err := addPeer(r, "b", "10.200.17.2"); err == nil {
		t.Fatal("two peers were given the same address")
	}
}

// A peer that reconnects after a daemon restart comes back with a fresh lease.
// The old address must stop routing into the peer rather than lingering.
func TestRouterReplacesAChangedAddress(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if err := addPeer(r, "a", "10.200.17.5"); err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if _, ok := r.PeerFor(addr(2)); ok {
		t.Error("the old address still routes to the peer")
	}
	if got, ok := r.PeerFor(addr(5)); !ok || got != "a" {
		t.Errorf("new address routes to %q (ok=%v), want a", got, ok)
	}
	if got := r.PeerCount(); got != 1 {
		t.Errorf("peer count = %d, want 1: the old entry was left behind", got)
	}
}

func TestRouterForwardsUnicastBetweenPeers(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatal(err)
	}
	if err := addPeer(r, "b", "10.200.17.3"); err != nil {
		t.Fatal(err)
	}
	d := r.Outbound(Packet{Payload: buildIPv4("10.200.17.1", "10.200.17.3", ProtocolUDP, 64, 8)})
	if d.Action != ActionForward {
		t.Fatalf("action = %v, want forward", d.Action)
	}
	if d.Target != "b" {
		t.Errorf("target = %q, want b", d.Target)
	}
}

func TestRouterDropsTrafficForAnUnknownAddress(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatal(err)
	}
	d := r.Outbound(Packet{Payload: buildIPv4("10.200.17.1", "10.200.17.99", ProtocolUDP, 64, 8)})
	if d.Action != ActionDrop {
		t.Fatalf("action = %v, want drop: nothing holds 10.200.17.99", d.Action)
	}
	// LanBaz must never become the default gateway. A packet for the internet
	// has to stay with the physical adapter, or a game that phones home would
	// send it into a tunnel that cannot carry it.
	d = r.Outbound(Packet{Payload: buildIPv4("10.200.17.1", "8.8.8.8", ProtocolUDP, 64, 8)})
	if d.Action != ActionDrop {
		t.Fatalf("action = %v, want drop: LanBaz is not a default gateway", d.Action)
	}
	if got := r.Metrics().Snapshot().NoRoute; got != 2 {
		t.Errorf("no-route counter = %d, want 2", got)
	}
}

// The single most important rule in the router: a packet's source address must
// be the one leased to the peer it arrived on. Without it, any guest could
// impersonate any other and every game on the far side would believe it.
func TestRouterRefusesASpoofedSourceAddress(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatal(err)
	}
	if err := addPeer(r, "b", "10.200.17.3"); err != nil {
		t.Fatal(err)
	}

	// Peer b sends a packet claiming to come from peer a.
	d := r.Inbound("b", Packet{Payload: buildIPv4("10.200.17.2", "10.200.17.1", ProtocolUDP, 64, 8)})
	if d.Action == ActionDeliver || d.Action == ActionForward {
		t.Fatalf("action = %v, want drop: b was allowed to speak as a", d.Action)
	}
	if got := r.Metrics().Snapshot().Spoofed; got != 1 {
		t.Errorf("spoof counter = %d, want 1", got)
	}

	// The same rule applies to the host's own address: nothing legitimate
	// arrives from a remote link claiming to be this machine.
	d = r.Inbound("b", Packet{Payload: buildIPv4("10.200.17.1", "10.200.17.3", ProtocolUDP, 64, 8)})
	if d.Action == ActionDeliver {
		t.Fatal("a remote peer was allowed to claim the local address")
	}
	if got := r.Metrics().Snapshot().Spoofed; got != 2 {
		t.Errorf("spoof counter = %d, want 2", got)
	}
}

func TestRouterRefusesAPeerItCannotAddress(t *testing.T) {
	r := testRoom(t, true)
	// A packet from a link the router holds no lease for has an unverifiable
	// source, so it is dropped rather than delivered.
	d := r.Inbound("stranger", Packet{Payload: buildIPv4("10.200.17.9", "10.200.17.1", ProtocolUDP, 64, 8)})
	if d.Action != ActionDrop {
		t.Fatalf("action = %v, want drop for an unaddressed peer", d.Action)
	}
}

// On the host, a broadcast from one guest must reach every other guest and the
// local stack, but never come back to its sender.
func TestRouterRelaysBroadcastAsTheHub(t *testing.T) {
	r := testRoom(t, true)
	for _, p := range []struct{ id, a string }{{"a", "10.200.17.2"}, {"b", "10.200.17.3"}} {
		if err := addPeer(r, p.id, p.a); err != nil {
			t.Fatal(err)
		}
	}
	for _, dst := range []string{"255.255.255.255", "10.200.17.255"} {
		d := r.Inbound("a", Packet{Payload: buildIPv4("10.200.17.2", dst, ProtocolUDP, 64, 8)})
		if d.Action != ActionRelay {
			t.Fatalf("%s: action = %v, want relay", dst, d.Action)
		}
		if d.Exclude != "a" {
			t.Errorf("%s: exclude = %q, want a: a broadcast must never echo to its sender", dst, d.Exclude)
		}
	}
}

// On a guest, a broadcast goes to the host and nowhere else. The host does the
// fan-out. This is what keeps the topology a star and the relay logic single.
func TestRouterSendsGuestBroadcastToTheHub(t *testing.T) {
	r := testRoom(t, false)
	if err := addPeer(r, "host", "10.200.17.1"); err != nil {
		t.Fatal(err)
	}
	if err := addPeer(r, "other", "10.200.17.3"); err != nil {
		t.Fatal(err)
	}
	d := r.Outbound(Packet{Payload: buildIPv4("10.200.17.2", "224.0.0.251", ProtocolUDP, 64, 8)})
	if d.Action != ActionForward {
		t.Fatalf("action = %v, want forward", d.Action)
	}
	if d.Target != "host" {
		t.Errorf("target = %q, want host: a guest has exactly one link to relay through", d.Target)
	}
}

// A guest whose broadcast arrives from the host is delivered locally and not
// relayed onward, because a guest has nobody to relay to.
func TestRouterDeliversGuestBroadcastLocally(t *testing.T) {
	r := testRoom(t, false)
	if err := addPeer(r, "host", "10.200.17.1"); err != nil {
		t.Fatal(err)
	}
	d := r.Inbound("host", Packet{Payload: buildIPv4("10.200.17.1", "224.0.0.251", ProtocolUDP, 64, 8)})
	if d.Action != ActionDeliver {
		t.Fatalf("action = %v, want deliver", d.Action)
	}
}

// A guest talking to another guest goes through the host, because the two have
// no direct link - and a guest cannot know the address belongs to anybody, so
// it must hand anything in the subnet to the hub rather than drop it.
func TestRouterRelaysUnicastToTheHubOnAGuest(t *testing.T) {
	r := testRoom(t, false)
	if err := addPeer(r, "host", "10.200.17.1"); err != nil {
		t.Fatal(err)
	}
	d := r.Outbound(Packet{Payload: buildIPv4("10.200.17.2", "10.200.17.7", ProtocolUDP, 64, 8)})
	if d.Action != ActionForward {
		t.Fatalf("action = %v, want forward", d.Action)
	}
	if d.Target != "host" {
		t.Errorf("target = %q, want host", d.Target)
	}
	// Even an address inside the subnet that nobody holds goes to the host: only
	// the host knows the full membership, so only the host may declare it
	// unroutable.
	d = r.Outbound(Packet{Payload: buildIPv4("10.200.17.2", "10.200.17.250", ProtocolUDP, 64, 8)})
	if d.Action != ActionForward || d.Target != "host" {
		t.Errorf("action = %v to %q, want forward to host", d.Action, d.Target)
	}
	// Outside the subnet there is nowhere to send it on any machine.
	d = r.Outbound(Packet{Payload: buildIPv4("10.200.17.2", "8.8.8.8", ProtocolUDP, 64, 8)})
	if d.Action != ActionDrop {
		t.Errorf("action = %v, want drop: LanBaz is not a gateway", d.Action)
	}
}

// Without the hub's address a guest has nowhere to relay, so the packet is
// dropped rather than sent to an arbitrary peer.
func TestRouterDropsGuestBroadcastWithNoHub(t *testing.T) {
	r := testRoom(t, false)
	d := r.Outbound(Packet{Payload: buildIPv4("10.200.17.2", "255.255.255.255", ProtocolUDP, 64, 8)})
	if d.Action != ActionDrop {
		t.Fatalf("action = %v, want drop: the hub link is not known", d.Action)
	}
}

func TestRouterDropsTrafficForTheLocalAddress(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatal(err)
	}
	// A game addressing this machine is already served by the local stack;
	// forwarding it would be a loop.
	d := r.Outbound(Packet{Payload: buildIPv4("10.200.17.2", "10.200.17.1", ProtocolUDP, 64, 8)})
	if d.Action != ActionDrop {
		t.Fatalf("action = %v, want drop", d.Action)
	}
}

func TestRouterRejectsMalformedAndOversizedPackets(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatal(err)
	}
	if d := r.Outbound(Packet{Payload: nil}); d.Action != ActionDrop {
		t.Error("an empty packet was not dropped")
	}
	if d := r.Outbound(Packet{Payload: []byte{0x45, 0x00}}); d.Action != ActionDrop {
		t.Error("a truncated packet was not dropped")
	}
	// A packet the transport cannot carry has to be dropped here rather than at
	// the send, where the failure would be a silent return value nobody reads.
	big := buildIPv4("10.200.17.1", "10.200.17.2", ProtocolUDP, 64, r.MTU()+64)
	if d := r.Outbound(Packet{Payload: big}); d.Action != ActionDrop {
		t.Error("an oversized packet was not dropped")
	}
	if got := r.Metrics().Snapshot().Malformed; got != 2 {
		t.Errorf("malformed counter = %d, want 2", got)
	}
	if got := r.Metrics().Snapshot().Oversized; got != 1 {
		t.Errorf("oversized counter = %d, want 1", got)
	}
}

func TestRouterRoutesAreOrderedAndManaged(t *testing.T) {
	r := testRoom(t, true)
	for _, p := range []struct{ id, a string }{
		{"c", "10.200.17.4"}, {"a", "10.200.17.2"}, {"b", "10.200.17.3"},
	} {
		if err := addPeer(r, p.id, p.a); err != nil {
			t.Fatal(err)
		}
	}
	routes := r.Routes()
	if len(routes) != 4 {
		t.Fatalf("got %d routes, want 4 (subnet plus three peers)", len(routes))
	}
	// A status payload that reshuffles on every call is unreadable, so the
	// order has to be the address order and nothing else.
	for i := 1; i < len(routes); i++ {
		if routes[i].Destination.Addr().Less(routes[i-1].Destination.Addr()) {
			t.Fatalf("routes are not ordered: %s came before %s",
				routes[i-1].Destination.Addr(), routes[i].Destination.Addr())
		}
	}
	for _, rt := range routes {
		if !rt.Managed {
			t.Errorf("route %s is not marked managed, so shutdown would leave it behind", rt.Destination)
		}
		if rt.Destination.Bits() == 0 {
			t.Error("a default route was produced: LanBaz must never claim default routing")
		}
	}
}

func TestRouterRemovePeerStopsRoutingToIt(t *testing.T) {
	r := testRoom(t, true)
	if err := addPeer(r, "a", "10.200.17.2"); err != nil {
		t.Fatal(err)
	}
	r.RemovePeer("a")
	if got := r.PeerCount(); got != 0 {
		t.Fatalf("peer count = %d, want 0", got)
	}
	d := r.Outbound(Packet{Payload: buildIPv4("10.200.17.1", "10.200.17.2", ProtocolUDP, 64, 8)})
	if d.Action != ActionDrop {
		t.Fatalf("action = %v, want drop after the peer was removed", d.Action)
	}
	// Removing twice is what a bye followed by a liveness timeout looks like,
	// and it must not be an error.
	r.RemovePeer("a")
	r.RemovePeer("nobody")
}

// One guest sending broadcast must not be able to make the host send an
// unbounded number of copies. This is the amplification guard from
// docs/networking.md, tested directly.
func TestBroadcastLimiterBoundsFanOut(t *testing.T) {
	now := time.Unix(1700000000, 0)
	clock := func() time.Time { return now }
	l := newBroadcastLimiter(10, 10, clock)
	src := addr(2)

	for i := 0; i < 10; i++ {
		if !l.allow(src, now) {
			t.Fatalf("packet %d was refused inside the burst allowance", i)
		}
	}
	for i := 0; i < 100; i++ {
		if l.allow(src, now) {
			t.Fatalf("packet %d was allowed after the burst was spent", 10+i)
		}
	}
	// Refilling is time-based, so a second later there is room again. A limiter
	// that never refills would break discovery permanently after one burst.
	now = now.Add(time.Second)
	if !l.allow(src, now) {
		t.Fatal("the bucket did not refill after a second")
	}
}

// The budget must be per peer. A shared one would let one noisy guest consume
// the whole room's allowance and then silence everybody else, including a
// legitimate game - a denial of service dressed up as a fix.
func TestBroadcastLimiterIsolatesPeers(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newBroadcastLimiter(2, 2, func() time.Time { return now })
	noisy, quiet := addr(2), addr(3)

	l.allow(noisy, now)
	l.allow(noisy, now)
	if l.allow(noisy, now) {
		t.Fatal("the noisy peer exceeded its own budget")
	}
	if !l.allow(quiet, now) {
		t.Fatal("the noisy peer consumed the quiet peer's budget")
	}
}

func TestBroadcastLimiterReclaimsIdleBuckets(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newBroadcastLimiter(10, 10, func() time.Time { return now })
	for i := 2; i < 40; i++ {
		l.allow(addr(i), now)
	}
	if got := l.Len(); got != 38 {
		t.Fatalf("bucket count = %d, want 38", got)
	}
	now = now.Add(2 * bucketTTL)
	l.allow(addr(2), now)
	if got := l.Len(); got != 1 {
		t.Errorf("bucket count = %d, want 1: idle buckets must be reclaimed", got)
	}
}

// A clock that jumps backwards must not take tokens away, which would lock a
// peer out until real time caught up. The assertion is that the bucket does not
// refill *and* does not go negative: a backwards jump must leave the peer exactly
// as throttled as it was.
func TestBroadcastLimiterToleratesAClockGoingBackwards(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newBroadcastLimiter(4, 4, func() time.Time { return now })
	for i := 0; i < 4; i++ {
		if !l.allow(addr(2), now) {
			t.Fatalf("packet %d was refused inside the burst allowance", i)
		}
	}
	now = now.Add(-time.Hour)
	if l.allow(addr(2), now) {
		t.Fatal("a backwards clock refilled an empty bucket")
	}
}

func TestDecrementTTLThroughTheRouter(t *testing.T) {
	r := testRoom(t, true)
	pkt := Packet{Payload: buildIPv4("10.200.17.2", "10.200.17.1", ProtocolUDP, 5, 0)}
	if !r.DecrementTTL(&pkt) {
		t.Fatal("a packet with ttl 5 was expired")
	}
	if got := pkt.Payload[8]; got != 4 {
		t.Errorf("ttl = %d, want 4", got)
	}
	pkt.Payload[8] = 1
	if r.DecrementTTL(&pkt) {
		t.Fatal("a packet with ttl 1 was forwarded")
	}
	if got := r.Metrics().Snapshot().Expired; got != 1 {
		t.Errorf("expired counter = %d, want 1", got)
	}
}

func TestNewRouterValidatesItsConfiguration(t *testing.T) {
	subnet := netip.MustParsePrefix("10.200.17.0/24")
	if _, err := NewRouter(RouterConfig{Subnet: netip.Prefix{}, Local: addr(1)}); err == nil {
		t.Error("an invalid subnet was accepted")
	}
	if _, err := NewRouter(RouterConfig{Subnet: subnet, Local: netip.Addr{}}); err == nil {
		t.Error("a missing local address was accepted")
	}
	if _, err := NewRouter(RouterConfig{Subnet: subnet, Local: netip.MustParseAddr("10.201.1.1")}); err == nil {
		t.Error("a local address outside the subnet was accepted")
	}
}

// The router's notion of the hub address must match the allocator's, or a guest
// would look for the hub where the host is not.
func TestHostAddressMatchesTheAllocator(t *testing.T) {
	subnet := netip.MustParsePrefix("10.200.17.0/24")
	if got, want := HostAddressOf(subnet).String(), ipamHostAddress(subnet).String(); got != want {
		t.Fatalf("router host address %s, allocator host address %s", got, want)
	}
}

func TestActionStrings(t *testing.T) {
	for a, want := range map[Action]string{
		ActionDrop:    "drop",
		ActionDeliver: "deliver",
		ActionForward: "forward",
		ActionRelay:   "relay",
	} {
		if got := a.String(); got != want {
			t.Errorf("Action(%d) = %q, want %q", a, got, want)
		}
	}
}

// ipamHostAddress is a local mirror of ipam.HostAddress, written out rather
// than imported so the network package keeps no dependency on the allocator.
// The comparison above is what keeps the two honest.
func ipamHostAddress(subnet netip.Prefix) netip.Addr { return HostAddr(subnet, 1) }
