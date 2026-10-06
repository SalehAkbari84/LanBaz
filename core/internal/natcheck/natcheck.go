// Package natcheck asks STUN servers what this machine's public address is.
//
// It answers the two questions behind almost every "we could not connect":
// can this PC reach any STUN server at all (some networks filter them), and
// does its NAT keep one public port per local socket (cone, punchable) or hand
// out a new one per destination (symmetric, needs a relay).
package natcheck

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun/v4"

	"github.com/lanbaz/lanbaz/core/internal/network/egress"
)

// Result is one server's answer.
type Result struct {
	Server  string        `json:"server"`
	OK      bool          `json:"ok"`
	Mapped  string        `json:"mapped,omitempty"`
	RTT     time.Duration `json:"rtt_ns,omitempty"`
	Error   string        `json:"error,omitempty"`
	mapping netip.AddrPort
}

// NAT classifies the mapping behaviour seen across servers.
type NAT string

const (
	// NATUnknown: fewer than two servers answered.
	NATUnknown NAT = "unknown"
	// NATNone: the public address is the local one.
	NATNone NAT = "open"
	// NATCone: every server saw the same public port; direct links work.
	NATCone NAT = "cone"
	// NATSymmetric: each server saw a different port; direct links usually
	// fail unless the other side is open or cone.
	NATSymmetric NAT = "symmetric"
	// NATBlocked: no server answered; STUN is filtered on this network.
	NATBlocked NAT = "blocked"
)

// Report is the whole check.
type Report struct {
	Results  []Result `json:"results"`
	PublicIP string   `json:"public_ip,omitempty"`
	NAT      NAT      `json:"nat"`
	// Working lists the servers that answered, fastest first.
	Working []string `json:"working"`
	// LocalPort is the UDP port the check used on this PC; a NAT that keeps
	// it as the public port is "port preserving".
	LocalPort int `json:"local_port,omitempty"`
}

// Check queries every server from ONE local UDP socket, which is what makes the
// NAT classification meaningful: the same socket mapped to different public
// ports by different servers is the definition of a symmetric NAT.
func Check(ctx context.Context, servers []string, timeout time.Duration) Report {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	conn, err := egress.ListenUDP("udp4", nil)
	if err != nil {
		rep := Report{NAT: NATUnknown}
		for _, s := range servers {
			rep.Results = append(rep.Results, Result{Server: s, Error: err.Error()})
		}
		return rep
	}
	defer conn.Close()

	type pending struct {
		idx  int
		sent time.Time
	}
	var (
		mu      sync.Mutex
		waiting = map[[stun.TransactionIDSize]byte]pending{}
		results = make([]Result, len(servers))
	)
	for i, s := range servers {
		results[i] = Result{Server: s, Error: "no answer"}
	}

	// Reader: match responses to requests by transaction id.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			m := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if m.Decode() != nil {
				continue
			}
			var xor stun.XORMappedAddress
			var mapped netip.AddrPort
			if xor.GetFrom(m) == nil {
				if a, ok := netip.AddrFromSlice(xor.IP); ok {
					mapped = netip.AddrPortFrom(a.Unmap(), uint16(xor.Port))
				}
			} else {
				var plain stun.MappedAddress
				if plain.GetFrom(m) == nil {
					if a, ok := netip.AddrFromSlice(plain.IP); ok {
						mapped = netip.AddrPortFrom(a.Unmap(), uint16(plain.Port))
					}
				}
			}
			mu.Lock()
			p, ok := waiting[m.TransactionID]
			if ok && mapped.IsValid() {
				delete(waiting, m.TransactionID)
				results[p.idx] = Result{
					Server: servers[p.idx], OK: true, Mapped: mapped.String(),
					RTT: time.Since(p.sent), mapping: mapped,
				}
			}
			mu.Unlock()
		}
	}()

	send := func(i int) {
		addr, err := resolve(ctx, servers[i])
		if err != nil {
			mu.Lock()
			results[i].Error = err.Error()
			mu.Unlock()
			return
		}
		m := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
		mu.Lock()
		waiting[m.TransactionID] = pending{idx: i, sent: time.Now()}
		mu.Unlock()
		_, _ = conn.WriteToUDP(m.Raw, addr)
	}
	// Two attempts: UDP loses packets, and a single loss should not mark a
	// working server as blocked.
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func(i int) { defer wg.Done(); send(i) }(i)
	}
	wg.Wait()
	deadline := time.Now().Add(timeout)
	retry := time.NewTimer(timeout / 2)
	select {
	case <-retry.C:
		mu.Lock()
		var again []int
		for i := range servers {
			if !results[i].OK && results[i].Error == "no answer" {
				again = append(again, i)
			}
		}
		mu.Unlock()
		for _, i := range again {
			send(i)
		}
	case <-ctx.Done():
		retry.Stop()
	}
	select {
	case <-time.After(time.Until(deadline)):
	case <-ctx.Done():
	}
	_ = conn.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	return summarise(results, conn.LocalAddr())
}

func summarise(results []Result, local net.Addr) Report {
	rep := Report{Results: results, NAT: NATUnknown}
	if ua, isUDP := local.(*net.UDPAddr); isUDP && ua != nil {
		rep.LocalPort = ua.Port
	}
	var ok []Result
	for _, r := range results {
		if r.OK {
			ok = append(ok, r)
		}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].RTT < ok[j].RTT })
	for _, r := range ok {
		rep.Working = append(rep.Working, r.Server)
	}
	switch {
	case len(ok) == 0:
		rep.NAT = NATBlocked
		return rep
	default:
		rep.PublicIP = ok[0].mapping.Addr().String()
	}
	if ua, isUDP := local.(*net.UDPAddr); isUDP && ua != nil {
		if a, valid := netip.AddrFromSlice(ua.IP); valid && a.Unmap() == ok[0].mapping.Addr() {
			rep.NAT = NATNone
			return rep
		}
	}
	if len(ok) < 2 {
		return rep
	}
	rep.NAT = classify(ok)
	return rep
}

// classify compares the mappings servers reported for one socket.
func classify(ok []Result) NAT {
	first := ok[0].mapping
	for _, r := range ok[1:] {
		if r.mapping != first {
			return NATSymmetric
		}
	}
	return NATCone
}

// resolve turns "stun:host:port" (or "host:port") into a UDP address.
func resolve(ctx context.Context, server string) (*net.UDPAddr, error) {
	hostport := strings.TrimPrefix(strings.TrimPrefix(server, "stun:"), "stuns:")
	if i := strings.IndexByte(hostport, '?'); i >= 0 {
		hostport = hostport[:i]
	}
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		hostport = net.JoinHostPort(hostport, "3478")
	}
	host, port, _ := net.SplitHostPort(hostport)
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip4", host)
	if err != nil {
		return nil, errors.New("cannot resolve " + host)
	}
	if len(ips) == 0 {
		return nil, errors.New("no IPv4 address for " + host)
	}
	return net.ResolveUDPAddr("udp4", net.JoinHostPort(ips[0].String(), port))
}

// Candidates is a broad list of public STUN servers from many operators, used
// by the connection check. Networks that filter one operator rarely filter all.
var Candidates = []string{
	"stun:stun.l.google.com:19302",
	"stun:stun1.l.google.com:19302",
	"stun:stun.cloudflare.com:3478",
	"stun:global.stun.twilio.com:3478",
	"stun:stun.nextcloud.com:443",
	"stun:stun.nextcloud.com:3478",
	"stun:stun.sipgate.net:3478",
	"stun:stun.ekiga.net:3478",
	"stun:stun.voipgate.com:3478",
	"stun:stun.stunprotocol.org:3478",
	"stun:stun.miwifi.com:3478",
	"stun:stun.qq.com:3478",
	"stun:stun.syncthing.net:3478",
	"stun:stun.framasoft.org:3478",
	"stun:stun.voip.blackberry.com:3478",
	"stun:stun.antisip.com:3478",
	"stun:stun.sonetel.com:3478",
	"stun:stun.zoiper.com:3478",
	"stun:stun.bethesda.net:3478",
	"stun:stun.services.mozilla.com:3478",
}
