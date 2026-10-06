//go:build !windows

// Package wintun has no implementation outside Windows.
//
// The stub exists so the package still compiles for `go vet` and for a
// cross-platform build, and so the daemon can ask "can you do this here?" and
// get a clear answer instead of a build failure. LanBaz targets Windows; this
// file is not a promise that it works anywhere else, only that the question can
// be asked.
package wintun

import (
	"context"
	"log/slog"
	"net/netip"

	"github.com/lanbaz/lanbaz/core/internal/network"
)

// Options configures New.
type Options struct {
	Name       string
	Logger     *slog.Logger
	NoPriority bool
}

// Available always reports that the driver is not present here.
func Available() error { return network.ErrAdapterFailed }

// New always fails on a platform with no Wintun driver.
func New(Options) (*Adapter, error) { return nil, network.ErrAdapterFailed }

// Adapter is a placeholder so callers that hold the type still compile.
type Adapter struct{}

// Name implements network.Adapter.
func (a *Adapter) Name() string { return "wintun" }

// The remaining Adapter methods exist only so the type satisfies the interface
// in a cross-platform build. Every one of them fails, because none of them can
// work without the driver.
func (a *Adapter) Create(context.Context, string, int) error { return network.ErrAdapterFailed }
func (a *Adapter) Configure(context.Context, network.AddressInfo) error {
	return network.ErrAdapterFailed
}
func (a *Adapter) ReadPacket(context.Context) (network.Packet, error) {
	return network.Packet{}, network.ErrAdapterFailed
}
func (a *Adapter) WritePacket(context.Context, network.Packet) error { return network.ErrAdapterFailed }
func (a *Adapter) Address() (network.AddressInfo, error) {
	return network.AddressInfo{}, network.ErrAdapterFailed
}
func (a *Adapter) Routes() ([]network.Route, error) { return nil, network.ErrAdapterFailed }
func (a *Adapter) Destroy(context.Context) error    { return network.ErrAdapterFailed }
func (a *Adapter) AddRoute(context.Context, network.Route) error {
	return network.ErrAdapterFailed
}
func (a *Adapter) DeleteRoute(context.Context, netip.Prefix) error { return network.ErrAdapterFailed }

var _ network.Adapter = (*Adapter)(nil)

// CleanupStale is a no-op off Windows.
func CleanupStale(_ context.Context, _ *slog.Logger) []string { return nil }
