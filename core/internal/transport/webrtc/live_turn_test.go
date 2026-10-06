package webrtc

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// TestLiveTURN checks that TURN servers hand out relay candidates. Only runs
// when asked:
//
//	LANBAZ_LIVE_TURN="turn:host:3478|user|pass;turn:host:443?transport=tcp|user|pass" go test -run LiveTURN -v ./core/internal/transport/webrtc
func TestLiveTURN(t *testing.T) {
	spec := os.Getenv("LANBAZ_LIVE_TURN")
	if spec == "" {
		t.Skip("set LANBAZ_LIVE_TURN to probe real TURN servers")
	}
	for _, one := range strings.Split(spec, ";") {
		parts := strings.SplitN(one, "|", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
			ICEServers:         []webrtc.ICEServer{{URLs: []string{parts[0]}, Username: parts[1], Credential: parts[2]}},
			ICETransportPolicy: webrtc.ICETransportPolicyRelay,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pc.CreateDataChannel("x", nil); err != nil {
			t.Fatal(err)
		}
		got := make(chan string, 8)
		pc.OnICECandidate(func(c *webrtc.ICECandidate) {
			if c != nil && c.Typ == webrtc.ICECandidateTypeRelay {
				got <- c.Address
			}
		})
		offer, _ := pc.CreateOffer(nil)
		_ = pc.SetLocalDescription(offer)
		select {
		case a := <-got:
			t.Logf("%-50s relay OK: %s", parts[0], a)
		case <-time.After(8 * time.Second):
			t.Logf("%-50s NO relay candidate", parts[0])
		}
		_ = pc.Close()
	}
}
