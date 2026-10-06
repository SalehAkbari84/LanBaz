package natcheck

import (
	"context"
	"net/netip"
	"os"
	"testing"
	"time"
)

func res(server, mapped string) Result {
	m := netip.MustParseAddrPort(mapped)
	return Result{Server: server, OK: true, Mapped: mapped, mapping: m}
}

func TestClassify(t *testing.T) {
	if got := classify([]Result{res("a", "1.2.3.4:5000"), res("b", "1.2.3.4:5000")}); got != NATCone {
		t.Fatalf("same mapping: got %s, want cone", got)
	}
	if got := classify([]Result{res("a", "1.2.3.4:5000"), res("b", "1.2.3.4:5001")}); got != NATSymmetric {
		t.Fatalf("different ports: got %s, want symmetric", got)
	}
}

func TestSummariseBlocked(t *testing.T) {
	rep := summarise([]Result{{Server: "a", Error: "no answer"}}, nil)
	if rep.NAT != NATBlocked || len(rep.Working) != 0 {
		t.Fatalf("got %+v, want blocked", rep)
	}
}

// TestLiveServers probes real STUN servers. It only runs when asked:
//
//	LANBAZ_LIVE_STUN=1 go test ./core/internal/natcheck -run Live -v
func TestLiveServers(t *testing.T) {
	if os.Getenv("LANBAZ_LIVE_STUN") == "" {
		t.Skip("set LANBAZ_LIVE_STUN=1 to probe real servers")
	}
	servers := Candidates
	if extra := os.Getenv("LANBAZ_STUN_EXTRA"); extra != "" {
		servers = append(append([]string(nil), servers...), extra)
	}
	rep := Check(context.Background(), servers, 4*time.Second)
	for _, r := range rep.Results {
		t.Logf("%-45s ok=%-5v rtt=%-8v mapped=%-22s %s", r.Server, r.OK, r.RTT.Round(time.Millisecond), r.Mapped, r.Error)
	}
	t.Logf("NAT=%s public=%s working=%v", rep.NAT, rep.PublicIP, rep.Working)
}
