package webrtc

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// TURNProbe is one relay's result.
type TURNProbe struct {
	URL   string
	OK    bool
	Relay string
	RTT   time.Duration
	Error string
}

// ProbeTURN asks each relay for an allocation, the same way a real link
// would, and reports which ones handed out a relay address.
func ProbeTURN(ctx context.Context, servers []TURNServer, timeout time.Duration) []TURNProbe {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	var (
		mu  sync.Mutex
		out []TURNProbe
		wg  sync.WaitGroup
	)
	for _, srv := range servers {
		for _, u := range srv.URLs {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			wg.Add(1)
			go func(u string, srv TURNServer) {
				defer wg.Done()
				r := probeOne(ctx, u, srv, timeout)
				mu.Lock()
				out = append(out, r)
				mu.Unlock()
			}(u, srv)
		}
	}
	wg.Wait()
	return out
}

func probeOne(ctx context.Context, url string, srv TURNServer, timeout time.Duration) TURNProbe {
	res := TURNProbe{URL: url, Error: "no relay address within " + timeout.String()}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers:         []webrtc.ICEServer{{URLs: []string{url}, Username: srv.Username, Credential: srv.Credential}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	})
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer pc.Close()
	if _, err := pc.CreateDataChannel("probe", nil); err != nil {
		res.Error = err.Error()
		return res
	}
	got := make(chan string, 4)
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil && c.Typ == webrtc.ICECandidateTypeRelay {
			select {
			case got <- c.Address:
			default:
			}
		}
	})
	start := time.Now()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		res.Error = err.Error()
		return res
	}
	select {
	case a := <-got:
		res.OK, res.Relay, res.RTT, res.Error = true, a, time.Since(start), ""
	case <-time.After(timeout):
	case <-ctx.Done():
		res.Error = "cancelled"
	}
	return res
}
