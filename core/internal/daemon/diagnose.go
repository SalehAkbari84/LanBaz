package daemon

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/natcheck"
	"github.com/lanbaz/lanbaz/core/internal/settings"
	"github.com/lanbaz/lanbaz/core/internal/transport/webrtc"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// diagnoseExtra are STUN servers from other operators, so the NAT type can be
// told even when the configured servers are few or filtered.
var diagnoseExtra = []string{
	"stun:stun.sipgate.net:3478",
	"stun:stun.voip.blackberry.com:3478",
	"stun:stun.l.google.com:19302",
}

// diagnose runs the connection check behind network.diagnose.
func (d *Daemon) diagnose(ctx context.Context) protocol.DiagnoseReport {
	start := time.Now()
	st := d.settings.Get()
	servers := settings.EffectiveSTUN(st)
	seen := map[string]bool{}
	for _, s := range servers {
		seen[strings.TrimSpace(s)] = true
	}
	for _, s := range diagnoseExtra {
		if !seen[s] {
			servers = append(servers, s)
		}
	}
	var relays []webrtc.TURNServer
	d.meteredMu.Lock()
	for _, r := range d.metered {
		relays = append(relays, webrtc.TURNServer{URLs: []string{r.URL}, Username: r.Username, Credential: r.Credential})
	}
	d.meteredMu.Unlock()
	for _, r := range st.TURNServers {
		if u := strings.TrimSpace(r.URL); u != "" {
			relays = append(relays, webrtc.TURNServer{URLs: []string{u}, Username: r.Username, Credential: r.Credential})
		}
	}

	var (
		wg   sync.WaitGroup
		nat  natcheck.Report
		turn []webrtc.TURNProbe
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		nat = natcheck.Check(ctx, servers, 4*time.Second)
	}()
	if len(relays) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			turn = webrtc.ProbeTURN(ctx, relays, 8*time.Second)
		}()
	}
	wg.Wait()
	rep := buildReport(nat, turn, st.AllowRelay || len(relays) > 0, time.Since(start))
	rep.PortMapping = webrtc.RouterPortMapping(ctx, d.log)
	d.log.Info("connection check", "nat", rep.NAT, "public_ip", rep.PublicIP,
		"working_stun", len(rep.Working), "advice", rep.Advice)
	return rep
}

// buildReport turns the raw results into the API payload and its advice.
func buildReport(nat natcheck.Report, turn []webrtc.TURNProbe, allowRelay bool, took time.Duration) protocol.DiagnoseReport {
	rep := protocol.DiagnoseReport{
		NAT:        string(nat.NAT),
		PublicIP:   nat.PublicIP,
		Working:    append([]string{}, nat.Working...),
		STUN:       []protocol.DiagnoseServer{},
		TURN:       []protocol.DiagnoseServer{},
		DurationMS: took.Milliseconds(),
	}
	for _, r := range nat.Results {
		rep.STUN = append(rep.STUN, protocol.DiagnoseServer{
			Server: r.Server, OK: r.OK, Mapped: r.Mapped, RTTMS: r.RTT.Milliseconds(), Error: r.Error,
		})
	}
	turnOK := false
	for _, t := range turn {
		rep.TURN = append(rep.TURN, protocol.DiagnoseServer{
			Server: t.URL, OK: t.OK, Mapped: t.Relay, RTTMS: t.RTT.Milliseconds(), Error: t.Error,
		})
		turnOK = turnOK || t.OK
	}
	relayReady := turnOK && allowRelay
	switch {
	case len(turn) > 0 && !turnOK:
		rep.Advice = "turn_broken"
	case nat.NAT == natcheck.NATBlocked && !relayReady:
		rep.Advice = "stun_blocked"
	case nat.NAT == natcheck.NATSymmetric && !relayReady:
		rep.Advice = "needs_turn"
	default:
		rep.Advice = "ok"
	}
	return rep
}
