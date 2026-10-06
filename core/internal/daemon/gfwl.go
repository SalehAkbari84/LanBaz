package daemon

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/gfwl"
)

// gfwlNet points Games for Windows Live at the room's adapter while the room's
// network runs (see package gfwl), and passes everything else through.
type gfwlNet struct {
	network.VirtualNetwork
	name    func() string
	log     *slog.Logger
	restore func()
}

// withGFWL is applied to a factory's result: withGFWL(name, log)(build(...)).
func withGFWL[T network.VirtualNetwork](name func() string, log *slog.Logger) func(T, error) (network.VirtualNetwork, error) {
	return func(v T, err error) (network.VirtualNetwork, error) {
		if err != nil {
			return nil, err
		}
		return &gfwlNet{VirtualNetwork: v, name: name, log: log, restore: func() {}}, nil
	}
}

func (g *gfwlNet) Start(ctx context.Context) error {
	if err := g.VirtualNetwork.Start(ctx); err != nil {
		return err
	}
	adapter := g.name()
	restore, prev, ok, err := gfwl.Point(adapter)
	switch {
	case err != nil:
		g.log.Warn("could not point Games for Windows Live at the LanBaz adapter", "adapter", adapter, "error", err)
	case ok:
		g.restore = restore
		g.log.Info("Games for Windows Live (GFWL) now uses the LanBaz adapter for LAN games; restart the game if it was already running",
			"adapter", adapter, "previous", prev)
	}
	return nil
}

func (g *gfwlNet) Stop(ctx context.Context) error {
	g.restore()
	return g.VirtualNetwork.Stop(ctx)
}

// The room and the daemon reach these through interface assertions, which an
// embedded interface would hide; forward them explicitly.

func (g *gfwlNet) SetNameSource(fn func() map[string]netip.Addr) {
	if ns, ok := g.VirtualNetwork.(interface {
		SetNameSource(func() map[string]netip.Addr)
	}); ok {
		ns.SetNameSource(fn)
	}
}

func (g *gfwlNet) Router() *network.Router {
	if r, ok := g.VirtualNetwork.(interface{ Router() *network.Router }); ok {
		return r.Router()
	}
	return nil
}

func (g *gfwlNet) CheckDiscovery(gameRunning bool) {
	if c, ok := g.VirtualNetwork.(interface{ CheckDiscovery(bool) }); ok {
		c.CheckDiscovery(gameRunning)
	}
}
