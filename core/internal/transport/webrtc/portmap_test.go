package webrtc

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network/portmap"
)

type fakeRouter struct{ unmapped chan uint16 }

func (fakeRouter) Method() string { return "UPnP" }
func (fakeRouter) ExternalIP(context.Context) (netip.Addr, error) {
	return netip.MustParseAddr("85.185.78.173"), nil
}
func (fakeRouter) Map(_ context.Context, internal uint16, l time.Duration) (portmap.Mapping, error) {
	return portmap.Mapping{Method: "UPnP", ExternalIP: netip.MustParseAddr("85.185.78.173"), Internal: internal, External: internal + 7, Lifetime: l}, nil
}
func (f fakeRouter) Unmap(_ context.Context, m portmap.Mapping) error {
	f.unmapped <- m.Internal
	return nil
}

// A forwarded port becomes an extra srflx candidate that survives the packed
// code, and the mapping is removed when the link closes.
func TestMappedCandidateIsOfferedAndRemoved(t *testing.T) {
	fr := fakeRouter{unmapped: make(chan uint16, 2)}
	oldGW, oldDisc, oldOff := gatewayFor, discoverRouter, portmapDisabled
	gatewayFor = func() (netip.Addr, netip.Addr) {
		return netip.MustParseAddr("192.168.1.4"), netip.MustParseAddr("192.168.1.1")
	}
	discoverRouter = func(context.Context, netip.Addr, netip.Addr) (portmap.Client, error) { return fr, nil }
	portmapDisabled = false
	router = routerState{}
	t.Cleanup(func() { gatewayFor, discoverRouter, portmapDisabled = oldGW, oldDisc, oldOff; router = routerState{} })

	sdp := "v=0\r\na=ice-ufrag:abcd\r\na=ice-pwd:0123456789abcdefghijkl\r\n" +
		"a=candidate:1 1 udp 2130706431 192.168.1.4 50000 typ host\r\n" +
		"a=candidate:2 1 udp 2130706431 172.20.0.5 50001 typ host\r\n" +
		"a=candidate:3 1 udp 1694498815 80.191.251.140 61000 typ srflx raddr 0.0.0.0 rport 61000\r\n" +
		"a=end-of-candidates\r\n"
	l := &link{log: slog.New(slog.NewTextHandler(io.Discard, nil)), closedCh: make(chan struct{})}
	got := l.withMappedCandidates(context.Background(), sdp)
	if !strings.Contains(got, "85.185.78.173 50007 typ srflx raddr 192.168.1.4 rport 50000\r\n") {
		t.Fatalf("no mapped candidate:\n%s", got)
	}
	if strings.Count(got, "typ srflx") != 2 || strings.Contains(got, "172.20.0.5 50008") {
		t.Fatalf("only the physical adapter's host port is mapped:\n%s", got)
	}
	if strings.Index(got, "lbpm0") > strings.Index(got, "a=end-of-candidates") {
		t.Fatal("candidate added after end-of-candidates")
	}
	full := strings.Replace(got, "v=0\r\n", "v=0\r\na=setup:actpass\r\na=fingerprint:sha-256 "+strings.Repeat("AB:", 31)+"AB\r\n", 1)
	packed, ok := packSDP(full)
	if !ok {
		t.Fatal("could not pack")
	}
	back, err := unpackSDP(packed)
	if err != nil || !strings.Contains(back, "85.185.78.173 50007 typ srflx") {
		t.Fatalf("the mapped candidate did not survive the code: %v\n%s", err, back)
	}
	if close(l.closedCh); <-fr.unmapped != 50000 {
		t.Fatal("mapping not removed on close")
	}
}
