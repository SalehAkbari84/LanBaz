// Package daemon wires the LanBaz control plane together and owns the process
// lifecycle of lanbazd.
//
// Everything the daemon can do in Phase 0 is reachable from a single
// internal/api.Server instance; room, peer and virtual network management arrive
// in later phases behind the same registry, so no client has to learn a new
// connection mechanism when they do.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/api"
	"github.com/lanbaz/lanbaz/core/internal/config"
	"github.com/lanbaz/lanbaz/core/internal/games"
	"github.com/lanbaz/lanbaz/core/internal/identity"
	"github.com/lanbaz/lanbaz/core/internal/logging"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/network/tap"
	"github.com/lanbaz/lanbaz/core/internal/relay"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/security"
	"github.com/lanbaz/lanbaz/core/internal/settings"
	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/internal/transport/webrtc"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// BuildInfo carries the version metadata injected at link time.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
}

// DefaultBuildInfo is used when ldflags were not provided (go run, tests).
var DefaultBuildInfo = BuildInfo{Version: "0.5.2-dev", Commit: "unknown", BuildTime: "unknown"}

// Daemon is the running control plane.
type Daemon struct {
	cfg   config.Config
	log   *slog.Logger
	api   *api.Server
	build BuildInfo
	token string
	level string
	// id is this installation's stable peer id, derived from its key.
	id    identity.PeerID
	idKey []byte
	// friends is the friend system; nil when it is unavailable.
	friends *friends
	files   *fileSvc
	// gameRunning is true while a game is detected; file transfers slow down.
	gameRunning atomic.Bool
	// metered holds relay servers fetched from a Metered account.
	meteredMu sync.Mutex
	metered   []transport.RelayServer
	rooms     *room.Manager
	roomsMu   sync.RWMutex

	// settings are the user-editable display name and ICE servers.
	settings *settings.Store
	// detector notices the game this machine runs.
	detector *games.Detector

	// The virtual network. See network.go: the daemon owns the decision about
	// which adapter backend to use, and the network package owns everything
	// after that.
	alloc      *ipam.Allocator
	netFactory room.NetworkFactory
	l2Factory  room.NetworkFactory
	netNote    string

	statePath string

	// managedByShell records that this process was started as the desktop
	// shell's sidecar, which the state file publishes so a forced shell exit can
	// clean this process up without touching a hand-started daemon.
	managedByShell bool

	startedAt time.Time

	mu    sync.RWMutex
	state protocol.DaemonState

	shutdownOnce sync.Once
	shutdownCh   chan struct{}
	doneCh       chan struct{}
	// clients tracks connected local clients for the status payload.
	clients int
}

// Options configures New.
type Options struct {
	Config config.Config
	// Token is the API token. When empty, a fresh one is generated.
	Token string
	// Logger is the already configured daemon logger. When nil, one is built
	// from Config.
	Logger *slog.Logger
	// BuildInfo is the version metadata. When zero-valued, DefaultBuildInfo is
	// used.
	BuildInfo BuildInfo
	// ExePath is recorded in the state file so the UI can restart the daemon.
	ExePath string
	// LogToFile enables the <state-dir>/daemon.log sink.
	LogToFile bool
	// ManagedByShell marks this process as the desktop shell's sidecar. It is
	// published in the state file so the shell can clean up a daemon orphaned by
	// a forced exit, while leaving a hand-started one alone.
	ManagedByShell bool
	// LogRing feeds the in-app developer log (logs.tail, daemon.log events).
	LogRing *logging.Ring
	// SocialBus carries friend messages. Nil uses the public relays; tests
	// pass an in-memory bus. LANBAZ_NO_FRIENDS=1 turns friends off.
	SocialBus social.Bus
}

// New builds a daemon without starting it.
func New(opts Options) (*Daemon, error) {
	cfg := opts.Config
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	build := opts.BuildInfo
	if build.Version == "" {
		build = DefaultBuildInfo
	}

	token := opts.Token
	if token == "" {
		generated, err := security.GenerateToken()
		if err != nil {
			return nil, protocol.NewErrorf(protocol.CodeInternal, "generate api token: %v", err)
		}
		token = generated
	}

	logger := opts.Logger
	if logger == nil {
		// A nil logger is replaced by a discard logger so tests can construct a
		// daemon without wiring logging; the real binary always passes one.
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	d := &Daemon{
		cfg:            cfg,
		log:            logger,
		build:          build,
		token:          token,
		level:          cfg.LogLevel,
		state:          protocol.StateStarting,
		shutdownCh:     make(chan struct{}),
		doneCh:         make(chan struct{}),
		startedAt:      time.Now(),
		managedByShell: opts.ManagedByShell,
	}

	server, err := api.New(api.Options{
		Token:             token,
		Logger:            logger,
		ListenHost:        cfg.ListenHost,
		ListenPort:        cfg.ListenPort,
		MaxConnections:    cfg.MaxConnections,
		RequestsPerSecond: cfg.RequestsPerSecond,
		RequestsBurst:     cfg.RequestsBurst,
		GracePeriod:       time.Duration(cfg.ShutdownGraceMillis) * time.Millisecond,
		Version:           d.versionInfo(),
		StatusFn:          d.status,
		OnShutdown:        func(grace time.Duration) { d.Shutdown(grace) },
	})
	if err != nil {
		return nil, err
	}
	d.api = server

	// The room service is installed separately from the API server and injected
	// into it, because the daemon is the only place that knows which transport
	// implementation is in use. The api package must not learn that WebRTC
	// exists, or swapping it would ripple all the way into the control plane.
	//
	// The network is initialised first because the rooms need its allocator: a
	// per-room allocator would let two rooms on this machine claim the same
	// subnet, and the symptom - two unrelated LANs on one wire - is very hard to
	// diagnose from the outside.
	// Settings first: the network factories read the advanced options.
	store, serr := settings.Open(cfg.StateDir)
	if serr != nil {
		d.log.Warn("settings file ignored; using defaults", "error", serr)
	}
	d.settings = store

	if err := d.initNetwork(); err != nil {
		return nil, err
	}
	if err := d.initRooms(); err != nil {
		return nil, err
	}
	if err := d.initGames(); err != nil {
		return nil, err
	}
	if opts.LogRing != nil {
		d.registerLogs(opts.LogRing)
	}
	bus := opts.SocialBus
	if bus == nil && os.Getenv("LANBAZ_NO_FRIENDS") == "" {
		bus = social.NewRelayBus(context.Background(), social.DefaultRelays)
	}
	if bus != nil {
		if err := d.initFriends(bus); err != nil {
			// Friends are an addition; a damaged friend file must not stop
			// the daemon from hosting rooms the manual way.
			d.log.Warn("the friend system is unavailable", "error", err)
		}
	}
	if d.Rooms() != nil {
		d.files = newFileSvc(d)
		if err := d.files.register(); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// initRooms loads the installation identity and builds the room manager.
//
// The identity is loaded here rather than in Run because a room cannot exist
// without one, and a daemon that fails to read its key should fail to start
// rather than start and then fail every room call.
func (d *Daemon) initRooms() error {
	id, err := identity.LoadOrCreate(d.cfg.StateDir)
	if err != nil {
		return protocol.NewErrorf(protocol.CodeInternal,
			"daemon: load identity: %v", err)
	}
	d.id = id.ID()
	d.idKey = id.PrivateKey()

	st := d.settings.Get()
	displayName := st.DisplayName
	if displayName == "" {
		displayName = hostname()
	}

	rooms, err := room.NewManager(room.ManagerOptions{
		LocalID:          protocol.PeerID(d.id),
		LocalKey:         d.idKey,
		DisplayName:      displayName,
		Factory:          webrtc.Factory{},
		STUNServers:      settings.EffectiveSTUN(st),
		TURNServers:      relayServers(st),
		MTU:              d.transportMTU(),
		AllowRelay:       st.AllowRelay,
		NetworkFactory:   d.netFactory,
		L2NetworkFactory: d.l2Factory,
		L2Check:          tap.Available,
		Allocator:        d.alloc,
		NetworkMTU:       d.cfg.NetworkMTU,
		OnNetworkEvent:   d.publishNetworkEvent,
		Logger:           d.log,
	})
	if err != nil {
		return err
	}
	d.rooms = rooms
	d.rooms.SetEventHandler(d.publishRoomEvent)
	d.rooms.SetTransportDefaults(d.transportDefaults(st))

	if err := d.api.SetRoomService(rooms); err != nil {
		return err
	}
	if err := d.api.SetSettingsService(settingsService{d: d}); err != nil {
		return err
	}
	if err := d.api.SetCapabilities(func() protocol.Capabilities {
		c := protocol.Capabilities{ClassicLAN: d.l2Factory != nil}
		if err := tap.Available(); err != nil {
			c.ClassicLAN, c.ClassicLANReason = false, err.Error()
		}
		return c
	}); err != nil {
		return err
	}
	if err := d.api.SetDiagnose(d.diagnose); err != nil {
		return err
	}
	// The network methods are registered only when a factory exists. A build
	// configured with the virtual network off still answers them with a clear
	// UNSUPPORTED_VERSION instead of failing to start.
	if d.netFactory != nil {
		if err := d.api.SetNetworkService(networkAdapter{rooms: rooms, note: d.netNote}); err != nil {
			return err
		}
	}
	d.log.Info("room service ready",
		"peer_id", string(d.id), "fingerprint", id.Fingerprint())
	return nil
}

// networkAdapter adapts the room manager to the API's network interface and
// folds the daemon-wide fallback reason into every status payload.
type networkAdapter struct {
	rooms *room.Manager
	note  string
}

func (n networkAdapter) First() (string, bool) { return n.rooms.First() }

func (n networkAdapter) RoomIDs() []string { return n.rooms.RoomIDs() }

func (n networkAdapter) NetworkStatus(ctx context.Context, roomID string) (protocol.NetworkStatus, error) {
	st, err := n.rooms.NetworkStatus(ctx, roomID)
	if err != nil {
		return protocol.NetworkStatus{}, err
	}
	if n.note != "" && st.Note == "" {
		st.Note = n.note
	}
	return st, nil
}

func (n networkAdapter) NetworkRoutes(ctx context.Context, roomID string) ([]protocol.RouteEntry, error) {
	return n.rooms.NetworkRoutes(ctx, roomID)
}

// settingsService adapts the settings store to the API and applies a change to
// the room manager, so the next room created or joined uses it.
type settingsService struct{ d *Daemon }

func (s settingsService) Get() protocol.Settings { return s.d.settings.Get() }

func (s settingsService) Set(next protocol.Settings) (protocol.Settings, error) {
	saved, err := s.d.settings.Set(next)
	if err != nil {
		return protocol.Settings{}, err
	}
	if rooms := s.d.Rooms(); rooms != nil {
		rooms.SetTransportDefaults(s.d.transportDefaults(saved))
		name := saved.DisplayName
		if name == "" {
			name = hostname()
		}
		rooms.SetDisplayName(name)
	}
	go s.d.refreshRelays(context.Background())
	s.d.log.Info("settings updated",
		"stun", len(saved.STUNServers), "turn", len(saved.TURNServers), "relay", saved.AllowRelay)
	return saved, nil
}

// transportDefaults turns saved settings into the room manager's defaults.
func (d *Daemon) transportDefaults(st protocol.Settings) room.TransportDefaults {
	n := settings.Net(st)
	mtu := n.MTU
	if mtu == 0 {
		mtu = d.transportMTU()
	}
	turn := relayServers(st)
	d.meteredMu.Lock()
	hosted := append([]transport.RelayServer(nil), d.metered...)
	d.meteredMu.Unlock()
	turn = append(turn, hosted...)
	return room.TransportDefaults{
		STUN:           settings.EffectiveSTUN(st),
		TURN:           turn,
		AllowRelay:     st.AllowRelay || len(hosted) > 0,
		RelayOnly:      n.RelayOnly,
		PortMin:        uint16(n.PortMin),
		PortMax:        uint16(n.PortMax),
		Mesh:           n.Mesh,
		NameResolution: n.NameResolution,
		RoomMode:       n.RoomMode,
		MTU:            mtu,
	}
}

// netTuning is what a new room's virtual network is built with.
type netTuning struct {
	MTU              int
	NoPriority       bool
	NoDiscoveryRelay bool
	BroadcastRate    float64
}

// tuning reads the current advanced network settings.
func (d *Daemon) tuning(defaultMTU int) netTuning {
	n := protocol.DefaultNetworkSettings()
	if d.settings != nil {
		n = settings.Net(d.settings.Get())
	}
	mtu := n.MTU
	if mtu == 0 {
		mtu = defaultMTU
	}
	return netTuning{
		MTU:              mtu,
		NoPriority:       !n.InterfacePriority,
		NoDiscoveryRelay: !n.RelayDiscovery,
		BroadcastRate:    float64(n.BroadcastRate),
	}
}

// relayServers converts the configured TURN servers to the transport's type.
func relayServers(st protocol.Settings) []transport.RelayServer {
	out := make([]transport.RelayServer, 0, len(st.TURNServers))
	for _, t := range st.TURNServers {
		out = append(out, transport.RelayServer{URL: t.URL, Username: t.Username, Credential: t.Credential})
	}
	return out
}

// Transport defaults.
var (
	// defaultMTU is the safe data frame size for an arbitrary Internet path.
	// 1200 avoids IP fragmentation on common tunnel encapsulations, where 1500
	// would.
	defaultMTU = 1200
)

// transportMTU keeps the transport's frame limit equal to the virtual adapter's
// MTU. If the adapter allowed larger packets than the transport carries, every
// full-size TCP segment a game sent would be dropped without a trace.
func (d *Daemon) transportMTU() int {
	if d.cfg.NetworkMTU > 0 {
		return d.cfg.NetworkMTU
	}
	return defaultMTU
}

// hostname is the default display name for this installation.
func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "lanbaz"
	}
	return h
}

// publishRoomEvent turns an internal room or peer notice into a protocol event
// and pushes it to every connected UI client.
func (d *Daemon) publishRoomEvent(n room.Notice) {
	switch {
	case n.Peer != nil:
		d.api.PublishEvent(n.Kind, *n.Peer)
	case n.Room != nil:
		d.api.PublishEvent(n.Kind, *n.Room)
		if d.friends != nil {
			switch n.Kind {
			case protocol.EventRoomClosed:
				d.friends.roomClosed(n.Room.RoomID, n.Room.Reason)
				d.friends.svc.AnnouncePresence(context.Background())
			case protocol.EventRoomCreated:
				d.friends.svc.AnnouncePresence(context.Background())
			}
		}
	case n.Chat != nil:
		d.api.PublishEvent(n.Kind, *n.Chat)
	case n.Presence != nil:
		d.api.PublishEvent(n.Kind, *n.Presence)
	case n.Voice != nil && n.Kind == room.NoticeFileSignal:
		if d.files != nil {
			d.files.onSignal(*n.Voice)
		}
	case n.Voice != nil:
		d.api.PublishEvent(n.Kind, *n.Voice)
	default:
		// An empty notice is a bug in whichever layer built it, not a condition
		// the UI should ever see. Logging it is the whole response; inventing a
		// payload here would hide the bug behind a plausible-looking event.
		d.log.Warn("dropping an empty room notice", "kind", n.Kind)
	}
}

// Rooms exposes the room manager, mainly for tests and the CLI.
func (d *Daemon) Rooms() *room.Manager {
	d.roomsMu.RLock()
	defer d.roomsMu.RUnlock()
	return d.rooms
}

// API exposes the control API server, mainly for tests.
func (d *Daemon) API() *api.Server { return d.api }

// Token returns the API token. Callers must never log the result.
func (d *Daemon) Token() string { return d.token }

// Run starts the daemon and blocks until it is asked to stop or ctx is done.
// The second return value is a non-nil error only for a failure to start.
func (d *Daemon) Run(ctx context.Context) error {
	exePath, _ := os.Executable()

	d.log.Info("LanBaz daemon started",
		"version", d.build.Version,
		"commit", d.build.Commit,
		"pid", os.Getpid(),
		"state_dir", d.cfg.StateDir,
		"log_level", d.cfg.LogLevel,
		"phase", "2-virtual-network",
	)

	// A second daemon on the same state directory would overwrite the first
	// one's state file, and every client - the desktop app included - would
	// then talk to whichever wrote last while the other kept its adapter and
	// rooms alive and unreachable.
	if err := d.refuseSecondInstance(); err != nil {
		return err
	}

	if err := d.api.Start(ctx); err != nil {
		return err
	}

	port := d.api.Port()
	addr := d.api.Addr().String()

	// The daemon is "running" the moment the API accepts connections, which it
	// already does. Flip the state before publishing the state file: clients
	// wait for that file to appear and then immediately query daemon.status,
	// and they must never observe "starting" on a socket that is live.
	d.setState(protocol.StateRunning)

	state := security.NewState(os.Getpid(), addr, port, d.token, d.build.Version, exePath)
	// The shell needs to recognise this process as its own child so a forced
	// exit can clean it up. A daemon started by hand leaves the flag unset.
	state.ManagedByShell = d.managedByShell
	path, err := security.WriteStateFile(d.cfg.StateDir, state)
	if err != nil {
		_ = d.api.Shutdown(context.Background())
		return err
	}

	// A sidecar must not outlive the shell that started it. The shell cannot
	// enforce this itself: a forced kill of the shell (Task Manager, a crash) runs
	// no cleanup code, so the guarantee has to live here, where the process that
	// would be orphaned can see it happen.
	//
	// `ManagedByShell` gates it, so a daemon started by hand survives its parent.
	if d.managedByShell {
		go d.exitWithParent(d.log)
	}
	go d.watchGames(ctx)
	go d.keepRelays(ctx)
	if d.friends != nil {
		go d.friends.svc.Run(ctx)
		go d.friends.runKeep(ctx)
	}
	d.statePath = path

	d.log.Info("daemon state file written", "path", path, "port", port)

	// The banner below is what scripts and the acceptance test grep for.
	fmt.Printf("LanBaz daemon started (version %s, pid %d)\n", d.build.Version, os.Getpid())
	fmt.Printf("API listening on %s%s\n", addr, protocol.APIPath)

	d.api.PublishEvent(protocol.EventDaemonState, protocol.StateEvent{
		State:     protocol.StateRunning,
		Previous:  protocol.StateStarting,
		Timestamp: time.Now().UTC(),
	})

	select {
	case <-d.shutdownCh:
	case <-ctx.Done():
		d.log.Info("context cancelled, shutting down", "reason", ctx.Err())
	}

	d.finish()
	return nil
}

// Shutdown asks the daemon to stop. It is safe to call from an API handler and
// from a signal handler, and it is idempotent.
func (d *Daemon) Shutdown(grace time.Duration) {
	d.shutdownOnce.Do(func() {
		d.log.Info("shutdown initiated", "grace_ms", grace.Milliseconds())
		d.setState(protocol.StateStopping)
		d.api.PublishEvent(protocol.EventDaemonStopping, protocol.StateEvent{
			State:     protocol.StateStopping,
			Previous:  protocol.StateRunning,
			Timestamp: time.Now().UTC(),
		})
		close(d.shutdownCh)
	})
}

// Done is closed once the daemon has fully stopped.
func (d *Daemon) Done() <-chan struct{} { return d.doneCh }

func (d *Daemon) finish() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(d.cfg.ShutdownGraceMillis)*time.Millisecond)
	defer cancel()

	if err := d.api.Shutdown(ctx); err != nil {
		d.log.Warn("api shutdown did not complete cleanly", "error", err)
	}
	// Rooms are closed after the API, so a UI that is still connected learns
	// why each link went away before the socket carrying the news disappears.
	if svc := d.Rooms(); svc != nil {
		if err := svc.Close(ctx); err != nil {
			d.log.Warn("room shutdown did not complete cleanly", "error", err)
		}
	}
	if err := security.RemoveStateFile(d.cfg.StateDir); err != nil {
		d.log.Warn("could not remove state file", "path", d.statePath, "error", err)
	}
	d.setState(protocol.StateStopped)
	d.log.Info("LanBaz daemon stopped", "uptime", time.Since(d.startedAt).Round(time.Millisecond).String())
	fmt.Println("LanBaz daemon stopped")
	close(d.doneCh)
}

func (d *Daemon) setState(state protocol.DaemonState) {
	d.mu.Lock()
	prev := d.state
	d.state = state
	d.mu.Unlock()
	if prev != state {
		d.log.Debug("state changed", "from", string(prev), "to", string(state))
	}
}

func (d *Daemon) versionInfo() protocol.DaemonVersion {
	return protocol.DaemonVersion{
		Daemon:          "lanbazd",
		Version:         d.build.Version,
		Commit:          d.build.Commit,
		BuildTime:       d.build.BuildTime,
		GoVersion:       runtime.Version(),
		ProtocolVersion: protocol.Version,
		Platform:        api.PlatformString(),
		Phase:           "2-virtual-network",
	}
}

func (d *Daemon) status(ctx context.Context) (protocol.DaemonStatus, error) {
	d.mu.RLock()
	state := d.state
	d.mu.RUnlock()

	// The counts come from the room manager at the moment they are asked for.
	// Caching them would mean the status payload could disagree with the room
	// list a client just fetched, which is exactly the kind of inconsistency that
	// turns into a bug report about a peer that "disappeared".
	rooms, peers := 0, 0
	if svc := d.Rooms(); svc != nil {
		rooms, peers = svc.Counts()
	}

	v := d.versionInfo()
	addr := ""
	port := 0
	if a := d.api.Addr(); a != nil {
		addr = a.String()
		port = d.api.Port()
	}

	return protocol.DaemonStatus{
		State:           state,
		Version:         v.Version,
		Commit:          v.Commit,
		BuildTime:       v.BuildTime,
		GoVersion:       v.GoVersion,
		ProtocolVersion: protocol.Version,
		PID:             os.Getpid(),
		UptimeSeconds:   time.Since(d.startedAt).Seconds(),
		StartedAt:       d.startedAt.UTC().Format(time.RFC3339Nano),
		APIListen:       addr,
		APIPort:         port,
		StateDir:        d.cfg.StateDir,
		LogLevel:        d.level,
		ActiveClients:   d.api.ConnectionCount(),
		RoomCount:       rooms,
		PeerCount:       peers,
	}, nil
}

// ExeName is the daemon binary name used in log output.
func ExeName() string { return filepath.Base(os.Args[0]) }

// ErrAlreadyRunning is returned when a second daemon finds a live state file.
var ErrAlreadyRunning = errors.New("lanbaz: a daemon is already running")

// refuseSecondInstance fails when the state file names a live daemon whose API
// still answers. A stale file - from a crash, or a pid that has been reused -
// is not a reason to refuse, so both checks have to agree.
func (d *Daemon) refuseSecondInstance() error {
	st, err := security.ReadStateFile(d.cfg.StateDir)
	if err != nil || st.PID <= 0 || st.PID == os.Getpid() {
		return nil
	}
	if !processAlive(st.PID) {
		return nil
	}
	addr := st.APIListen
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", st.APIPort)
	}
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return nil
	}
	_ = c.Close()
	return protocol.NewErrorf(protocol.CodeDaemonAlreadyRunning,
		"%v (pid %d, %s); stop it first or use a different --state-dir", ErrAlreadyRunning, st.PID, addr)
}

// keepRelays fetches hosted relay servers at start and every six hours.
func (d *Daemon) keepRelays(ctx context.Context) {
	d.refreshRelays(ctx)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		case <-t.C:
			d.refreshRelays(ctx)
		}
	}
}

// refreshRelays fetches the relay servers of the configured Metered account
// and makes them the fallback path of every new link.
func (d *Daemon) refreshRelays(ctx context.Context) {
	st := d.settings.Get()
	var got []transport.RelayServer
	if st.Metered != nil {
		servers, err := relay.Metered(ctx, st.Metered.App, st.Metered.Key)
		if err != nil {
			d.log.Warn("the relay (Metered) could not be loaded; only direct paths will be tried", "error", err)
			return
		}
		for _, s := range servers {
			got = append(got, transport.RelayServer{URL: s.URL, Username: s.Username, Credential: s.Credential})
		}
		d.log.Info("relay ready: direct paths are tried first, the relay takes over when they fail", "servers", len(got))
	}
	d.meteredMu.Lock()
	d.metered = got
	d.meteredMu.Unlock()
	if rooms := d.Rooms(); rooms != nil {
		rooms.SetTransportDefaults(d.transportDefaults(st))
	}
}

// registerLogs serves the developer log: logs.tail for the history and a
// daemon.log event for every new record.
func (d *Daemon) registerLogs(ring *logging.Ring) {
	// Publishing can itself log ("event dropped for slow subscriber"), which
	// would publish again, forever. Records logged while publishing are kept
	// in the ring but not pushed.
	var publishing atomic.Bool
	ring.OnEntry(func(e logging.Entry) {
		if d.api == nil || !publishing.CompareAndSwap(false, true) {
			return
		}
		defer publishing.Store(false)
		d.api.PublishEvent(protocol.EventDaemonLog, e)
	})
	_ = d.api.Register(protocol.MethodLogsTail, func(_ context.Context, raw json.RawMessage) (any, error) {
		var req struct {
			After uint64 `json:"after"`
		}
		_ = json.Unmarshal(raw, &req)
		return ring.Since(req.After), nil
	})
}
