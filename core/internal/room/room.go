// Package room owns LanBaz rooms: the set of peers that may see each other, the
// pairing code that admits them, and the transport that connects them.
//
// # The serverless handshake
//
// LanBaz has no room server in the common case, so a join is a conversation
// carried by a human. The host calls room.create, which produces a pairing code
// containing an SDP offer. The guest pastes it, which produces an answer code.
// The guest hands the answer back, and the host calls room.accept.
//
// The awkward part - and the reason this is a package rather than three handlers
// - is that both descriptions must pass through a human being, so the offer has
// to be produced *before* the guest exists and the answer applied *after* the
// guest has gone away. That means the host holds state across a gap of minutes
// with no live connection to justify it, and it means a guest is briefly a room
// member whose link is not up yet. Both are represented here explicitly rather
// than papered over.
//
// # Why rooms are single-shot
//
// A pairing code is redeemable once. That is the whole of LanBaz's replay
// protection and it is what lets the code travel over a chat window or be read
// aloud: observing a code once buys an attacker nothing that re-reading it does
// not. Regenerating a code is cheap and room.regenerate_pairing exists so a user
// who suspects exposure can reissue without disturbing the room.
package room

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Defaults for a new room.
const (
	// DefaultMaxPeers bounds a room. Radmin rooms are effectively unlimited, but
	// a hard ceiling is what stops one guest from looping room.join and turning
	// the host into a relay for itself.
	DefaultMaxPeers = 8
	// DefaultPairingTTL is how long a generated code stays redeemable. Ten
	// minutes is long enough to read a code aloud, type it, and send the answer
	// back, and short enough that a code pasted into a chat log is a short-lived
	// liability.
	DefaultPairingTTL = 10 * time.Minute
	// MaxNameLen bounds a room name, in runes.
	MaxNameLen = 48
	// MaxPeersCeiling is the largest value room.create will accept, whatever
	// the request says.
	MaxPeersCeiling = 64
	// MaxTTL bounds a requested pairing lifetime.
	MaxTTL = time.Hour
)

// Room is one virtual LAN-in-waiting: its members, its transport and its
// outstanding pairing code.
type Room struct {
	id   string
	name string

	// owner is the local peer id that created the room.
	owner protocol.PeerID
	// isHost distinguishes a room this daemon hosts from one it joined. It is
	// not implied by owner: a guest also owns the room record locally, it just
	// is not the one issuing codes.
	isHost bool

	maxPeers    int
	gameProfile string
	createdAt   time.Time

	// secret is the room secret carried by every pairing code. Both sides
	// derive the same authentication key from it, which is what lets a code
	// authenticate its own contents without a server holding a key.
	secret string

	tr transport.Transport
	pm *peer.Manager

	log *slog.Logger
	now func() time.Time

	// pairing holds the currently issued code, if any.
	mu           sync.RWMutex
	pairing      *issued
	pairingSpent bool

	// pending maps a guest peer id to its answer, for the window between
	// room.join on the guest side and room.accept on the host side.
	pending map[transport.PeerID]*pendingJoin

	// The virtual LAN. See net.go for why a room owns one and why failing to
	// create it is not fatal.
	netMu          sync.RWMutex
	attachMu       sync.Mutex
	net            network.VirtualNetwork
	netErr         error
	allocator      *ipam.Allocator
	onNetworkEvent func(string, protocol.NetworkEvent)
	displayName    string
	// subnet and local are this room's address space and this machine's place in
	// it. They are set once, from the room id on the host and from the pairing
	// code on a guest, and never change afterwards.
	subnet netip.Prefix
	local  netip.Addr

	// leases maps a peer to its address. It is the room's own record, kept
	// alongside the routing table so a status payload can name peers and so a
	// peer that vanishes can be released without consulting the allocator.
	leasesMu sync.RWMutex
	leases   map[transport.PeerID]netip.Addr

	// mesh enables direct guest-to-guest links; meshSt is their bookkeeping.
	mesh    bool
	meshSt  *meshState
	nameRes bool
	// mode is "l3" (IP router) or "l2" (Ethernet switch on TAP).
	mode string
	// pres is what each player is running.
	pres    *presenceState
	signals signalState

	// app holds room-level messaging state (chat history, dedup, rate limits).
	app           *appState
	appHandlersMu sync.RWMutex
	appHandlers   map[string]func(*peer.Peer, peer.AppMessage)

	// onHostGone is called (on a guest) once the link to the host is gone
	// for good: the host said goodbye, kicked us, or the link failed.
	onHostGone func(reason string)

	closeOnce sync.Once
	done      chan struct{}
}

// HostAlive reports whether a guest room still has a usable link to its host.
func (r *Room) HostAlive() bool { return r.hostAlive() }

// hostAlive reports whether a guest room still has a usable link to its host.
func (r *Room) hostAlive() bool {
	for _, p := range r.pm.List() {
		if !p.IsHost {
			continue
		}
		switch p.State {
		case protocol.PeerFailed, protocol.PeerDisconnected:
		default:
			return true
		}
	}
	return false
}

// issued is a pairing code and its bookkeeping.
type issued struct {
	code      string
	uri       string
	expiresAt time.Time
	// guest is the reservation this code was issued for.
	guest transport.PeerID
}

// pendingJoin is a guest whose answer has not arrived yet.
//
// The guest is a room member from the moment its code is verified, but its link
// does not exist until the answer is applied. Counting it against capacity is
// correct: it will become a real peer or it will expire, and either way it must
// not be possible to fill a room with half-finished joins.
type pendingJoin struct {
	// peerID is the transport id reserved for the guest. It is minted when the
	// offer is produced and travels inside both pairing codes, so the host can
	// apply the answer to the right link even with several guests in flight.
	peerID transport.PeerID
	// secret is the secret carried by this guest's code. Each code has its own,
	// so several guests can hold live codes at once, and an answer is only
	// accepted with the secret of the code it answers.
	secret string
	// expiresAt is the expiry of the code this guest was given.
	expiresAt time.Time
	// addr is the address reserved alongside it. It is named in the pairing code,
	// so releasing it when a guest never answers is what keeps a room from
	// filling with leases nobody can reach.
	addr     netip.Addr
	joinedAt time.Time
}

// Options configures Create.
type Options struct {
	// RoomID identifies the room. Generated when empty.
	RoomID string
	Name   string
	// Owner is the local peer id, which is the room's identity.
	Owner protocol.PeerID
	// MaxPeers bounds the room; 0 selects DefaultMaxPeers.
	MaxPeers int
	// PairingTTL is the code lifetime; 0 selects DefaultPairingTTL.
	PairingTTL time.Duration
	// GameProfile names a profile under profiles/. Validated in Phase 5.
	GameProfile string
	// OwnerKey is the local Ed25519 private key in its 64-byte form. The room
	// never signs with it; it is passed straight to the transport and the peer
	// manager, which own every use of it, so there is exactly one hop between
	// the key and the code that could conceivably log it.
	OwnerKey []byte
	// STUNServers, MTU and AllowRelay are transport construction parameters.
	STUNServers []string
	TURNServers []transport.RelayServer
	MTU         int
	AllowRelay  bool
	RelayOnly   bool
	PortMin     uint16
	PortMax     uint16
	// Mesh lets guests link directly to each other.
	Mesh bool
	// NameResolution answers <name>.local for players.
	NameResolution bool
	// Mode is "l3" or "l2".
	Mode string
	// DisplayName is what this daemon is called in a room's peer list.
	DisplayName string
	// Subnet and LocalAddress are the room's address space and this machine's
	// place in it. When empty the subnet is derived from the room id and the
	// local address defaults to the room host's, which is what a host wants.
	Subnet       netip.Prefix
	LocalAddress netip.Addr
	// Transport is the already constructed room transport.
	Transport transport.Transport
	// Peers is the peer manager for this transport, already wired to it.
	Peers *peer.Manager
	// NetworkFactory builds the room's virtual LAN. Nil disables it, which is
	// what a build with no adapter backend wants.
	NetworkFactory NetworkFactory
	// Allocator hands out addresses. One per daemon, shared by every room: only
	// shared state can stop two rooms from claiming the same subnet.
	Allocator *ipam.Allocator
	// NetworkMTU bounds a single packet on the virtual adapter; 0 selects the
	// network package's default. It is distinct from MTU above, which bounds a
	// transport frame: the two can differ, and conflating them would make a
	// tuning knob mean two things at once.
	NetworkMTU int
	// OnNetworkEvent receives addressing changes for the UI.
	OnNetworkEvent func(string, protocol.NetworkEvent)
	Logger         *slog.Logger
	// Now is the clock; nil selects time.Now.
	Now func() time.Time
	// OnEvent receives room and peer events for the UI.
	OnEvent func(Notice)
}

// Create builds a room. It does not issue a pairing code; call IssuePairing.
//
// The caller supplies the transport and peer manager because both are wired to
// the room's identity before the room exists. Building them here would mean the
// room's own id and secret were not yet known at construction time, and both end
// up inside the pairing code.
func Create(opts Options) (*Room, error) {
	if opts.Transport == nil || opts.Peers == nil {
		return nil, protocol.NewError(protocol.CodeConfigInvalid,
			"room: a transport and a peer manager are required")
	}
	if opts.Owner == "" {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "room: an owner peer id is required")
	}
	maxPeers := opts.MaxPeers
	if maxPeers <= 0 {
		maxPeers = DefaultMaxPeers
	}
	if maxPeers > MaxPeersCeiling {
		maxPeers = MaxPeersCeiling
	}
	ttl := opts.PairingTTL
	if ttl <= 0 {
		ttl = DefaultPairingTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	subnet := opts.Subnet
	if !subnet.IsValid() {
		// The derivation is pure, so a host and a guest holding the same room id
		// land on the same subnet without ever exchanging it.
		subnet = ipam.New(ipam.Pool).SubnetFor(opts.RoomID)
	}
	local := opts.LocalAddress
	if !local.IsValid() {
		local = ipam.HostAddress(subnet)
	}
	r := &Room{
		id:             opts.RoomID,
		name:           cleanName(opts.Name),
		owner:          opts.Owner,
		isHost:         true,
		maxPeers:       maxPeers,
		gameProfile:    opts.GameProfile,
		createdAt:      now(),
		tr:             opts.Transport,
		pm:             opts.Peers,
		log:            log.With("room", opts.RoomID),
		now:            now,
		pending:        make(map[transport.PeerID]*pendingJoin),
		done:           make(chan struct{}),
		subnet:         subnet,
		local:          local,
		displayName:    opts.DisplayName,
		allocator:      opts.Allocator,
		onNetworkEvent: opts.OnNetworkEvent,
		leases:         make(map[transport.PeerID]netip.Addr),
		app:            newAppState(),
		mesh:           opts.Mesh,
		nameRes:        opts.NameResolution,
		mode:           normMode(opts.Mode),
		meshSt:         newMeshState(),
		pres:           newPresenceState(),
	}
	r.installAppHandler()
	r.installMesh()
	r.installPresence()
	r.installSignals()
	return r, nil
}

// roomSubnet returns the room's address space.
func (r *Room) roomSubnet() (netip.Prefix, bool) {
	r.netMu.RLock()
	defer r.netMu.RUnlock()
	if !r.subnet.IsValid() {
		return netip.Prefix{}, false
	}
	return r.subnet, true
}

// localAddr returns this machine's address in the room.
func (r *Room) localAddr() netip.Addr {
	r.netMu.RLock()
	defer r.netMu.RUnlock()
	return r.local
}

// ID returns the room id.
func (r *Room) ID() string { return r.id }

// IsHost reports whether this daemon hosts the room.
func (r *Room) IsHost() bool { return r.isHost }

// Owner returns the local peer id that owns the room.
func (r *Room) Owner() protocol.PeerID { return r.owner }

// Peers exposes the peer manager.
func (r *Room) Peers() *peer.Manager { return r.pm }

// Secret returns the room secret. It is only ever encoded into a pairing code
// and must never be logged.
func (r *Room) Secret() string { return r.secret }

// setSecret installs the secret. A joining guest adopts the host's secret; a
// host mints one when it issues its first code.
func (r *Room) setSecret(s string) {
	r.mu.Lock()
	r.secret = s
	r.mu.Unlock()
}

// PairingIssued reports whether a code is currently valid.
func (r *Room) PairingIssued() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pairing != nil && !r.pairingSpent && r.now().Before(r.pairing.expiresAt)
}

// Summary renders the UI view of the room.
func (r *Room) Summary() protocol.RoomSummary {
	r.mu.RLock()
	id, name, owner, isHost := r.id, r.name, r.owner, r.isHost
	maxPeers, profile, created := r.maxPeers, r.gameProfile, r.createdAt
	var expires time.Time
	spent := r.pairingSpent
	if r.pairing != nil && !spent {
		expires = r.pairing.expiresAt
	}
	r.mu.RUnlock()

	peers := r.pm.List()
	// The addresses are stamped in here rather than in the peer manager: the peer
	// manager does not know about the virtual LAN, and a summary that could not
	// name a peer's address would be missing the one thing a user asks for when
	// they open a room.
	//
	// The lookup is by link id, not by the identity the peer announced. Those
	// are minted separately - the link id is what the address was leased
	// against - and matching on the announced identity would leave every peer
	// without an address forever.
	r.leasesMu.RLock()
	for i := range peers {
		if addr, ok := r.leases[transport.PeerID(peers[i].TransportPeerID)]; ok {
			peers[i].VirtualAddress = addr.String()
		}
	}
	r.leasesMu.RUnlock()

	out := protocol.RoomSummary{
		RoomID:           id,
		Name:             name,
		Owner:            owner,
		IsHost:           isHost,
		Peers:            peers,
		PeerCount:        len(peers),
		MaxPeers:         maxPeers,
		CreatedAt:        created,
		GameProfile:      profile,
		PairingExpiresAt: expires,
		PairingSpent:     spent,
		Mode:             r.Mode(),
	}
	if subnet, ok := r.roomSubnet(); ok {
		out.Subnet = subnet.String()
		out.LocalAddress = r.localAddr().String()
	}
	return out
}

// reserveCapacity checks the room is not full, counting pending joins.
//
// A pending join is counted because it has already spent a pairing code and will
// become a real peer or time out. Ignoring it would let a caller with a single
// valid code open an unbounded number of half-finished links.
func (r *Room) reserveCapacity() error {
	r.mu.RLock()
	pending := len(r.pending)
	r.mu.RUnlock()
	live := r.pm.Count()
	if live+pending >= r.MaxPeers() {
		return protocol.NewErrorf(protocol.CodeRoomFull,
			"room: %s already holds %d of %d peers", r.id, live, r.MaxPeers())
	}
	return nil
}

// makeRoomForCode ensures a new code can be issued. When the room is full only
// because of codes nobody has answered yet, the oldest unanswered code is
// withdrawn: a host pressing "new code" means the new one, and refusing it
// because of a code sent to someone who never replied is the wrong answer.
func (r *Room) makeRoomForCode() error {
	for {
		r.mu.RLock()
		pending := len(r.pending)
		var oldest *pendingJoin
		for _, p := range r.pending {
			if oldest == nil || p.joinedAt.Before(oldest.joinedAt) {
				oldest = p
			}
		}
		r.mu.RUnlock()
		live := r.pm.Count()
		if live+pending < r.MaxPeers() {
			return nil
		}
		if oldest == nil {
			return protocol.NewErrorf(protocol.CodeRoomFull,
				"room: %s already holds %d of %d peers", r.id, live, r.MaxPeers())
		}
		r.log.Info("withdrawing the oldest unanswered code to make room for a new one",
			"guest", string(oldest.peerID))
		r.dropPending(oldest.peerID)
	}
}

// releaseGuest hands a guest's reserved address back after a failed join.
func (r *Room) releaseGuest(peerID transport.PeerID, addr netip.Addr) {
	r.netMu.RLock()
	alloc := r.allocator
	r.netMu.RUnlock()
	if alloc != nil && addr.IsValid() {
		alloc.Release(r.id, string(peerID))
	}
}

// MaxPeers returns the capacity.
func (r *Room) MaxPeers() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.maxPeers
}

// addPending records a guest awaiting an answer.
func (r *Room) addPending(p *pendingJoin) {
	r.mu.Lock()
	r.pending[p.peerID] = p
	r.mu.Unlock()
}

// takePending removes and returns a pending guest, or nil.
func (r *Room) takePending(peerID transport.PeerID) *pendingJoin {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[peerID]
	if !ok {
		return nil
	}
	delete(r.pending, peerID)
	return p
}

// pendingFor returns a pending guest without removing it.
func (r *Room) pendingFor(peerID transport.PeerID) *pendingJoin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pending[peerID]
}

// expirePending drops pending joins older than ttl.
//
// A guest that produced an answer code and then vanished would otherwise hold a
// reservation forever, and the room would slowly refuse new guests for reasons
// the user cannot see. The sweep runs from the room's own goroutine so the
// expiry needs no timer of its own per guest.
//
// The reserved address goes back at the same moment as the reservation: an
// address nobody can reach is exactly as leaked as a full one.
func (r *Room) expirePending(ttl time.Duration) {
	now := r.now()
	cutoff := now.Add(-ttl)
	r.mu.Lock()
	var dropped []*pendingJoin
	for id, p := range r.pending {
		expired := p.joinedAt.Before(cutoff)
		if !p.expiresAt.IsZero() {
			// A guest holds its reservation for exactly as long as its code
			// is redeemable, plus a minute for the answer to travel back.
			expired = now.After(p.expiresAt.Add(time.Minute))
		}
		if expired {
			dropped = append(dropped, p)
			delete(r.pending, id)
		}
	}
	r.mu.Unlock()

	for _, p := range dropped {
		r.log.Info("dropping a join that never produced an answer",
			"peer", string(p.peerID), "address", p.addr.String())
		_ = r.tr.Close(p.peerID)
		r.pm.Remove(p.peerID)
		r.netMu.RLock()
		alloc := r.allocator
		r.netMu.RUnlock()
		if alloc != nil && p.addr.IsValid() {
			alloc.Release(r.id, string(p.peerID))
		}
	}
}

// Run drives the room until ctx is cancelled: the peer liveness loop, the
// pending join sweep and the addressing reconciliation.
func (r *Room) Run(ctx context.Context) {
	// The liveness loop is scoped to this room, not to the manager. With the
	// manager's context it would keep ticking for a room that was left until
	// the daemon exited.
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.pm.Run(pctx)
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	presence := time.NewTicker(presenceTick)
	defer presence.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.done:
			return
		case <-presence.C:
			r.announcePresence("")
		case <-ticker.C:
			r.expirePending(pendingJoinTTL)
			r.reconcile()
		}
	}
}

// pendingJoinTTL bounds how long a guest may hold a reservation without
// producing an answer.
const pendingJoinTTL = 5 * time.Minute

// pingInterval is the room maintenance tick. It sweeps expired joins and
// reconciles addressing; peer events also trigger a reconcile, so this is only
// the backstop.
const pingInterval = 10 * time.Second

// Close tears the room down: peers are told goodbye, links are closed, the
// virtual LAN is removed and the peer manager releases its handlers.
//
// The order matters. The network is detached last, after the peers have been
// told to leave, so a goodbye packet still has somewhere to go; and the
// allocator is emptied last of all, so a room that is reopened immediately does
// not find its own subnet taken by leases belonging to processes that have
// already gone.
func (r *Room) Close(ctx context.Context) error {
	var err error
	r.closeOnce.Do(func() {
		close(r.done)
		if perr := r.pm.Close(ctx); perr != nil && err == nil {
			err = perr
		}
		if serr := r.tr.Shutdown(ctx); serr != nil && err == nil {
			err = serr
		}
		r.detachNetwork(ctx)
		r.releaseLeases()
		r.mu.Lock()
		r.pending = make(map[transport.PeerID]*pendingJoin)
		r.pairing = nil
		r.secret = ""
		r.mu.Unlock()
		r.leasesMu.Lock()
		r.leases = make(map[transport.PeerID]netip.Addr)
		r.leasesMu.Unlock()
		r.log.Info("room closed", "id", r.id)
	})
	return err
}

// cleanName trims a room name to something safe to render and log.
func cleanName(s string) string {
	if s == "" {
		return "LanBaz Room"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
		if len(out) >= MaxNameLen {
			break
		}
	}
	if len(out) == 0 {
		return "LanBaz Room"
	}
	return string(out)
}

// peerLabel renders a peer for a log line. Peer ids are public by design, so this
// needs no redaction; it exists only to keep the wording consistent.
func peerLabel(id protocol.PeerID) string { return fmt.Sprintf("%s", id) }

func normMode(m string) string {
	if m == protocol.RoomModeL2 {
		return protocol.RoomModeL2
	}
	return protocol.RoomModeL3
}

// Mode returns "l3" or "l2".
func (r *Room) Mode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mode
}
