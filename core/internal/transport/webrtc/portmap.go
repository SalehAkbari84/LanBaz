package webrtc

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network/egress"
	"github.com/lanbaz/lanbaz/core/internal/network/portmap"
)

// Router port mapping for links.
//
// When two strict routers drop each other's checks, only the relay works. If
// the home router speaks UPnP or NAT-PMP, LanBaz asks it to forward the UDP
// port of each local ICE candidate and adds the forwarded address to the
// description it sends, as an extra server-reflexive candidate. The friend's
// checks then arrive through the opened port and a direct path forms. The
// mapping lives while the link does and is removed when it closes.

const (
	pmLease        = time.Hour
	pmRediscover   = 10 * time.Minute
	pmCallTimeout  = 4 * time.Second
	pmDiscoverWait = 6 * time.Second
)

// Disabled by LANBAZ_NO_PORTMAP=1, and replaceable in tests.
var (
	portmapDisabled = os.Getenv("LANBAZ_NO_PORTMAP") != ""
	gatewayFor      = egress.PhysicalGateway
	discoverRouter  = portmap.Discover
)

type routerState struct {
	mu      sync.Mutex
	client  portmap.Client
	local   netip.Addr
	ext     netip.Addr
	checked time.Time
	logged  string
}

var router routerState

// routerClient returns the port-mapping router, discovering it at most every
// pmRediscover, and logs the outcome when it changes.
func routerClient(ctx context.Context, log *slog.Logger) (portmap.Client, netip.Addr, netip.Addr) {
	router.mu.Lock()
	defer router.mu.Unlock()
	if !router.checked.IsZero() && time.Since(router.checked) < pmRediscover {
		return router.client, router.local, router.ext
	}
	router.checked = time.Now()
	router.client, router.ext = nil, netip.Addr{}
	local, gw := gatewayFor()
	router.local = local
	note := ""
	if !local.IsValid() || !gw.IsValid() {
		note = "router port mapping: no physical network adapter with a gateway"
	} else {
		dctx, cancel := context.WithTimeout(ctx, pmDiscoverWait)
		c, err := discoverRouter(dctx, local, gw)
		if err == nil {
			var ext netip.Addr
			ext, err = c.ExternalIP(dctx)
			if err == nil && !portmap.Public(ext) {
				err = portmap.ErrNotPublic
			}
			if err == nil {
				router.client, router.ext = c, ext
				note = fmt.Sprintf("router port mapping available: %s on %s, external address %s; direct connections get easier", c.Method(), gw, ext)
			}
		}
		cancel()
		if note == "" {
			note = fmt.Sprintf("router port mapping not available on %s: %v. Turning on UPnP in the router settings lets more friends connect directly", gw, err)
		}
	}
	if note != router.logged {
		router.logged = note
		log.Info(note)
	}
	return router.client, router.local, router.ext
}

// RouterPortMapping describes the router's port-mapping support for
// diagnostics, discovering it if needed.
func RouterPortMapping(ctx context.Context, log *slog.Logger) string {
	if portmapDisabled {
		return "disabled"
	}
	c, _, ext := routerClient(ctx, log)
	if c == nil {
		router.mu.Lock()
		defer router.mu.Unlock()
		return router.logged
	}
	return c.Method() + " " + ext.String()
}

// withMappedCandidates forwards the router port of each host candidate on the
// physical adapter and appends the forwarded address as a srflx candidate.
// It never fails: without a router that maps, the SDP is returned unchanged.
func (l *link) withMappedCandidates(ctx context.Context, sdp string) string {
	if portmapDisabled {
		return sdp
	}
	// Only candidates on the real adapter can be forwarded; a link without one
	// (a test on a virtual network, a VPN-only PC) never talks to the router.
	local, _ := gatewayFor()
	if !local.IsValid() || !strings.Contains(sdp, " "+local.String()+" ") {
		return sdp
	}
	client, _, ext := routerClient(ctx, l.log)
	if client == nil {
		return sdp
	}
	have := map[string]bool{}
	var hostPorts []uint16
	for _, line := range strings.Split(sdp, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 8 || !strings.HasPrefix(f[0], "a=candidate:") || f[1] != "1" || !strings.EqualFold(f[2], "udp") {
			continue
		}
		have[f[4]+":"+f[5]] = true
		if f[7] == "host" && f[4] == local.String() {
			if p, err := strconv.Atoi(f[5]); err == nil && p > 0 && p < 65536 {
				hostPorts = append(hostPorts, uint16(p))
			}
		}
	}
	var extra []string
	for i, port := range hostPorts {
		mctx, cancel := context.WithTimeout(ctx, pmCallTimeout)
		m, err := client.Map(mctx, port, pmLease)
		cancel()
		if err != nil {
			l.log.Debug("router port mapping refused", "port", port, "error", err)
			continue
		}
		if !m.ExternalIP.IsValid() {
			m.ExternalIP = ext
		}
		l.log.Info("router forwards a port for this link", "mapping", m.String())
		go l.keepMapping(client, m)
		key := fmt.Sprintf("%s:%d", m.ExternalIP, m.External)
		if have[key] {
			continue // the router already preserves this port; nothing new to offer
		}
		have[key] = true
		// Priority just below Pion's own srflx candidates.
		extra = append(extra, fmt.Sprintf("a=candidate:lbpm%d 1 udp %d %s %d typ srflx raddr %s rport %d",
			i, 1677721855-i, m.ExternalIP, m.External, local, port))
	}
	if len(extra) == 0 {
		return sdp
	}
	return insertCandidates(sdp, extra)
}

// insertCandidates adds candidate lines after the last existing one (or at
// the end of the media section).
func insertCandidates(sdp string, cands []string) string {
	nl := "\n"
	if strings.Contains(sdp, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.TrimRight(sdp, "\r\n"), nl)
	at := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "a=candidate:") {
			at = i + 1
		}
	}
	out := append([]string{}, lines[:at]...)
	out = append(out, cands...)
	out = append(out, lines[at:]...)
	return strings.Join(out, nl) + nl
}

// keepMapping renews a mapping at half its lifetime while the link lives and
// removes it when the link closes.
func (l *link) keepMapping(c portmap.Client, m portmap.Mapping) {
	renew := m.Lifetime / 2
	if renew <= 0 {
		renew = 30 * time.Minute
	}
	t := time.NewTicker(renew)
	defer t.Stop()
	for {
		select {
		case <-l.closedCh:
			ctx, cancel := context.WithTimeout(context.Background(), pmCallTimeout)
			_ = c.Unmap(ctx, m)
			cancel()
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), pmCallTimeout)
			if _, err := c.Map(ctx, m.Internal, pmLease); err != nil {
				l.log.Debug("could not renew a router port mapping", "error", err)
			}
			cancel()
		}
	}
}
