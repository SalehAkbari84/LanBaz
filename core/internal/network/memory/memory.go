// Package memory implements network.Adapter without touching an operating
// system.
//
// It is not a stub. It is a real backend with two jobs:
//
//   - It is what every router and service test runs against, so the routing
//     rules are exercised by the same code that ships rather than by a mock
//     that can drift from it.
//   - It is the fallback on a machine where the real driver cannot load - a
//     container, a locked-down Windows install, a non-Windows developer box.
//     In that case rooms, pairing and the control plane keep working and the
//     user is told, through network.status, that traffic is not moving.
//
// The second job is why it implements the full Adapter interface rather than a
// test-only subset. A fallback that silently fails to configure would leave the
// daemon claiming a healthy virtual LAN that carries nothing.
package memory

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// queueDepth bounds each direction.
//
// An unbounded queue in a packet path is a memory leak with extra steps: a game
// in a sending spree would otherwise grow it without limit on a machine that is
// already running a browser. Dropping the newest packet is the right loss for
// game traffic, which is what the transport already does upstream.
const queueDepth = 256

// Adapter is an in-memory virtual interface.
//
// The two channels are the whole design: a test injects a packet by writing to
// outbound, and reads what the router delivered by taking from inbound. Nothing
// else about an operating system is simulated, because nothing else about an
// operating system is used by the routing logic.
type Adapter struct {
	mu         sync.Mutex
	name       string
	mtu        int
	created    bool
	configured bool
	addr       network.AddressInfo
	routes     []network.Route

	inbound  chan network.Packet
	outbound chan network.Packet
	stopped  chan struct{}
	once     sync.Once

	// dropped counts packets refused because a queue was full. A test asserting
	// on zero has caught a real backpressure problem rather than a slow machine.
	dropped uint64
}

// New builds an in-memory adapter.
func New() *Adapter {
	return &Adapter{
		inbound:  make(chan network.Packet, queueDepth),
		outbound: make(chan network.Packet, queueDepth),
		stopped:  make(chan struct{}),
		mtu:      network.DefaultMTU,
	}
}

// Name implements network.Adapter.
func (a *Adapter) Name() string { return "memory" }

// Create implements network.Adapter.
func (a *Adapter) Create(_ context.Context, name string, mtu int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.created {
		return network.ErrAdapterExists
	}
	if mtu <= 0 {
		mtu = network.DefaultMTU
	}
	a.name, a.mtu, a.created = name, mtu, true
	return nil
}

// Configure implements network.Adapter.
func (a *Adapter) Configure(_ context.Context, addr network.AddressInfo) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.created {
		return network.ErrNotStarted
	}
	if !addr.IPv4.IsValid() {
		return protocol.NewError(protocol.CodeConfigInvalid,
			"memory: the interface needs an IPv4 prefix")
	}
	// The in-memory backend reports fan-out support because it can deliver it:
	// the channels carry whatever is written to them, and the router does the
	// fanning. Claiming otherwise would make every test assert the degraded path.
	a.addr = addr
	a.addr.Broadcast, a.addr.Multicast = true, true
	a.configured = true
	a.routes = nil
	a.routes = append(a.routes, network.Route{
		Destination: addr.IPv4.Masked(),
		NextHop:     addr.Gateway,
		Managed:     true,
		Note:        "room subnet, on-link",
	})
	return nil
}

// ReadPacket implements network.Adapter. It returns what a local game injected.
func (a *Adapter) ReadPacket(ctx context.Context) (network.Packet, error) {
	select {
	case <-ctx.Done():
		return network.Packet{}, ctx.Err()
	case <-a.stopped:
		return network.Packet{}, network.ErrClosed
	case pkt := <-a.outbound:
		if pkt.ReceivedAt.IsZero() {
			pkt.ReceivedAt = time.Now()
		}
		return pkt, nil
	}
}

// WritePacket implements network.Adapter. It injects a packet for local games.
func (a *Adapter) WritePacket(ctx context.Context, pkt network.Packet) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stopped:
		return network.ErrClosed
	default:
	}
	if len(pkt.Payload) > a.MTU() {
		return &protocol.Error{
			Code:    protocol.CodePayloadTooLarge,
			Message: "memory: packet exceeds the adapter MTU",
		}
	}
	if pkt.ReceivedAt.IsZero() {
		pkt.ReceivedAt = time.Now()
	}
	select {
	case a.inbound <- pkt:
		return nil
	default:
		a.mu.Lock()
		a.dropped++
		a.mu.Unlock()
		return nil
	}
}

// Address implements network.Adapter.
func (a *Adapter) Address() (network.AddressInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.configured {
		return network.AddressInfo{}, network.ErrNotStarted
	}
	return a.addr, nil
}

// Routes implements network.Adapter.
func (a *Adapter) Routes() ([]network.Route, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.configured {
		return nil, network.ErrNotStarted
	}
	return append([]network.Route(nil), a.routes...), nil
}

// Destroy implements network.Adapter.
func (a *Adapter) Destroy(_ context.Context) error {
	a.once.Do(func() { close(a.stopped) })
	a.mu.Lock()
	defer a.mu.Unlock()
	a.created, a.configured = false, false
	a.routes = nil
	return nil
}

// MTU returns the configured packet size bound.
func (a *Adapter) MTU() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mtu
}

// Dropped returns how many packets were refused because a queue was full.
func (a *Adapter) Dropped() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// Inject hands a packet to the adapter as if a local game had sent it. It is the
// test-facing half of ReadPacket: without it a test would have to run a
// goroutine to push a packet into a channel, which turns every assertion into a
// timing question.
func (a *Adapter) Inject(pkt network.Packet) {
	pkt.ReceivedAt = time.Now()
	select {
	case a.outbound <- pkt:
	case <-a.stopped:
	default:
		a.mu.Lock()
		a.dropped++
		a.mu.Unlock()
	}
}

// Delivered blocks until the router injects a packet for local games.
func (a *Adapter) Delivered(timeout time.Duration) (network.Packet, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case pkt := <-a.inbound:
		return pkt, nil
	case <-timer.C:
		return network.Packet{}, errors.New("memory: no packet was delivered in time")
	case <-a.stopped:
		return network.Packet{}, network.ErrClosed
	}
}

// MustConfigure builds, creates and configures an adapter in one step.
func MustConfigure(t testingT, subnet netip.Prefix, local netip.Addr, mtu int) *Adapter {
	t.Helper()
	a := New()
	if err := a.Create(context.Background(), "lanbaz-test", mtu); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := a.Configure(context.Background(), network.AddressInfo{
		Interface: "lanbaz-test",
		IPv4:      netip.PrefixFrom(local, local.BitLen()),
		Gateway:   network.HostAddressOf(subnet),
		MTU:       mtu,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return a
}

// testingT is the subset of testing.TB used here, so a helper can be called
// from a test without importing testing into the package's public surface.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

var _ network.Adapter = (*Adapter)(nil)
