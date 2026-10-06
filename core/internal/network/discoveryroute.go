package network

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Broadcast and multicast discovery leave Windows through one interface only:
// the one whose 255.255.255.255/32 (or 224.0.0.0/4) route has the lowest
// route + interface metric. Every interface has those routes at metric 256, so
// the interface metric decides - and another virtual LAN (Radmin VPN, Hamachi,
// ZeroTier) that also sets interface metric 1 ties with LanBaz and can win,
// taking every game's "is anyone hosting?" packet with it. Lowering the route
// metric of these two routes on LanBaz's adapter to 0 breaks the tie without
// touching any other interface or any unicast route.

// DiscoveryRouteScript gives alias the lowest-metric broadcast and multicast
// routes and prints which interface now wins 255.255.255.255.
func DiscoveryRouteScript(alias string) string {
	a := strings.ReplaceAll(alias, "'", "''")
	return fmt.Sprintf("$i=Get-NetAdapter -Name '%s' -ErrorAction Stop;"+
		"foreach($d in '255.255.255.255/32','224.0.0.0/4'){"+
		"$r=Get-NetRoute -InterfaceIndex $i.ifIndex -DestinationPrefix $d -AddressFamily IPv4 -ErrorAction SilentlyContinue;"+
		"if($r){$r | Set-NetRoute -RouteMetric 0 -ErrorAction SilentlyContinue}"+
		"else{New-NetRoute -InterfaceIndex $i.ifIndex -DestinationPrefix $d -NextHop '0.0.0.0' -RouteMetric 0 -ErrorAction SilentlyContinue | Out-Null}};"+
		"$b=Find-NetRoute -RemoteIPAddress 255.255.255.255 -ErrorAction SilentlyContinue | Where-Object { $_.InterfaceAlias } | Select-Object -Last 1;"+
		"'winner=' + $b.InterfaceAlias", a)
}

// ParseDiscoveryWinner reads the interface DiscoveryRouteScript reported.
func ParseDiscoveryWinner(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "winner="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// PreferForDiscovery applies DiscoveryRouteScript and logs which interface
// LAN discovery leaves through. Windows recreates an interface's broadcast
// routes when its media comes up, so it is applied again a few seconds later.
func PreferForDiscovery(ctx context.Context, alias string, run func(context.Context, string) (string, error), log *slog.Logger) {
	var winner string
	for _, wait := range []time.Duration{0, 3 * time.Second, 10 * time.Second} {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		out, err := run(ctx, DiscoveryRouteScript(alias))
		if err != nil {
			log.Debug("could not set the discovery routes", "adapter", alias, "error", err, "output", strings.TrimSpace(out))
			continue
		}
		winner = ParseDiscoveryWinner(out)
		if strings.EqualFold(winner, alias) {
			break
		}
	}
	// Find-NetRoute only reports the route table's preference. Winsock may
	// still send a game's broadcast out of LanBaz (and on a PC with Hyper-V
	// it reported "vEthernet (Default Switch)" while every search did go
	// through the room), so this is a hint for the developer, never an error;
	// the traffic lines ("LAN discovery from this PC") are the real evidence.
	if strings.EqualFold(winner, alias) {
		log.Info("LAN discovery routes point at LanBaz", "adapter", alias)
		return
	}
	log.Debug("the route table still prefers another adapter for broadcast; watch for \"LAN discovery from this PC\" lines to see whether games reach LanBaz",
		"adapter", alias, "route_table_prefers", winner)
}
