package room

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/identity"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/pairing"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Manager holds every room this daemon participates in.
//
// It is deliberately a host-side and guest-side capability in one type. A LanBaz
// installation hosts some rooms and is a guest in others at the same time, and
// splitting them into two managers would force the caller to know which it is
// talking to before it knows which room it meant.
type Manager struct {
	localID protocol.PeerID
	log     *slog.Logger
	now     func() time.Time

	// factory builds the transport for a new room. Injected so the daemon can
	// register implementations without this package importing Pion.
	factory transport.Factory
	// transports maps a room id to the factory name that built it, so a room
	// joined with a different implementation still negotiates correctly.
	baseOptions Options
	// optsMu guards the parts of baseOptions the user can change at run time:
	// the display name and the ICE servers.
	optsMu sync.RWMutex

	mu    sync.RWMutex
	rooms map[string]*Room
	// closing holds rooms that left the list but are still tearing down
	// (links, adapter). A rejoin of the same room waits for it: both would
	// otherwise want the same adapter name, and the second create failed
	// with "Cannot create a file when that file already exists".
	closing map[string]chan struct{}

	// onEvent forwards room and peer events to the daemon for the UI. It is
	// stored rather than captured per room because the daemon builds the
	// manager before it can build a closure over the API server.
	onEvent func(Notice)

	// ctx is the lifetime handed to every room's Run.
	ctx    context.Context
	cancel context.CancelFunc

	// l2Factory builds Classic LAN rooms; l2Check says whether it can now.
	l2Factory NetworkFactory
	l2Check   func() error

	// game is this machine's detected game, applied to every room.
	gameMu sync.RWMutex
	game   *LocalGame

	closeOnce sync.Once
}

// Notice is what the room layer reports upward: a protocol event name and its
// payload.
//
// It exists so the event *name* survives the trip. Passing bare protocol
// payloads loses it, and the daemon then has to guess whether a RoomEvent means
// "created" or "closed" — which it would get wrong on exactly the events a user
// is watching for.
type Notice struct {
	// Kind is a protocol.Event* constant.
	Kind string
	// Room is set for room events, Peer for peer events.
	Room     *protocol.RoomEvent
	Peer     *protocol.PeerEvent
	Chat     *protocol.ChatMessage
	Presence *protocol.Presence
	// Voice carries voice signalling (Kind EventVoiceSignal) or file-transfer
	// signalling (Kind NoticeFileSignal, for the daemon, not the UI).
	Voice *protocol.VoiceSignal
}

// NoticeFileSignal is the Notice kind of incoming file-transfer signalling.
const NoticeFileSignal = "file.signal"

// ManagerOptions configures NewManager.
type ManagerOptions struct {
	// LocalID and LocalKey are this daemon's identity.
	LocalID  protocol.PeerID
	LocalKey []byte
	// DisplayName is what this daemon is called in peer lists.
	DisplayName string
	// Factory builds transports. Required.
	Factory transport.Factory
	// STUNServers, TURNServers, MTU and AllowRelay are transport defaults for
	// new rooms.
	STUNServers []string
	TURNServers []transport.RelayServer
	MTU         int
	AllowRelay  bool
	// GameProfile is the default profile for new rooms.
	GameProfile string
	// NetworkFactory builds each room's virtual LAN. Nil disables the virtual
	// network entirely, which is what a build with no adapter backend wants and
	// what the offline tests use.
	NetworkFactory NetworkFactory
	// L2NetworkFactory builds Classic LAN (Ethernet) rooms. Nil means this
	// build cannot host or join them.
	L2NetworkFactory NetworkFactory
	// L2Check reports whether Classic LAN rooms can run right now (driver
	// installed). Nil means they can whenever a factory exists.
	L2Check func() error
	// Allocator hands out addresses across every room. Required whenever a
	// NetworkFactory is supplied: a per-room allocator would let two rooms claim
	// the same subnet.
	Allocator *ipam.Allocator
	// NetworkMTU bounds a packet on the virtual adapter; 0 selects the default.
	NetworkMTU int
	// OnNetworkEvent receives addressing changes for the UI.
	OnNetworkEvent func(string, protocol.NetworkEvent)
	Logger         *slog.Logger
	Now            func() time.Time
	// OnEvent receives room and peer events for the UI.
	OnEvent func(Notice)
}

// NewManager builds a room manager. Rooms are created and joined through it.
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Factory == nil {
		return nil, protocol.NewError(protocol.CodeConfigInvalid,
			"room: a transport factory is required")
	}
	if opts.LocalID == "" || len(opts.LocalKey) != identity.KeySize {
		return nil, protocol.NewErrorf(protocol.CodeConfigInvalid,
			"room: the local identity is required and must be a %d byte key", identity.KeySize)
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		localID: opts.LocalID,
		log:     log,
		now:     now,
		factory: opts.Factory,
		baseOptions: Options{
			Owner:          opts.LocalID,
			OwnerKey:       append([]byte(nil), opts.LocalKey...),
			STUNServers:    append([]string(nil), opts.STUNServers...),
			TURNServers:    append([]transport.RelayServer(nil), opts.TURNServers...),
			MTU:            opts.MTU,
			AllowRelay:     opts.AllowRelay,
			DisplayName:    opts.DisplayName,
			GameProfile:    opts.GameProfile,
			NetworkFactory: opts.NetworkFactory,
			Allocator:      opts.Allocator,
			NetworkMTU:     opts.NetworkMTU,
			OnNetworkEvent: opts.OnNetworkEvent,
			Logger:         log,
		},
		rooms:     make(map[string]*Room),
		onEvent:   opts.OnEvent,
		l2Factory: opts.L2NetworkFactory,
		l2Check:   opts.L2Check,
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

// TransportDefaults are the connection settings applied to new rooms.
type TransportDefaults struct {
	STUN       []string
	TURN       []transport.RelayServer
	AllowRelay bool
	RelayOnly  bool
	PortMin    uint16
	PortMax    uint16
	// Mesh enables direct guest-to-guest links in new rooms.
	Mesh bool
	// NameResolution answers <name>.local for players in new rooms.
	NameResolution bool
	// RoomMode is the default mode for rooms this machine creates.
	RoomMode string
	// MTU is both the tunnel frame limit and the adapter MTU; 0 keeps the
	// current value.
	MTU int
}

// SetTransportDefaults changes the connection settings used by rooms created
// or joined from now on. Rooms already open keep what they started with:
// their links are already negotiated.
func (m *Manager) SetTransportDefaults(d TransportDefaults) {
	m.optsMu.Lock()
	// A non-nil empty list means "no STUN" (offline tests, LAN-only use);
	// nil means the defaults. The copy must keep that distinction.
	if d.STUN != nil {
		m.baseOptions.STUNServers = append([]string{}, d.STUN...)
	} else {
		m.baseOptions.STUNServers = nil
	}
	m.baseOptions.TURNServers = append([]transport.RelayServer(nil), d.TURN...)
	m.baseOptions.AllowRelay = d.AllowRelay
	m.baseOptions.RelayOnly = d.RelayOnly
	m.baseOptions.PortMin, m.baseOptions.PortMax = d.PortMin, d.PortMax
	m.baseOptions.Mesh = d.Mesh
	m.baseOptions.NameResolution = d.NameResolution
	if d.RoomMode != "" {
		m.baseOptions.Mode = d.RoomMode
	}
	if d.MTU > 0 {
		m.baseOptions.MTU = d.MTU
		m.baseOptions.NetworkMTU = d.MTU
	}
	m.optsMu.Unlock()
}

// SetDisplayName changes the name announced in rooms created or joined from
// now on.
func (m *Manager) SetDisplayName(name string) {
	if name == "" {
		return
	}
	m.optsMu.Lock()
	m.baseOptions.DisplayName = name
	m.optsMu.Unlock()
}

// options returns a copy of the base options for a new room.
func (m *Manager) options() Options {
	m.optsMu.RLock()
	defer m.optsMu.RUnlock()
	o := m.baseOptions
	if o.STUNServers != nil {
		o.STUNServers = append([]string{}, o.STUNServers...)
	}
	o.TURNServers = append([]transport.RelayServer(nil), o.TURNServers...)
	return o
}

// SetEventHandler installs the UI event sink after construction.
func (m *Manager) SetEventHandler(fn func(Notice)) {
	m.mu.Lock()
	m.onEvent = fn
	m.mu.Unlock()
}

func (m *Manager) emit(n Notice) {
	m.mu.RLock()
	fn := m.onEvent
	m.mu.RUnlock()
	if fn == nil {
		return
	}
	fn(n)
}

// emitRoom reports a room lifecycle event.
func (m *Manager) emitRoom(kind string, ev protocol.RoomEvent) {
	m.emit(Notice{Kind: kind, Room: &ev})
}

// l2Busy reports an error when another classic LAN room is open. There is one
// TAP adapter per machine and only one room can hold it.
func (m *Manager) l2Busy(except string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, r := range m.rooms {
		if id != except && r.Mode() == protocol.RoomModeL2 {
			return protocol.NewErrorf(protocol.CodeRoomFull,
				"room: only one classic LAN (L2) room can be open at a time; leave %q first", r.name)
		}
	}
	return nil
}

// Create makes a new hosted room and registers it.
func (m *Manager) Create(ctx context.Context, req protocol.RoomCreateRequest) (protocol.RoomCreateResponse, error) {
	opts := m.options()
	opts.Name = req.Name
	if req.Mode != "" {
		opts.Mode = req.Mode
	}
	if normMode(opts.Mode) == protocol.RoomModeL2 {
		if m.l2Factory == nil || (m.l2Check != nil && m.l2Check() != nil) {
			return protocol.RoomCreateResponse{}, protocol.NewError(protocol.CodeUnsupportedVersion,
				"room: classic LAN (L2) rooms need the TAP-Windows driver; install it from Settings → Network")
		}
		if err := m.l2Busy(""); err != nil {
			return protocol.RoomCreateResponse{}, err
		}
	}
	opts.MaxPeers = req.MaxPeers
	opts.GameProfile = req.GameProfile
	if req.Subnet != "" {
		if p, err := netip.ParsePrefix(req.Subnet); err == nil {
			opts.KeepSubnet = p.Masked()
		}
	}
	if len(req.Leases) > 0 {
		opts.LeaseHints = map[string]netip.Addr{}
		for id, a := range req.Leases {
			if addr, err := netip.ParseAddr(a); err == nil {
				opts.LeaseHints[id] = addr
			}
		}
	}
	opts.OnEvent = func(n Notice) { m.emit(n) }
	opts.Now = m.now

	ttl := time.Duration(req.PairingTTLSeconds) * time.Second
	if req.PairingTTLSeconds == 0 {
		ttl = DefaultPairingTTL
	}

	// The room and its pairing code are one unit from the user's point of view:
	// room.create that returned a room with no code would force a second round
	// trip for something the host already knows it wants. So the code is issued
	// as part of creating the room, and a failure to issue fails the create.
	r, err := CreateRoom(ctx, m.factory, nil, opts)
	if err != nil {
		return protocol.RoomCreateResponse{}, err
	}
	if err := m.adopt(ctx, r); err != nil {
		_ = r.Close(ctx)
		return protocol.RoomCreateResponse{}, err
	}
	// The subnet has to be claimed before the code is minted, not after: the
	// code is what tells every guest which LAN they are joining, and a code
	// naming a subnet this host does not actually hold would configure the whole
	// room on a range that belongs to another room open on this same machine.
	if err := r.ReserveAddressing(); err != nil {
		_ = r.Close(ctx)
		m.remove(r.ID())
		return protocol.RoomCreateResponse{}, err
	}
	pairingRes, err := r.IssuePairing(ctx, ttl)
	if err != nil {
		_ = r.Close(ctx)
		m.remove(r.ID())
		return protocol.RoomCreateResponse{}, err
	}
	m.emitRoom(protocol.EventRoomCreated,
		protocol.RoomEvent{RoomID: r.ID(), Name: r.name, Room: ptr(r.Summary()), At: m.now()})
	// The virtual LAN comes up once the room exists and its addressing is settled.
	// A host's addressing is settled from the moment it is constructed; a
	// guest's only after it has read the code.
	r.attachNet(ctx, m.factoryFor(r))
	return protocol.RoomCreateResponse{
		Room:        r.Summary(),
		PairingCode: pairingRes.PairingCode,
		PairingURI:  pairingRes.PairingURI,
		ExpiresAt:   pairingRes.ExpiresAt,
	}, nil
}

// Join creates a guest room from a host's pairing code and registers it.
//
// The room is registered immediately, in a pending state, even though no link
// exists yet. That is what lets room.get and room.list show a guest the room it
// is joining, and it means the pairing code is consumed from the moment it is
// accepted rather than only on success.
func (m *Manager) Join(ctx context.Context, req protocol.RoomJoinRequest) (protocol.RoomJoinResponse, error) {
	roomID, err := roomIDFromCode(req.PairingCode)
	if err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	m.mu.RLock()
	existing, duplicate := m.rooms[roomID]
	m.mu.RUnlock()
	if duplicate {
		// A fresh code for a room this machine is already in is a rejoin: the
		// link to the host dropped, and the host sent a new code. The old room
		// is replaced unless it still has a working link to its host, which
		// would make this a mistaken paste rather than a reconnect.
		if existing.IsHost() || existing.hostAlive() {
			return protocol.RoomJoinResponse{}, protocol.NewErrorf(protocol.CodePairingReplay,
				"room: %s is already joined on this machine", roomID)
		}
		m.log.Info("replacing a guest room whose host link is gone", "room", roomID)
		if err := m.Leave(ctx, roomID); err != nil {
			m.log.Warn("could not close the stale room before rejoining", "room", roomID, "error", err)
		}
	}

	m.waitClosed(ctx, roomID)

	opts := m.options()
	opts.RoomID = roomID
	opts.Name = roomID
	if req.DisplayName != "" {
		opts.DisplayName = req.DisplayName
	}
	opts.OnEvent = func(n Notice) { m.emit(n) }
	opts.Now = m.now

	r, err := CreateRoom(ctx, m.factory, nil, opts)
	if err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	// A guest is not the host, which is the single most important flag on a
	// room: it decides whether codes can be issued and how the peer list is
	// rendered.
	r.isHost = false
	r.onHostGone = func(reason string) { m.dropGuestRoom(r.ID(), reason) }
	r.onModeChange = func(mode string) { m.guestModeChanged(r, mode) }
	if err := m.adopt(ctx, r); err != nil {
		_ = r.Close(ctx)
		return protocol.RoomJoinResponse{}, err
	}
	resp, err := r.Join(ctx, req.PairingCode, req.DisplayName)
	if err != nil {
		// The join failed, so no room exists from the user's point of view.
		// Dropping it here rather than leaving an empty room in the list is what
		// makes a bad code look like nothing happened.
		_ = r.Close(ctx)
		m.remove(r.ID())
		return protocol.RoomJoinResponse{}, err
	}
	if r.Mode() == protocol.RoomModeL2 {
		if err := m.l2Busy(r.ID()); err != nil {
			_ = r.Close(ctx)
			m.remove(r.ID())
			return protocol.RoomJoinResponse{}, err
		}
	}
	resp.Room = r.Summary()
	resp.LocalPeer = r.pm.LocalSummary(false)
	m.emitRoom(protocol.EventRoomCreated,
		protocol.RoomEvent{RoomID: r.ID(), Name: r.name, Room: ptr(r.Summary()), At: m.now()})
	// The guest learned its addressing from the code inside Join, so this is the
	// first moment the virtual LAN can be built for it.
	r.attachNet(ctx, m.factoryFor(r))
	return resp, nil
}

// Accept applies a guest's answer code to a hosted room.
func (m *Manager) Accept(ctx context.Context, req protocol.RoomAcceptRequest) error {
	r, err := m.Get(req.RoomID)
	if err != nil {
		return err
	}
	if err := r.Accept(ctx, req); err != nil {
		return err
	}
	// A host whose adapter failed the first time gets another attempt here: the
	// user may well have restarted the daemon as administrator between issuing a
	// code and accepting the answer.
	r.attachNet(ctx, m.factoryFor(r))
	return nil
}

// RegeneratePairing issues a fresh code, invalidating the previous one.
func (m *Manager) RegeneratePairing(ctx context.Context, req protocol.RoomRegeneratePairingRequest) (protocol.PairingResponse, error) {
	r, err := m.Get(req.RoomID)
	if err != nil {
		return protocol.PairingResponse{}, err
	}
	return r.IssuePairingFor(ctx, time.Duration(req.TTLSeconds)*time.Second, req.ForPeer)
}

// Get returns one room.
func (m *Manager) Get(id string) (*Room, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rooms[id]
	if !ok {
		return nil, protocol.NewErrorf(protocol.CodeNotFound, "room: %s is not open here", id)
	}
	return r, nil
}

// All returns every room. It exists alongside List because a peer id is unique
// per installation but not namespaced by room, so a caller looking for one peer
// has to search every room it is in.
func (m *Manager) All() []*Room {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		out = append(out, r)
	}
	return out
}

// List returns every room's summary, ordered by creation.
func (m *Manager) List() []protocol.RoomSummary {
	rooms := m.All()
	out := make([]protocol.RoomSummary, 0, len(rooms))
	for _, r := range rooms {
		out = append(out, r.Summary())
	}
	return out
}

// Counts returns the room and peer totals for daemon.status.
func (m *Manager) Counts() (rooms, peers int) {
	rs := m.All()
	for _, r := range rs {
		peers += r.pm.Count()
	}
	return len(rs), peers
}

// Leave closes a room and forgets it. The distinction from Close is that Leave
// also applies to a room this daemon hosts, which is the user saying they are
// done running a LAN, not just stepping out of one.
func (m *Manager) Leave(ctx context.Context, id string) error {
	r, err := m.Get(id)
	if err != nil {
		return err
	}
	done := m.removeClosing(id)
	defer done()
	m.emitRoom(protocol.EventRoomClosed, protocol.RoomEvent{RoomID: id, Reason: "left", At: m.now()})
	return r.Close(context.WithoutCancel(ctx))
}

// removeClosing takes a room off the list and records it as closing until
// the returned function is called.
func (m *Manager) removeClosing(id string) func() {
	ch := make(chan struct{})
	m.mu.Lock()
	delete(m.rooms, id)
	if m.closing == nil {
		m.closing = map[string]chan struct{}{}
	}
	m.closing[id] = ch
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if m.closing[id] == ch {
			delete(m.closing, id)
		}
		m.mu.Unlock()
		close(ch)
	}
}

// waitClosed blocks until a room with this id has finished closing.
func (m *Manager) waitClosed(ctx context.Context, id string) {
	m.mu.RLock()
	ch := m.closing[id]
	m.mu.RUnlock()
	if ch == nil {
		return
	}
	m.log.Info("waiting for the previous copy of this room to finish closing", "room", id)
	select {
	case <-ch:
	case <-time.After(45 * time.Second):
	case <-ctx.Done():
	}
}

// dropGuestRoom closes a guest room whose host has gone. Without a host there
// is no LAN - every packet goes through it - so keeping the room would only
// show the user a room that silently does nothing. They rejoin with a new code.
func (m *Manager) dropGuestRoom(id, reason string) {
	r, err := m.Get(id)
	if err != nil || r.IsHost() {
		return
	}
	done := m.removeClosing(id)
	defer done()
	m.emitRoom(protocol.EventRoomClosed, protocol.RoomEvent{RoomID: id, Reason: reason, At: m.now()})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = r.Close(ctx)
	m.log.Info("left a room whose host is gone", "room", id, "reason", reason)
}

// DropStale closes a guest room whose host is known to have moved on (for
// example restarted and opened a new room), with a reason other than "left".
func (m *Manager) DropStale(id, reason string) { m.dropGuestRoom(id, reason) }

// adopt registers a room and starts its background goroutines.
func (m *Manager) adopt(ctx context.Context, r *Room) error {
	m.mu.Lock()
	if _, exists := m.rooms[r.ID()]; exists {
		m.mu.Unlock()
		return protocol.NewErrorf(protocol.CodeInternal,
			"room: %s was registered twice", r.ID())
	}
	m.rooms[r.ID()] = r
	m.mu.Unlock()
	r.SetChatHandler(func(msg protocol.ChatMessage) {
		m.emit(Notice{Kind: protocol.EventChatMessage, Chat: &msg})
	})
	r.SetPresenceHandler(func(p protocol.Presence) {
		m.emit(Notice{Kind: protocol.EventPeerPresence, Presence: &p})
	})
	r.SetSignalHandler(func(kind string, v protocol.VoiceSignal) {
		ev := protocol.EventVoiceSignal
		if kind == SignalFile {
			ev = NoticeFileSignal
		}
		m.emit(Notice{Kind: ev, Voice: &v})
	})
	m.gameMu.RLock()
	g := m.game
	m.gameMu.RUnlock()
	if g != nil {
		go r.SetLocalGame(g)
	}

	go r.Run(m.ctx)
	m.log.Info("room registered", "room", r.ID(), "host", r.IsHost())
	return nil
}

// remove forgets a room without closing it.
func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.rooms, id)
	m.mu.Unlock()
}

func ptr[T any](v T) *T { return &v }

// Close tears down every room and cancels their goroutines.
func (m *Manager) Close(ctx context.Context) error {
	m.closeOnce.Do(func() {
		m.cancel()
		m.mu.Lock()
		rooms := make([]*Room, 0, len(m.rooms))
		for id, r := range m.rooms {
			rooms = append(rooms, r)
			delete(m.rooms, id)
		}
		m.mu.Unlock()
		for _, r := range rooms {
			if err := r.Close(ctx); err != nil {
				m.log.Warn("a room did not close cleanly", "room", r.ID(), "error", err)
			}
		}
	})
	return nil
}

// roomIDFromCode extracts just the room id from a pairing code, so a duplicate
// join can be refused before anything else happens.
//
// It deliberately does not verify the signature. The id is used only to notice
// that this machine already has the room open, which is a UX check and not a
// security one; full verification happens in Room.Join before any field of the
// code influences a link.
func roomIDFromCode(s string) (string, error) {
	code, err := pairing.Decode(s)
	if err != nil {
		return "", err
	}
	if code.RoomID == "" {
		return "", protocol.NewError(protocol.CodePairingInvalid,
			"room: the pairing code names no room")
	}
	return code.RoomID, nil
}

// RoomIDFromCode extracts the room id a pairing or answer code belongs to.
//
// It exists so the control API can accept room.accept with only the answer
// code, which is what lets a user paste one string and get a working join
// instead of having to remember which room they came from. The returned id is
// unverified; Room.Accept verifies the code before acting on any of it.
func RoomIDFromCode(s string) (string, error) { return roomIDFromCode(s) }

// SendVoice relays voice signalling from this PC's app to a player.
func (m *Manager) SendVoice(roomID, to string, data json.RawMessage) error {
	r, err := m.Get(roomID)
	if err != nil {
		return err
	}
	return r.SendVoice(to, data)
}

// SendSignal sends signalling of one kind (SignalVoice, SignalFile) to a player.
func (m *Manager) SendSignal(roomID, kind, to string, data json.RawMessage) error {
	r, err := m.Get(roomID)
	if err != nil {
		return err
	}
	return r.SendSignal(kind, to, data)
}

// SendChat posts a message to a room.
func (m *Manager) SendChat(roomID, text string) (protocol.ChatMessage, error) {
	r, err := m.Get(roomID)
	if err != nil {
		return protocol.ChatMessage{}, err
	}
	return r.SendChat(text)
}

// ChatHistory returns a room's recent chat.
func (m *Manager) ChatHistory(roomID string) ([]protocol.ChatMessage, error) {
	r, err := m.Get(roomID)
	if err != nil {
		return nil, err
	}
	return r.ChatHistory(), nil
}

// SetLocalGame records this machine's game and announces it in every room.
func (m *Manager) SetLocalGame(g *LocalGame) {
	m.gameMu.Lock()
	m.game = g
	m.gameMu.Unlock()
	for _, r := range m.All() {
		r.SetLocalGame(g)
	}
}

// Presences returns what every player in every room is running.
func (m *Manager) Presences() []protocol.Presence {
	var out []protocol.Presence
	for _, r := range m.All() {
		out = append(out, r.Presences()...)
	}
	return out
}

// factoryFor picks the network backend for a room's mode.
func (m *Manager) factoryFor(r *Room) NetworkFactory {
	if r.Mode() == protocol.RoomModeL2 {
		if m.l2Factory == nil {
			return nil
		}
		return m.l2Factory
	}
	return m.baseOptions.NetworkFactory
}
