// Package ipam hands out the addresses of the virtual LAN.
//
// # Why addressing cannot be local
//
// A virtual LAN is one address space shared by several machines that never meet
// before the game starts. Each of them must independently arrive at the same
// answer to "what is my address" without a server to ask, which means the
// address cannot be a function of join order, of the local machine, or of
// anything the machine cannot recompute from what it was told.
//
// LanBaz makes it a function of the room id. The room id is in the pairing
// code, the pairing code is authenticated by the room secret, and the secret is
// in the same code - so a machine that can decode a pairing code knows the room
// id without trusting anything outside it. Hashing the id into a subnet index
// therefore gives every participant the same answer with no negotiation, no
// extra round trip and no state that could drift.
//
// # What the host still owns
//
// Hashing decides the subnet, not the addresses inside it. The host is the only
// participant that knows the full membership, so it is the only one that can
// hand out a unique address within that subnet; see Allocator.Allocate.
package ipam

import (
	"fmt"
	"hash/fnv"
	"net/netip"
	"sync"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// The address space.
//
// 10.200.0.0/16 is chosen because it is inside RFC 1918 space, is not commonly
// used by consumer routers (192.168.x is where home networks live), and is
// short enough that a room subnet reads clearly in a game's server list:
// 10.200.17.1 rather than something with a hex tail.
const (
	// PoolBits is the size of the pool a room subnet is drawn from.
	PoolBits = 16
	// SubnetBits is the size of one room's subnet.
	SubnetBits = 24
	// HostOffset is the address the room host always takes.
	//
	// The host being first rather than last is deliberate: a game that lists
	// players by address shows 10.200.17.1 as the room and .2, .3, .4 as guests,
	// which matches how the host describes itself in the UI.
	HostOffset = 1
	// FirstGuestOffset is the lowest address a peer may take.
	FirstGuestOffset = 2
	// LastGuestOffset is the highest address a peer may take. .255 is the
	// subnet's broadcast address and is never assigned.
	LastGuestOffset = 254
	// MaxGuests is how many peers one /24 can address.
	MaxGuests = LastGuestOffset - FirstGuestOffset + 1
)

// Pool is the address space LanBaz draws from.
var Pool = netip.MustParsePrefix("10.200.0.0/16")

// Allocator owns the addresses of every room on this machine.
//
// One allocator per daemon, not one per room: the thing that must not happen is
// two rooms on the same machine picking the same subnet, and only a shared
// piece of state can prevent that. A per-room allocator would make each one
// independently believe it owns 10.200.17.0/24.
type Allocator struct {
	// base is the pool. It is a field rather than a constant so a test can use
	// a small pool, and so a future configuration can move it.
	base netip.Prefix

	mu sync.Mutex
	// subnets maps a room id to the subnet that room uses.
	subnets map[string]netip.Prefix
	// owners maps a subnet to the room that claimed it, so a collision can be
	// reported instead of silently producing two identical networks.
	owners map[netip.Prefix]string
	// taken records, per subnet, which addresses are leased and to whom.
	taken map[netip.Prefix]*leaseTable
	// probes counts how often a hashed index collided, which is the signal that
	// the pool is too small for the number of rooms.
	probes int
}

// leaseTable is one subnet's address book.
type leaseTable struct {
	// byAddress maps a leased address to the peer holding it.
	byAddress map[netip.Addr]string
	// byPeer maps a peer to its address, so Allocate is idempotent for a peer
	// that asks twice.
	byPeer map[string]netip.Addr
	// next is the last offset handed out. Allocation continues from there rather
	// than always returning the lowest free address, which stops two peers that
	// disconnect and reconnect from trading the same address forever.
	next int
}

// New builds an allocator over the given pool. An empty or invalid pool selects
// the default.
func New(base netip.Prefix) *Allocator {
	if !base.IsValid() || !base.Addr().Is4() || base.Bits() != PoolBits {
		base = Pool
	}
	return &Allocator{
		base:    base,
		subnets: make(map[string]netip.Prefix),
		owners:  make(map[netip.Prefix]string),
		taken:   make(map[netip.Prefix]*leaseTable),
	}
}

// Pool returns the address space this allocator draws from.
func (a *Allocator) Pool() netip.Prefix { return a.base }

// SubnetFor derives a room's subnet from its id, without claiming it.
//
// The derivation is pure, so every machine holding a pairing code for a room
// computes the same subnet without talking to anybody. It is the answer Reserve
// gives when nothing is in the way; callers that need the claim use Reserve,
// which returns the same value unless the local machine already gave it away.
func (a *Allocator) SubnetFor(roomID string) netip.Prefix {
	return a.prefixAt(a.subnetIndex(roomID))
}

// subnetIndex returns the /24 index a room id hashes to.
func (a *Allocator) subnetIndex(roomID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(roomID))
	return h.Sum32() & 0xff
}

// prefixAt builds the /24 at the given index within the pool.
func (a *Allocator) prefixAt(index uint32) netip.Prefix {
	base := a.base.Masked().Addr()
	b := base.As4()
	b[2] = byte(index)
	return netip.PrefixFrom(netip.AddrFrom4(b), SubnetBits)
}

// Reserve claims a subnet for a room and returns it.
//
// The first claim for a room takes the deterministic subnet the room id hashes
// to. A later claim - a second room whose id collides - takes the first free
// subnet after it, and the caller is told whether a probe happened so the room
// can be given a subnet both sides will agree on.
//
// The agreed-on part matters: a subnet that only the host knows about produces
// a host that addresses itself 10.200.17.1 and a guest that believes it is
// 10.200.18.2. The two never meet. So the chosen subnet is put in the pairing
// code by the caller, and this function is only ever called with rooms whose id
// both sides share.
func (a *Allocator) Reserve(roomID string) (netip.Prefix, bool, error) {
	if roomID == "" {
		return netip.Prefix{}, false, protocol.NewError(protocol.CodeConfigInvalid,
			"ipam: a room id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if p, ok := a.subnets[roomID]; ok {
		return p, false, nil
	}
	start := a.subnetIndex(roomID)
	for i := uint32(0); i < 256; i++ {
		idx := (start + i) & 0xff
		p := a.prefixAt(idx)
		owner, taken := a.owners[p]
		if taken && owner == roomID {
			a.subnets[roomID] = p
			return p, i > 0, nil
		}
		if !taken {
			a.subnets[roomID] = p
			a.owners[p] = roomID
			return p, i > 0, nil
		}
		a.probes++
	}
	return netip.Prefix{}, false, protocol.NewErrorf(protocol.CodeIPAMExhausted,
		"ipam: no free subnet left in %s for room %s", a.base, roomID)
}

// Adopt records a subnet that the room's pairing code named.
//
// The guest does not choose its subnet: the host's code carries one, and this
// records it. The address is checked against the pool and against every other
// room on this machine, because a guest adopting a subnet a local room already
// uses would put two unrelated LANs on the same wire.
//
// The boolean reports whether the named subnet differs from the one the room id
// hashes to. That is worth knowing: it means the host had to probe around a
// local collision, so anything that derived the subnet from the room id rather
// than from the code would pick the wrong one, and the room is a good place to
// say so out loud.
func (a *Allocator) Adopt(roomID string, subnet netip.Prefix) (bool, error) {
	if roomID == "" {
		return false, protocol.NewError(protocol.CodeConfigInvalid,
			"ipam: a room id is required")
	}
	subnet = subnet.Masked()
	if !a.base.Contains(subnet.Addr()) {
		return false, protocol.NewErrorf(protocol.CodeConfigInvalid,
			"ipam: %s is outside the LanBaz pool %s", subnet, a.base)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if p, ok := a.subnets[roomID]; ok {
		if p != subnet {
			return false, protocol.NewErrorf(protocol.CodePairingInvalid,
				"ipam: room %s already uses %s, the code names %s", roomID, p, subnet)
		}
		return p != a.prefixAt(a.subnetIndex(roomID)), nil
	}
	if owner, taken := a.owners[subnet]; taken && owner != roomID {
		// Two rooms on one machine sharing a subnet is not a warning, it is a
		// broken network: both would claim the same host address. It can only
		// happen when a user joins the same room twice under two ids.
		return false, protocol.NewErrorf(protocol.CodeIPAMExhausted,
			"ipam: %s is already used by room %s on this machine", subnet, owner)
	}
	a.subnets[roomID] = subnet
	a.owners[subnet] = roomID
	return subnet != a.prefixAt(a.subnetIndex(roomID)), nil
}

// Allocate leases the next free address in a room's subnet to a peer.
//
// It is idempotent: a peer that already holds a lease gets the same address
// back. That is not a nicety - a peer announces itself over the control channel
// and may do so more than once, and handing out a second address would leave
// the first leased to nobody reachable.
func (a *Allocator) Allocate(roomID, peerID string) (netip.Addr, error) {
	if peerID == "" {
		return netip.Addr{}, protocol.NewError(protocol.CodeConfigInvalid,
			"ipam: a peer id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	subnet, ok := a.subnets[roomID]
	if !ok {
		return netip.Addr{}, protocol.NewErrorf(protocol.CodeConfigInvalid,
			"ipam: room %s has no subnet reserved", roomID)
	}
	table, ok := a.taken[subnet]
	if !ok {
		table = &leaseTable{
			byAddress: make(map[netip.Addr]string),
			byPeer:    make(map[string]netip.Addr),
		}
		a.taken[subnet] = table
	}
	if addr, held := table.byPeer[peerID]; held {
		return addr, nil
	}
	for i := 0; i < MaxGuests; i++ {
		off := FirstGuestOffset + ((table.next + i) % MaxGuests)
		addr := hostAt(subnet, off)
		if addr.IsValid() && addr != HostAddress(subnet) {
			if _, used := table.byAddress[addr]; !used {
				table.byAddress[addr] = peerID
				table.byPeer[peerID] = addr
				table.next = off + 1
				return addr, nil
			}
		}
	}
	return netip.Addr{}, protocol.NewErrorf(protocol.CodeIPAMExhausted,
		"ipam: room %s has no free address left in %s", roomID, subnet)
}

// Release returns a peer's address to the room's pool.
//
// Leases are per room and in memory only. A crashed daemon therefore cannot
// leak addresses across a restart, which is the failure mode a persistent
// allocator would have: after enough crashes the pool would be exhausted by
// addresses belonging to processes that no longer exist.
func (a *Allocator) Release(roomID, peerID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	subnet, ok := a.subnets[roomID]
	if !ok {
		return
	}
	table, ok := a.taken[subnet]
	if !ok {
		return
	}
	addr, held := table.byPeer[peerID]
	if !held {
		return
	}
	delete(table.byPeer, peerID)
	delete(table.byAddress, addr)
	if len(table.byPeer) == 0 {
		delete(a.taken, subnet)
	}
}

// ReleaseRoom forgets a room entirely, including its subnet claim.
func (a *Allocator) ReleaseRoom(roomID string) {
	a.mu.Lock()
	subnet, ok := a.subnets[roomID]
	delete(a.subnets, roomID)
	if ok {
		delete(a.owners, subnet)
		delete(a.taken, subnet)
	}
	a.mu.Unlock()
}

// Lookup returns the peer holding an address in a room's subnet.
func (a *Allocator) Lookup(roomID string, addr netip.Addr) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	subnet, ok := a.subnets[roomID]
	if !ok {
		return "", false
	}
	table, ok := a.taken[subnet]
	if !ok {
		return "", false
	}
	peer, ok := table.byAddress[addr]
	return peer, ok
}

// PeerAddress returns the address leased to a peer, if any.
func (a *Allocator) PeerAddress(roomID, peerID string) (netip.Addr, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	subnet, ok := a.subnets[roomID]
	if !ok {
		return netip.Addr{}, false
	}
	table, ok := a.taken[subnet]
	if !ok {
		return netip.Addr{}, false
	}
	addr, ok := table.byPeer[peerID]
	return addr, ok
}

// Subnet returns the subnet a room uses.
func (a *Allocator) Subnet(roomID string) (netip.Prefix, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.subnets[roomID]
	return p, ok
}

// Leases returns every lease in a room's subnet, ordered by address.
//
// It exists for network.status, and it returns a copy so a caller rendering the
// status cannot hold the allocator's lock while formatting.
func (a *Allocator) Leases(roomID string) []Lease {
	a.mu.Lock()
	subnet, ok := a.subnets[roomID]
	if !ok {
		a.mu.Unlock()
		return nil
	}
	table, ok := a.taken[subnet]
	if !ok {
		a.mu.Unlock()
		return nil
	}
	out := make([]Lease, 0, len(table.byPeer)+1)
	if addr := HostAddress(subnet); addr.IsValid() {
		out = append(out, Lease{Address: addr, Role: "host"})
	}
	for addr, peer := range table.byAddress {
		out = append(out, Lease{Address: addr, PeerID: peer, Role: "peer"})
	}
	a.mu.Unlock()
	sortLeases(out)
	return out
}

// Subnets returns every room subnet currently claimed.
func (a *Allocator) Subnets() map[string]netip.Prefix {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]netip.Prefix, len(a.subnets))
	for id, p := range a.subnets {
		out[id] = p
	}
	return out
}

// Stats reports allocator counters for diagnostics.
func (a *Allocator) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	rooms, leases := len(a.subnets), 0
	for _, t := range a.taken {
		leases += len(t.byPeer)
	}
	return Stats{Rooms: rooms, Leases: leases, Collisions: a.probes, Pool: a.base}
}

// Stats is a snapshot of allocator state.
type Stats struct {
	Rooms int
	// Leases is the number of peer addresses handed out across all rooms.
	Leases int
	// Collisions counts subnet index collisions observed while reserving.
	Collisions int
	Pool       netip.Prefix
}

// Lease is one address assignment.
type Lease struct {
	Address netip.Addr
	// PeerID is empty for the host's own address.
	PeerID string
	// Role is "host" or "peer".
	Role string
}

// HostAddress returns the room host's address: the first host address of the
// subnet. It is fixed so that a guest can find the host without being told.
func HostAddress(subnet netip.Prefix) netip.Addr { return hostAt(subnet, HostOffset) }

// SubnetBroadcast returns the subnet's directed broadcast address.
//
// The network package computes the same value for the router. The duplication
// is deliberate and small: the router must identify the hub and recognise a
// broadcast without importing the allocator, and the allocator must be able to
// answer without importing the router. One line of address arithmetic is a
// cheaper price than an import cycle between two packages that should be able to
// evolve separately; a test in each pins the result.
func SubnetBroadcast(subnet netip.Prefix) netip.Addr { return hostAt(subnet, 255) }

// hostAt returns the address n bytes past the subnet base.
func hostAt(subnet netip.Prefix, n int) netip.Addr {
	base := subnet.Masked().Addr()
	if !base.Is4() {
		return netip.Addr{}
	}
	b := base.As4()
	b[3] = byte(n)
	return netip.AddrFrom4(b)
}

// sortLeases orders leases by address so a status payload is stable between
// calls. A UI that reshuffles its list on every poll is unreadable.
func sortLeases(ls []Lease) {
	for i := 1; i < len(ls); i++ {
		for j := i; j > 0 && ls[j].Address.Less(ls[j-1].Address); j-- {
			ls[j], ls[j-1] = ls[j-1], ls[j]
		}
	}
}

// Describe renders a subnet the way a user reads it.
func Describe(subnet netip.Prefix) string { return fmt.Sprintf("%s/%d", subnet.Addr(), subnet.Bits()) }
