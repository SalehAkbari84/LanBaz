//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	pion "github.com/pion/webrtc/v4"

	"github.com/lanbaz/lanbaz/core/internal/natcheck"
	"github.com/lanbaz/lanbaz/core/internal/network/egress"
	"github.com/lanbaz/lanbaz/core/internal/network/tap"
	"github.com/lanbaz/lanbaz/core/internal/settings"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	lbrtc "github.com/lanbaz/lanbaz/core/internal/transport/webrtc"
)

// layerTAP lists the Classic LAN adapters and whether each can be opened.
func layerTAP(r *report) {
	r.section("3. TAP adapters (Classic LAN rooms only)")
	list := tap.Probe()
	if len(list) == 0 {
		r.info("no TAP-Windows adapter installed (only needed for Classic LAN rooms)")
		return
	}
	usable := false
	for _, p := range list {
		state := "opens"
		if !p.Opened {
			state = "cannot open: " + p.Error
		}
		use := "ignored (belongs to another program)"
		if p.Eligible {
			use = "LanBaz may use it"
		}
		r.info("%q  [%s]  %s; %s", p.Alias, p.Description, use, state)
		if p.Eligible && p.Opened {
			usable = true
		}
	}
	if usable {
		r.ok("a TAP adapter LanBaz can use is available")
	} else {
		r.warn("no TAP adapter LanBaz can use right now; Classic LAN rooms would fail (normal rooms are unaffected)")
	}
}

// layerInternet checks what decides whether two PCs can reach each other.
func layerInternet(r *report) {
	r.section("4. Internet, STUN and NAT")

	for _, host := range []string{"stun.cloudflare.com", "global.stun.twilio.com", "stun.nextcloud.com", "www.google.com"} {
		t := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		cancel()
		if err != nil {
			r.warn("DNS %s: %v", host, err)
			continue
		}
		r.ok("DNS %s -> %v (%s)", host, ips[0], time.Since(t).Round(time.Millisecond))
	}

	if idx, name := egress.Physical(); idx > 0 {
		r.ok("real internet adapter: %q (index %d); LanBaz sends its traffic through it even while a VPN is connected", name, idx)
	} else {
		r.warn("could not identify the real internet adapter; LanBaz traffic follows the routing table (and any VPN)")
	}
	rep := natcheck.Check(context.Background(), natcheck.Candidates, 4*time.Second)
	var b strings.Builder
	publicIPs := map[string]int{}
	var mapped []mp
	for _, x := range rep.Results {
		if x.OK {
			fmt.Fprintf(&b, "ok    %-40s %5d ms  %s\n", strings.TrimPrefix(x.Server, "stun:"), x.RTT.Milliseconds(), x.Mapped)
			if ap, err := netAddrPort(x.Mapped); err == nil {
				publicIPs[ap.ip]++
				mapped = append(mapped, mp{x.Server, ap.port})
			}
		} else {
			fmt.Fprintf(&b, "fail  %-40s %s\n", strings.TrimPrefix(x.Server, "stun:"), x.Error)
		}
	}
	r.info("STUN servers from one local UDP port (%d):", rep.LocalPort)
	r.block(b.String())
	switch rep.NAT {
	case natcheck.NATBlocked:
		r.fail("no STUN server answered: UDP to the internet is blocked here; only a TURN relay over TCP could work")
	case natcheck.NATSymmetric:
		r.warn("NAT is SYMMETRIC: a new public port for every destination. A direct link works only if the friend's NAT is open or full/address-restricted cone")
	case natcheck.NATCone:
		r.ok("NAT is CONE: the same public port for every destination - good for direct links")
	case natcheck.NATNone:
		r.ok("no NAT: this PC has a public address")
	default:
		r.warn("NAT type unknown (too few servers answered)")
	}
	settingsWorking := 0
	for _, s := range settings.DefaultSTUN {
		for _, w := range rep.Working {
			if w == s {
				settingsWorking++
			}
		}
	}
	if settingsWorking == 0 {
		r.fail("none of LanBaz's default STUN servers answered")
	} else {
		r.ok("%d of %d LanBaz default STUN servers answer", settingsWorking, len(settings.DefaultSTUN))
	}
	if len(publicIPs) > 1 {
		var ips []string
		for ip, n := range publicIPs {
			ips = append(ips, fmt.Sprintf("%s (%d servers)", ip, n))
		}
		sort.Strings(ips)
		r.warn("different servers see DIFFERENT public addresses: %s. Some traffic leaves through a VPN/proxy or a second link; peers may get an address that does not lead back to this PC", strings.Join(ips, ", "))
	}
	if len(mapped) > 0 {
		preserved := 0
		var ports []string
		for _, m := range mapped {
			if m.port == rep.LocalPort {
				preserved++
			}
			ports = append(ports, fmt.Sprint(m.port))
		}
		r.info("public ports in order of query: %s", strings.Join(ports, " "))
		if preserved == len(mapped) {
			r.ok("the NAT keeps the local port as the public port (port preserving)")
		}
		if rep.NAT == natcheck.NATSymmetric {
			r.info("port allocation: %s", portPattern(mapped))
		}
	}

	// A second socket shows whether the mapping is stable per socket.
	rep2 := natcheck.Check(context.Background(), []string{"stun:stun.cloudflare.com:3478", "stun:global.stun.twilio.com:3478"}, 3*time.Second)
	var again []string
	for _, x := range rep2.Results {
		if x.OK {
			again = append(again, x.Mapped)
		}
	}
	r.info("second socket (local port %d) mapped to: %s", rep2.LocalPort, strings.Join(again, ", "))

	// TCP paths, which a TURN relay over TCP/TLS would use.
	for _, hp := range []string{"stun.nextcloud.com:443", "1.1.1.1:443", "stun.cloudflare.com:3478"} {
		t := time.Now()
		c, err := net.DialTimeout("tcp4", hp, 4*time.Second)
		if err != nil {
			r.warn("TCP %s: %v", hp, err)
			continue
		}
		_ = c.Close()
		r.ok("TCP %s reachable (%s)", hp, time.Since(t).Round(time.Millisecond))
	}

	iceGather(r)
}

type mp struct {
	server string
	port   int
}

type addrPort struct {
	ip   string
	port int
}

func netAddrPort(s string) (addrPort, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return addrPort{}, err
	}
	var port int
	_, err = fmt.Sscan(p, &port)
	return addrPort{h, port}, err
}

// portPattern says whether a symmetric NAT hands out ports predictably.
func portPattern(m []mp) string {
	if len(m) < 3 {
		return "too few answers"
	}
	small := 0
	for i := 1; i < len(m); i++ {
		d := m[i].port - m[i-1].port
		if d > 0 && d <= 10 {
			small++
		}
	}
	if small >= len(m)-2 {
		return "sequential (predictable)"
	}
	return "random (unpredictable: hole punching from this side cannot guess ports)"
}

// iceGather does what an invite does: gather candidates with the default
// STUN servers, and times it.
func iceGather(r *report) {
	var servers []pion.ICEServer
	for _, s := range settings.DefaultSTUN {
		servers = append(servers, pion.ICEServer{URLs: []string{s}})
	}
	se := pion.SettingEngine{}
	se.SetInterfaceFilter(func(n string) bool { return !strings.HasPrefix(n, "LanBaz") })
	if n := lbrtc.NewBypassNet(); n != nil {
		se.SetNet(n)
	}
	pc, err := pion.NewAPI(pion.WithSettingEngine(se)).NewPeerConnection(pion.Configuration{ICEServers: servers})
	if err != nil {
		r.fail("ICE: %v", err)
		return
	}
	defer pc.Close()
	_, _ = pc.CreateDataChannel("doctor", nil)
	var mu sync.Mutex
	counts := map[string]int{}
	var firstPublic time.Duration
	start := time.Now()
	pc.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		mu.Lock()
		counts[c.Typ.String()]++
		if c.Typ == pion.ICECandidateTypeSrflx && firstPublic == 0 {
			firstPublic = time.Since(start)
		}
		mu.Unlock()
	})
	done := pion.GatheringCompletePromise(pc)
	offer, _ := pc.CreateOffer(nil)
	_ = pc.SetLocalDescription(offer)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		r.warn("ICE gathering did not finish within 15 s")
	}
	mu.Lock()
	defer mu.Unlock()
	r.info("ICE gathering took %s: host=%d srflx=%d relay=%d", time.Since(start).Round(time.Millisecond),
		counts["host"], counts["srflx"], counts["relay"])
	if counts["srflx"] == 0 {
		r.fail("no public (srflx) candidate: an invite from this PC only works on the same LAN")
	} else {
		r.ok("first public candidate after %s", firstPublic.Round(time.Millisecond))
	}
}

// layerLocalWebRTC connects two LanBaz transports on this PC, proving that
// WebRTC, DTLS and the data channels work here at all.
func layerLocalWebRTC(r *report) {
	r.section("5. WebRTC link on this PC (two local endpoints)")
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(fileOnly{r}, nil)))
	defer slog.SetDefault(prev)
	key := func(b byte) []byte {
		k := make([]byte, 64)
		for i := range k {
			k[i] = b
		}
		return k
	}
	mk := func(b byte) (*lbrtc.Transport, error) {
		return lbrtc.New(lbrtc.Options{RoomID: "doctor", PrivateKey: key(b), STUNServers: []string{}, MTU: 1200})
	}
	host, err := mk(1)
	if err != nil {
		r.fail("%v", err)
		return
	}
	guest, err := mk(2)
	if err != nil {
		r.fail("%v", err)
		return
	}
	defer host.Shutdown(context.Background())
	defer guest.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t := time.Now()
	hp := transport.PeerInfo{ID: "guest", RoomID: "doctor"}
	gp := transport.PeerInfo{ID: "host", RoomID: "doctor"}
	offer, err := host.CreateOffer(ctx, hp)
	if err != nil {
		r.fail("create invite: %v", err)
		return
	}
	answer, err := guest.AcceptOffer(ctx, gp, offer)
	if err != nil {
		r.fail("create reply: %v", err)
		return
	}
	if err := host.ApplyAnswer(ctx, hp, answer); err != nil {
		r.fail("connect: %v", err)
		return
	}
	r.ok("link up in %s (invite, reply, ICE, DTLS, data channels)", time.Since(t).Round(time.Millisecond))

	pong := make(chan time.Time, 8)
	guest.SetControlHandler(func(id transport.PeerID, p []byte) { _ = guest.SendControl(id, p) })
	host.SetControlHandler(func(transport.PeerID, []byte) { pong <- time.Now() })
	var rtts []time.Duration
	for i := 0; i < 5; i++ {
		for len(pong) > 0 {
			<-pong
		}
		s := time.Now()
		if err := host.SendControl(hp.ID, []byte("ping")); err != nil {
			r.fail("send: %v", err)
			return
		}
		select {
		case at := <-pong:
			rtts = append(rtts, at.Sub(s))
		case <-time.After(3 * time.Second):
			r.fail("a message over the link got no answer")
			return
		}
	}
	r.ok("5 messages echoed, round trips %v", rtts)
}
