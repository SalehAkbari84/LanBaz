package daemon

import (
	"context"
	"log/slog"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/config"
	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/network/memory"
	"github.com/lanbaz/lanbaz/core/internal/network/tap"
	"github.com/lanbaz/lanbaz/core/internal/network/wintun"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Choosing the virtual adapter is the daemon's job and not the network package's.
//
// The daemon is the only layer that knows what machine this is, what the
// configuration says, and what a failure should mean for the process. Keeping
// the decision here is what lets the network package stay portable: it takes an
// AdapterFactory and never learns that Wintun exists.

// defaultNetworkMTU is the virtual interface's packet size bound.
//
// It is deliberately below 1500. An adapter that advertises a full Ethernet
// payload over a path that cannot carry one produces a LAN that works between
// two machines on the same Wi-Fi and breaks the moment one of them is on mobile
// data. See network.DefaultMTU for why it is 1200.
const defaultNetworkMTU = network.DefaultMTU

// initNetwork builds the allocator and the adapter factory.
//
// It never fails. A machine with no driver, no administrator rights, or no
// virtual network support at all still gets a daemon that pairs, connects and
// explains itself through network.status; losing the packet path is reported,
// not fatal.
func (d *Daemon) initNetwork() error {
	d.alloc = ipam.New(ipam.Pool)
	// The L2 factory is always wired on a real backend; whether the TAP driver
	// is installed is checked when a Classic LAN room is created, so installing
	// the driver from the app works without restarting the engine.
	if d.cfg.NetworkBackend != config.NetworkOff && d.cfg.NetworkBackend != config.NetworkMemory {
		d.l2Factory = tapNetworkFactory(d.tuner(defaultNetworkMTU), d.log)
	}
	backend := d.cfg.NetworkBackend
	mtu := d.cfg.NetworkMTU
	if mtu <= 0 {
		mtu = defaultNetworkMTU
	}

	switch backend {
	case config.NetworkOff:
		d.log.Info("the virtual network is disabled by configuration")
		return nil
	case config.NetworkMemory:
		d.netFactory = memoryNetworkFactory(d.tuner(mtu), d.log)
		d.log.Info("using the in-memory network backend; no packets will move",
			"reason", "configured")
		return nil
	case config.NetworkWintun:
		if err := wintun.Available(); err != nil {
			d.log.Warn("the Wintun driver is not available and the backend was pinned to it", "error", err)
		}
		d.netFactory = wintunNetworkFactory(d.tuner(mtu), d.log)
		return nil
	default:
		if err := wintun.Available(); err != nil {
			// The fallback is the whole point of auto. Falling back silently would
			// be worse than useless: a user whose games cannot see each other
			// would be told nothing. The reason travels in network.status.
			d.log.Warn("the Wintun driver is unavailable; falling back to the in-memory backend",
				"error", err,
				"hint", "install wintun.dll next to the daemon and run it as administrator")
			d.netFactory = memoryNetworkFactory(d.tuner(mtu), d.log)
			d.netNote = "the Wintun driver is unavailable (" + err.Error() +
				"); traffic is not moving. " +
				"Install wintun.dll next to the daemon and run LanBaz as administrator."
			return nil
		}
		d.netFactory = wintunNetworkFactory(d.tuner(mtu), d.log)
		d.startStaleCleanup()
		return nil
	}
}

// startStaleCleanup removes adapters an earlier run left behind, in the
// background so the API comes up at once. The Wintun factory waits for it
// (bounded), so no room can race the cleanup for the same adapter name.
func (d *Daemon) startStaleCleanup() {
	done := make(chan struct{})
	staleCleanup.Store(&done)
	go func() {
		defer close(done)
		if removed := wintun.CleanupStale(context.Background(), d.log); len(removed) > 0 {
			d.log.Info("leftover LanBaz adapters removed", "count", len(removed))
		}
	}()
}

// staleCleanup is the in-flight startup cleanup, if any.
var staleCleanup atomic.Pointer[chan struct{}]

// waitStaleCleanup blocks until the startup cleanup is over, or 60 s.
func waitStaleCleanup(ctx context.Context) {
	p := staleCleanup.Load()
	if p == nil {
		return
	}
	select {
	case <-*p:
	case <-time.After(60 * time.Second):
	case <-ctx.Done():
	}
}

// tuner returns a function that reads the advanced settings at the moment a
// room's network is built, so a change applies to the next room.
func (d *Daemon) tuner(defaultMTU int) func() netTuning {
	return func() netTuning { return d.tuning(defaultMTU) }
}

// memoryNetworkFactory builds rooms on the in-memory adapter.
func memoryNetworkFactory(tune func() netTuning, log *slog.Logger) room.NetworkFactory {
	return func(_ context.Context, roomID string, tr transport.Transport,
		subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
		t := tune()
		return network.NewService(network.ServiceConfig{
			RoomID:           roomID,
			Subnet:           subnet,
			Local:            local,
			IsHost:           isHost,
			Transport:        tr,
			Adapter:          memory.New(),
			Backend:          "memory",
			MTU:              t.MTU,
			NoDiscoveryRelay: t.NoDiscoveryRelay,
			BroadcastRate:    t.BroadcastRate,
			Logger:           log,
			OnChange:         func(network.Status) {},
		})
	}
}

// wintunNetworkFactory builds rooms on a real Wintun adapter.
func wintunNetworkFactory(tune func() netTuning, log *slog.Logger) room.NetworkFactory {
	return func(ctx context.Context, roomID string, tr transport.Transport,
		subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
		waitStaleCleanup(ctx)
		t := tune()
		adapter, err := wintun.New(wintun.Options{Name: adapterName(roomID), Logger: log, NoPriority: t.NoPriority})
		if err != nil {
			return nil, err
		}
		name := adapterName(roomID)
		return withGFWL[*network.Service](func() string { return name }, log)(network.NewService(network.ServiceConfig{
			RoomID:           roomID,
			Subnet:           subnet,
			Local:            local,
			IsHost:           isHost,
			Transport:        tr,
			Adapter:          adapter,
			Backend:          "wintun",
			MTU:              t.MTU,
			NoDiscoveryRelay: t.NoDiscoveryRelay,
			BroadcastRate:    t.BroadcastRate,
			Logger:           log,
			OnChange:         func(network.Status) {},
		}))
	}
}

// adapterName derives the Windows interface name for a room.
//
// The room's short id is in the name so two rooms on one machine produce two
// visibly distinct adapters. A user with three rooms open can tell which is
// which in the Windows network list without opening LanBaz at all.
func adapterName(roomID string) string {
	id := roomID
	if len(id) > 8 {
		id = id[len(id)-8:]
	}
	return "LanBaz-" + id
}

// publishNetworkEvent turns an addressing change into a protocol event.
func (d *Daemon) publishNetworkEvent(kind string, ev protocol.NetworkEvent) {
	if d.api == nil {
		return
	}
	// The reason the fallback is reported is folded into every status payload,
	// because a status payload is what a UI renders next to "your LAN is not
	// working".
	if d.netNote != "" && ev.Status.Note == "" {
		ev.Status.Note = d.netNote
	}
	d.api.PublishEvent(kind, ev)
}

// NetworkStatus renders a room's virtual LAN for the API and the CLI.
//
// A room with no network is not an error: a machine that cannot create an
// adapter is exactly the machine whose user needs to be told why, and returning
// an error would send them looking for a failure somewhere else.
func (d *Daemon) NetworkStatus(ctx context.Context, roomID string) (protocol.NetworkStatus, error) {
	rooms := d.Rooms()
	if rooms == nil {
		return protocol.NetworkStatus{}, protocol.NewError(protocol.CodeUnsupportedVersion,
			"daemon: this build has no room service")
	}
	st, err := rooms.NetworkStatus(ctx, roomID)
	if err != nil {
		return protocol.NetworkStatus{}, err
	}
	if d.netNote != "" && st.Note == "" {
		st.Note = d.netNote
	}
	return st, nil
}

// tapNetworkFactory builds Classic LAN rooms: a TAP adapter and an Ethernet
// switch instead of Wintun and the IP router.
func tapNetworkFactory(tune func() netTuning, log *slog.Logger) room.NetworkFactory {
	return func(_ context.Context, roomID string, tr transport.Transport,
		subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
		t := tune()
		adapter, err := tap.New(tap.Options{Logger: log, NoPriority: t.NoPriority})
		if err != nil {
			return nil, err
		}
		alias := func() string {
			if a, ok := any(adapter).(interface{ Alias() string }); ok {
				return a.Alias()
			}
			return tap.AdapterName
		}
		return withGFWL[*network.Switch](alias, log)(network.NewSwitch(network.SwitchConfig{
			RoomID:        roomID,
			IsHost:        isHost,
			Transport:     tr,
			Adapter:       adapter,
			Backend:       "tap",
			Subnet:        subnet,
			Local:         local,
			MTU:           t.MTU,
			BroadcastRate: t.BroadcastRate,
			Logger:        log,
		}))
	}
}
