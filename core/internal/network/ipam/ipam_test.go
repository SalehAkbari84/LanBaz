package ipam

import (
	"net/netip"
	"testing"
)

func TestSubnetDerivationIsDeterministic(t *testing.T) {
	// The whole serverless handshake rests on this: a machine holding only a
	// pairing code must be able to compute the room's subnet without talking to
	// anybody. If the derivation were random, or depended on anything local, the
	// host would be 10.200.17.1 and the guest 10.200.18.2 and they would never
	// meet.
	a, b := New(Pool), New(Pool)
	for _, room := range []string{
		"lbzroom-0123456789ab", "lbzroom-zzzz", "", "a", "lbzroom-00000000",
	} {
		first, second := a.SubnetFor(room), b.SubnetFor(room)
		if first != second {
			t.Fatalf("room %q: subnet %s on one allocator, %s on another", room, first, second)
		}
		if !Pool.Contains(first.Addr()) {
			t.Fatalf("room %q: subnet %s is outside the pool", room, first)
		}
		if first.Bits() != SubnetBits {
			t.Fatalf("room %q: subnet %s is not a /%d", room, first, SubnetBits)
		}
	}
}

// A subnet that is not a real /24 would break every address calculation below
// it, so the derivation is pinned rather than trusted.
func TestDerivedSubnetsAreRealSlash24s(t *testing.T) {
	a := New(Pool)
	for _, room := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		p := a.SubnetFor(room)
		if got := p.Masked(); got != p {
			t.Errorf("room %q: subnet %s is not masked to its own boundary", room, p)
		}
		host := HostAddress(p)
		if !p.Contains(host) {
			t.Errorf("room %q: host address %s is outside %s", room, host, p)
		}
		if host == SubnetBroadcast(p) {
			t.Errorf("room %q: host address is the broadcast address", room)
		}
	}
}

// Reserve must be idempotent: the room calls it when it is created and again
// when it is reopened, and a second call that handed back a different subnet
// would silently move a live room.
func TestReserveIsIdempotent(t *testing.T) {
	a := New(Pool)
	first, probed, err := a.Reserve("lbzroom-x")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if probed {
		t.Error("the first reservation reported a collision")
	}
	second, probed, err := a.Reserve("lbzroom-x")
	if err != nil {
		t.Fatalf("reserve again: %v", err)
	}
	if second != first {
		t.Errorf("subnet changed on the second reservation: %s then %s", first, second)
	}
	if probed {
		t.Error("the second reservation reported a collision")
	}
}

func TestReserveNeedsARoomID(t *testing.T) {
	if _, _, err := New(Pool).Reserve(""); err == nil {
		t.Fatal("an empty room id was accepted")
	}
}

// Two rooms hashing to the same index must not be given the same subnet: both
// would claim the same host address and put two unrelated LANs on the same wire.
func TestReserveProbesPastACollision(t *testing.T) {
	a := New(Pool)
	// Find two room ids that hash to the same index.
	var collided bool
	byIndex := map[uint32]string{}
	for i := 0; i < 4096 && !collided; i++ {
		room := string(rune('a'+i%26)) + string(rune('A'+i/26))
		p := a.SubnetFor(room)
		idx := p.Addr().As4()[2]
		if prev, ok := byIndex[uint32(idx)]; ok {
			first, _, err := a.Reserve(prev)
			if err != nil {
				t.Fatalf("reserve %s: %v", prev, err)
			}
			second, probed, err := a.Reserve(room)
			if err != nil {
				t.Fatalf("reserve %s: %v", room, err)
			}
			if first == second {
				t.Fatalf("rooms %s and %s share the subnet %s", prev, room, first)
			}
			if !probed {
				t.Errorf("rooms %s and %s collided but the reservation did not report it", prev, room)
			}
			collided = true
			break
		}
		byIndex[uint32(idx)] = room
	}
	if !collided {
		t.Skip("no colliding room ids found in the searched range")
	}
}

// A guest does not choose its subnet: the host's pairing code names one and the
// guest adopts it. The code is authenticated, so the value can be trusted.
func TestAdoptRecordsTheSubnetsTheHostNamed(t *testing.T) {
	a := New(Pool)
	room := "lbzroom-guest"
	// The host names the subnet the room id derives to, which is the ordinary
	// case and must be reported as quiet.
	subnet := a.SubnetFor(room)
	probed, err := a.Adopt(room, subnet)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if probed {
		t.Error("adopting the derived subnet reported a probe")
	}
	got, ok := a.Subnet(room)
	if !ok || got != subnet {
		t.Fatalf("subnet = %s (ok=%v), want %s", got, ok, subnet)
	}
	// Adopting twice is what a guest does when it retries a join.
	if _, err := a.Adopt(room, subnet); err != nil {
		t.Fatalf("adopt again: %v", err)
	}
	// Adopting a *different* subnet for a room it already has would mean the
	// host disagreed with itself.
	if _, err := a.Adopt(room, netip.MustParsePrefix("10.200.45.0/24")); err == nil {
		t.Fatal("a room adopted a second, different subnet")
	}
}

// A subnet outside the pool cannot be routed to: every other machine in the
// room is addressing itself out of the pool, and the two would never meet.
func TestAdoptRefusesASubnetOutsideThePool(t *testing.T) {
	a := New(Pool)
	for _, p := range []string{"192.168.1.0/24", "10.201.1.0/24", "0.0.0.0/0"} {
		if _, err := a.Adopt("room", netip.MustParsePrefix(p)); err == nil {
			t.Errorf("%s was adopted but lies outside %s", p, Pool)
		}
	}
	if _, err := a.Adopt("", netip.MustParsePrefix("10.200.44.0/24")); err == nil {
		t.Error("an empty room id was accepted")
	}
}

// A guest that derives the subnet from the room id instead of reading the code
// would get this one wrong, so adopting it has to be reported as unusual.
func TestAdoptReportsASubnetThatIsNotTheDerivedOne(t *testing.T) {
	a := New(Pool)
	room := "lbzroom-probe-me"
	derived := a.SubnetFor(room)
	other := netip.MustParsePrefix("10.200.44.0/24")
	if other == derived {
		t.Skip("the derived subnet already happens to be the test subnet")
	}
	probed, err := a.Adopt(room, other)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !probed {
		t.Error("adopting a subnet other than the derived one did not report it")
	}
	// Adopting the derived one is the ordinary case and must be quiet.
	probed, err = a.Adopt("lbzroom-quiet", a.SubnetFor("lbzroom-quiet"))
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if probed {
		t.Error("adopting the derived subnet reported a probe")
	}
}

// A guest adopting a subnet a local room already holds would put two unrelated
// LANs on one wire. It has to be refused rather than tolerated.
func TestAdoptRefusesASubnetAnotherLocalRoomUses(t *testing.T) {
	a := New(Pool)
	subnet := netip.MustParsePrefix("10.200.44.0/24")
	if _, _, err := a.Reserve("room-one"); err != nil {
		t.Fatal(err)
	}
	// Claim the subnet explicitly so the test controls which room owns it.
	a.mu.Lock()
	a.subnets["room-one"] = subnet
	a.owners[subnet] = "room-one"
	a.mu.Unlock()

	if _, err := a.Adopt("room-two", subnet); err == nil {
		t.Fatal("two rooms on one machine share a subnet")
	}
}

func TestAllocateHandsOutDistinctAddresses(t *testing.T) {
	a := New(Pool)
	if _, _, err := a.Reserve("room"); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < MaxGuests; i++ {
		peer := "peer-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		addr, err := a.Allocate("room", peer)
		if err != nil {
			t.Fatalf("allocate %s: %v", peer, err)
		}
		if seen[addr.String()] {
			t.Fatalf("address %s was handed out twice", addr)
		}
		seen[addr.String()] = true
		if addr == HostAddress(a.SubnetFor("room")) {
			t.Errorf("peer %s got the host's own address %s", peer, addr)
		}
		if addr == netip.MustParseAddr("10.200.0.255") {
			t.Error("a peer was given an address outside its own subnet")
		}
	}
	// The pool for one room is exactly MaxGuests addresses wide; the next one
	// must fail rather than hand out the subnet's own broadcast address.
	if _, err := a.Allocate("room", "one-too-many"); err == nil {
		t.Fatal("the room was given more addresses than its subnet has")
	}
}

// Allocation is idempotent per peer. A peer that announces itself twice must
// keep one address, or the first lease is orphaned and its traffic disappears.
func TestAllocateIsIdempotentForAPeer(t *testing.T) {
	a := New(Pool)
	if _, _, err := a.Reserve("room"); err != nil {
		t.Fatal(err)
	}
	first, err := a.Allocate("room", "peer")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Allocate("room", "peer")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("the same peer got %s then %s", first, second)
	}
}

func TestAllocateRequiresAReservedRoom(t *testing.T) {
	a := New(Pool)
	if _, err := a.Allocate("no-such-room", "peer"); err == nil {
		t.Fatal("an address was allocated for a room with no subnet")
	}
	if _, err := a.Allocate("no-such-room", ""); err == nil {
		t.Fatal("an address was allocated to an empty peer id")
	}
}

// A released address must come back, and it should not be the very next one a
// departing peer held - otherwise a peer that reconnects keeps trading its old
// address with whoever took it.
func TestReleaseReturnsTheAddressAndAdvances(t *testing.T) {
	a := New(Pool)
	if _, _, err := a.Reserve("room"); err != nil {
		t.Fatal(err)
	}
	first, _ := a.Allocate("room", "a")
	second, _ := a.Allocate("room", "b")
	if first == second {
		t.Fatal("two peers got the same address")
	}

	a.Release("room", "a")
	if _, held := a.PeerAddress("room", "a"); held {
		t.Error("the released peer still holds an address")
	}
	if _, still := a.Lookup("room", first); still {
		t.Error("the released address is still in the table")
	}
	// The other peer must be untouched.
	if _, held := a.PeerAddress("room", "b"); !held {
		t.Error("releasing one peer released another")
	}
	// Releasing twice is what a goodbye followed by a liveness timeout looks
	// like, and it must not be an error.
	a.Release("room", "a")
	a.Release("room", "nobody")
}

func TestReleaseRoomForgetsEverything(t *testing.T) {
	a := New(Pool)
	if _, _, err := a.Reserve("room"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Allocate("room", "peer"); err != nil {
		t.Fatal(err)
	}
	a.ReleaseRoom("room")
	if _, ok := a.Subnet("room"); ok {
		t.Error("the room still holds a subnet after being released")
	}
	if got := a.Stats().Leases; got != 0 {
		t.Errorf("leases = %d after releasing the room, want 0", got)
	}
	// Releasing an unknown room is not an error.
	a.ReleaseRoom("never-existed")
}

func TestLookupFindsTheHolder(t *testing.T) {
	a := New(Pool)
	if _, _, err := a.Reserve("room"); err != nil {
		t.Fatal(err)
	}
	addr, err := a.Allocate("room", "peer")
	if err != nil {
		t.Fatal(err)
	}
	who, ok := a.Lookup("room", addr)
	if !ok || who != "peer" {
		t.Errorf("lookup returned %q (ok=%v), want peer", who, ok)
	}
	if _, ok := a.Lookup("room", netip.MustParseAddr("10.200.17.200")); ok {
		t.Error("an unallocated address resolved to a peer")
	}
	if _, ok := a.Lookup("no-such-room", addr); ok {
		t.Error("a lookup in an unknown room resolved")
	}
}

func TestLeasesListsTheHostFirstAndIsOrdered(t *testing.T) {
	a := New(Pool)
	if _, _, err := a.Reserve("room"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := a.Allocate("room", "peer"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	leases := a.Leases("room")
	if len(leases) != 4 {
		t.Fatalf("got %d leases, want 4 (host plus three peers)", len(leases))
	}
	if leases[0].Role != "host" {
		t.Errorf("first lease role = %q, want host", leases[0].Role)
	}
	for i := 1; i < len(leases); i++ {
		if leases[i-1].Address.Compare(leases[i].Address) > 0 {
			t.Fatalf("leases are not ordered by address: %s came before %s",
				leases[i-1].Address, leases[i].Address)
		}
	}
	if got := a.Leases("no-such-room"); got != nil {
		t.Errorf("leases for an unknown room = %v, want nil", got)
	}
}

// A custom pool is what a deployment would use to move LanBaz off a range that
// collides with a real network, so New has to honour one.
func TestNewHonoursACustomPool(t *testing.T) {
	moved := netip.MustParsePrefix("10.201.0.0/16")
	if got := New(moved).Pool(); got != moved {
		t.Errorf("pool = %s, want %s", got, moved)
	}
	a := New(moved)
	subnet, _, err := a.Reserve("room")
	if err != nil {
		t.Fatal(err)
	}
	if !moved.Contains(subnet.Addr()) {
		t.Errorf("room subnet %s is outside the custom pool %s", subnet, moved)
	}
	// Anything that is not a /16 falls back to the default rather than producing
	// an allocator that hands out nonsense addresses. A /24 in particular cannot
	// work: the index space is eight bits wide, so 256 rooms could never fit.
	for _, bad := range []string{"", "10.0.0.0/8", "10.200.0.0/24", "not-a-prefix"} {
		var p netip.Prefix
		if bad != "" {
			p, _ = netip.ParsePrefix(bad)
		}
		if got := New(p).Pool(); got != Pool {
			t.Errorf("New(%q) pool = %s, want the default %s", bad, got, Pool)
		}
	}
}

func TestStatsCountRoomsAndLeases(t *testing.T) {
	a := New(Pool)
	if _, _, err := a.Reserve("room-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Reserve("room-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Allocate("room-a", "p1"); err != nil {
		t.Fatal(err)
	}
	s := a.Stats()
	if s.Rooms != 2 {
		t.Errorf("rooms = %d, want 2", s.Rooms)
	}
	if s.Leases != 1 {
		t.Errorf("leases = %d, want 1", s.Leases)
	}
	if s.Pool != Pool {
		t.Errorf("pool = %s, want %s", s.Pool, Pool)
	}
	if got := len(a.Subnets()); got != 2 {
		t.Errorf("subnets() returned %d entries, want 2", got)
	}
}

func TestDescribe(t *testing.T) {
	if got, want := Describe(netip.MustParsePrefix("10.200.17.0/24")), "10.200.17.0/24"; got != want {
		t.Errorf("Describe = %q, want %q", got, want)
	}
}

// The host address is fixed at .1 so a guest can find the hub without being
// told. Both the router and the allocator rely on that.
func TestHostAddressIsTheFirstHostAddress(t *testing.T) {
	subnet := netip.MustParsePrefix("10.200.17.0/24")
	if got, want := HostAddress(subnet).String(), "10.200.17.1"; got != want {
		t.Fatalf("host address = %s, want %s", got, want)
	}
	if got, want := HostOffset, 1; got != want {
		t.Fatalf("host offset = %d, want %d", got, want)
	}
	if FirstGuestOffset != HostOffset+1 {
		t.Errorf("the first guest offset %d does not follow the host at %d", FirstGuestOffset, HostOffset)
	}
}

// A friend keeps the same address in a network across sessions.
func TestPreferredAddressIsStable(t *testing.T) {
	subnet := netip.MustParsePrefix("10.200.17.0/24")
	a1, a2 := PreferredFor(subnet, "friend-key"), PreferredFor(subnet, "friend-key")
	if a1 != a2 || !subnet.Contains(a1) || a1 == HostAddress(subnet) {
		t.Fatalf("preferred %v / %v", a1, a2)
	}
	if PreferredFor(subnet, "").IsValid() {
		t.Fatal("no identity must give no preference")
	}

	for session := 0; session < 2; session++ {
		a := New(Pool)
		if _, err := a.Adopt("room", subnet); err != nil {
			t.Fatal(err)
		}
		// A different link id each session, the same identity.
		got, err := a.AllocatePreferred("room", "link-"+string(rune('a'+session)), a1)
		if err != nil || got != a1 {
			t.Fatalf("session %d got %v, want %v (%v)", session, got, a1, err)
		}
	}

	// Taken by somebody else: falls back to another free address.
	a := New(Pool)
	_, _ = a.Adopt("room", subnet)
	_, _ = a.AllocatePreferred("room", "other", a1)
	got, err := a.AllocatePreferred("room", "me", a1)
	if err != nil || got == a1 || !subnet.Contains(got) {
		t.Fatalf("fallback got %v (%v)", got, err)
	}
	// Outside the subnet or the host's own address is never handed out.
	for _, bad := range []netip.Addr{netip.MustParseAddr("10.200.99.5"), HostAddress(subnet)} {
		got, _ := a.AllocatePreferred("room", "x"+bad.String(), bad)
		if got == bad {
			t.Fatalf("handed out %v", bad)
		}
	}
}
