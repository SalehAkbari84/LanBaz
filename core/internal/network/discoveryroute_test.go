package network

import (
	"strings"
	"testing"
)

func TestDiscoveryRouteScript(t *testing.T) {
	s := DiscoveryRouteScript("LanBaz-ab'cd")
	for _, want := range []string{"'LanBaz-ab''cd'", "255.255.255.255/32", "224.0.0.0/4", "-RouteMetric 0", "winner="} {
		if !strings.Contains(s, want) {
			t.Fatalf("script lacks %q:\n%s", want, s)
		}
	}
	if got := ParseDiscoveryWinner("WARNING: x\r\nwinner=Radmin VPN\r\n"); got != "Radmin VPN" {
		t.Fatalf("winner = %q", got)
	}
}
