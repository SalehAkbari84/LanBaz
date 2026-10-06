// Package portmap asks the home router to forward a UDP port to this PC, so a
// friend's connection checks can come straight in and two players behind
// strict routers connect directly instead of through the relay.
//
// Two protocols cover nearly every home router: NAT-PMP (Apple's, also
// answered by most PCP routers) and UPnP IGD (the "UPnP" switch in router
// settings). Both only ever talk to the router itself: NAT-PMP to the default
// gateway, UPnP to a device on the local subnet that answered our search.
// Nothing here opens a port on this PC's firewall or talks to the internet.
package portmap

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Mapping is one forwarded port.
type Mapping struct {
	Method     string // "NAT-PMP" or "UPnP"
	Gateway    netip.Addr
	ExternalIP netip.Addr
	Internal   uint16
	External   uint16
	Lifetime   time.Duration // 0 = permanent (some UPnP routers)
}

func (m Mapping) String() string {
	return fmt.Sprintf("%s on %s: %s:%d → this PC:%d", m.Method, m.Gateway, m.ExternalIP, m.External, m.Internal)
}

// Client is a router that forwards ports.
type Client interface {
	Method() string
	ExternalIP(ctx context.Context) (netip.Addr, error)
	// Map forwards UDP external port (as close to internal as the router
	// allows) to local:internal for lifetime.
	Map(ctx context.Context, internal uint16, lifetime time.Duration) (Mapping, error)
	Unmap(ctx context.Context, m Mapping) error
}

// ErrUnsupported means the router answered neither protocol.
var ErrUnsupported = errors.New("the router does not support UPnP or NAT-PMP (or it is turned off)")

// ErrNotPublic means the router's own "external" address is private or
// carrier-grade NAT: a mapping there would not make this PC reachable.
var ErrNotPublic = errors.New("the router's external address is not public (carrier-grade NAT); port mapping would not help")

// Discover finds a port-mapping router behind local (this PC's LAN address)
// with the given default gateway: NAT-PMP first (one packet), then UPnP.
func Discover(ctx context.Context, local, gateway netip.Addr) (Client, error) {
	if !local.Is4() || !gateway.Is4() {
		return nil, ErrUnsupported
	}
	if c := newNATPMP(local, gateway); c.probe(ctx) {
		return c, nil
	}
	if c, err := discoverUPnP(ctx, local, gateway); err == nil {
		return c, nil
	}
	return nil, ErrUnsupported
}

// Public reports whether an address is usable from the internet.
func Public(a netip.Addr) bool {
	if !a.IsValid() || !a.Is4() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(a) // RFC 6598 CGNAT
}
