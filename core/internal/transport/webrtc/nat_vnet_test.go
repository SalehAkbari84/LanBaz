package webrtc

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/turn/v5"

	"github.com/lanbaz/lanbaz/core/internal/transport"
)

// These tests put two LanBaz transports behind real (simulated) home routers:
// port-restricted cone NATs, the kind both players in the first field test
// had. The guest's reply reaches the host only after the NAT has forgotten
// the guest's first packets - which is what happens when a person carries the
// reply by hand.

const (
	labUser     = "lanbaz"
	labPass     = "relay-pass"
	vnetSTUN    = "1.2.3.4"
	natLifetime = 4 * time.Second
	replyDelay  = 10 * time.Second // > natLifetime, like a human copying a code
)

type natLab struct {
	wan        *vnet.Router
	host, gues *vnet.Net
	stun       *turn.Server
}

func (l *natLab) close() {
	_ = l.stun.Close()
	_ = l.wan.Stop()
}

func buildNATLab(t *testing.T) *natLab {
	t.Helper()
	return buildNATLabWith(t, vnet.EndpointIndependent)
}

// buildNATLabWith builds the lab with the given router mapping behaviour;
// EndpointAddrPortDependent mapping is a symmetric NAT.
func buildNATLabWith(t *testing.T, mapping vnet.EndpointDependencyType) *natLab {
	t.Helper()
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelError
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: lf})
	if err != nil {
		t.Fatal(err)
	}
	wanNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{vnetSTUN}})
	if err != nil {
		t.Fatal(err)
	}
	if err := wan.AddNet(wanNet); err != nil {
		t.Fatal(err)
	}
	homeRouter := func(public, lan string) *vnet.Net {
		r, err := vnet.NewRouter(&vnet.RouterConfig{
			StaticIPs:     []string{public},
			LoggerFactory: lf,
			CIDR:          lan + "/24",
			NATType: &vnet.NATType{
				MappingBehavior:   mapping,
				FilteringBehavior: vnet.EndpointAddrPortDependent, // port-restricted
				MappingLifeTime:   natLifetime,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{lan}})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.AddNet(n); err != nil {
			t.Fatal(err)
		}
		if err := wan.AddRouter(r); err != nil {
			t.Fatal(err)
		}
		return n
	}
	host := homeRouter("27.1.1.1", "192.168.0.2")
	guest := homeRouter("28.1.1.1", "192.168.1.4")
	if err := wan.Start(); err != nil {
		t.Fatal(err)
	}
	pc, err := wanNet.ListenPacket("udp", vnetSTUN+":3478")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm:         "lanbaz",
		LoggerFactory: lf,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			if ra.Username != labUser {
				return "", nil, false
			}
			return ra.Username, turn.GenerateAuthKey(labUser, ra.Realm, labPass), true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP(vnetSTUN), Address: "0.0.0.0", Net: wanNet,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &natLab{wan: wan, host: host, gues: guest, stun: srv}
}

func natTransport(t *testing.T, n *vnet.Net, b byte, limit uint16, keep time.Duration) *Transport {
	t.Helper()
	return natTransportTURN(t, n, b, limit, keep, nil)
}

func natTransportTURN(t *testing.T, n *vnet.Net, b byte, limit uint16, keep time.Duration, turnSrv []TURNServer) *Transport {
	t.Helper()
	tr, err := New(Options{
		TURNServers: turnSrv,
		RoomID:      "room",
		PrivateKey:  testKey(b),
		STUNServers: []string{fmt.Sprintf("stun:%s:3478", vnetSTUN)},
		MTU:         1200,
		net:         n,
		checkLimit:  limit,
		keepalive:   keep,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.pending = time.Minute
	return tr
}

// lateReply runs the hand-carried handshake through the NATs and reports
// whether the link came up.
func lateReply(t *testing.T, limit uint16, keep, delay time.Duration) bool {
	lab := buildNATLab(t)
	defer lab.close()
	host := natTransport(t, lab.host, 1, limit, keep)
	guest := natTransport(t, lab.gues, 2, limit, keep)
	defer host.Shutdown(context.Background())
	defer guest.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hp := transport.PeerInfo{ID: "guest", RoomID: "room"}
	gp := transport.PeerInfo{ID: "host", RoomID: "room"}
	offer, err := host.CreateOffer(ctx, hp)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	answer, err := guest.AcceptOffer(ctx, gp, offer)
	if err != nil {
		t.Fatalf("AcceptOffer: %v", err)
	}
	if s, err := decodeDescription(offer.Data); err == nil {
		t.Logf("invite: %s", candidateSummary(s))
	}
	if s, err := decodeDescription(answer.Data); err == nil {
		t.Logf("reply: %s", candidateSummary(s))
	}
	time.Sleep(delay)
	if err := host.ApplyAnswer(ctx, hp, answer); err != nil {
		t.Logf("ApplyAnswer: %v", err)
		return false
	}
	return guest.Ready(gp.ID) || waitUsable(ctx, guest, gp.ID) == nil
}

// The fix: every path keeps being checked, so the guest is still sending when
// the host finally starts, and the port-restricted NATs open both ways.
func TestLateReplyConnectsThroughPortRestrictedNATs(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the NAT to forget mappings")
	}
	if !lateReply(t, 0, time.Second, replyDelay) {
		t.Fatal("the link did not come up through two port-restricted NATs with a late reply")
	}
}

// The regression this guards: 0.3.4's behaviour - 7 checks per path and no
// keepalive - fails the same handshake, exactly as the two-PC field test did
// (every path "failed" after 8 checks, nothing ever received).
func TestOldBehaviourFailsThroughPortRestrictedNATs(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the NAT to forget mappings")
	}
	if lateReply(t, 7, time.Hour, replyDelay) {
		t.Fatal("the old settings connected; the lab no longer reproduces the field failure")
	}
}

func TestNATLabConnectsImmediately(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	if !lateReply(t, 0, time.Second, 0) {
		t.Fatal("the NAT lab cannot connect even without delay")
	}
}

// connectPair runs a prompt handshake through the lab and reports whether the
// link came up and whether it went through the relay.
func connectPair(t *testing.T, lab *natLab, hostTURN []TURNServer) (ok, relayed bool) {
	host := natTransportTURN(t, lab.host, 1, 0, time.Second, hostTURN)
	guest := natTransportTURN(t, lab.gues, 2, 0, time.Second, nil)
	defer host.Shutdown(context.Background())
	defer guest.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	hp := transport.PeerInfo{ID: "guest", RoomID: "room"}
	gp := transport.PeerInfo{ID: "host", RoomID: "room"}
	offer, err := host.CreateOffer(ctx, hp)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	answer, err := guest.AcceptOffer(ctx, gp, offer)
	if err != nil {
		t.Fatalf("AcceptOffer: %v", err)
	}
	_ = host.ApplyAnswer(ctx, hp, answer)
	if waitUsable(ctx, guest, gp.ID) != nil {
		return false, false
	}
	l, _ := host.link(hp.ID)
	deadline := time.Now().Add(5 * time.Second)
	for l.selectedLocal.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	return true, !l.direct.Load()
}

// Two symmetric NATs: no direct path can work. Without a relay the link
// fails; with a relay on ONE side only, ICE falls back to it by itself.
func TestRelayFallbackThroughTwoSymmetricNATs(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	t.Run("no relay", func(t *testing.T) {
		lab := buildNATLabWith(t, vnet.EndpointAddrPortDependent)
		defer lab.close()
		if ok, _ := connectPair(t, lab, nil); ok {
			t.Skip("the lab let a direct path through two symmetric NATs; nothing to prove")
		}
	})
	t.Run("relay on the host", func(t *testing.T) {
		lab := buildNATLabWith(t, vnet.EndpointAddrPortDependent)
		defer lab.close()
		relay := []TURNServer{{URLs: []string{"turn:" + vnetSTUN + ":3478"}, Username: labUser, Credential: labPass}}
		ok, relayed := connectPair(t, lab, relay)
		if !ok {
			t.Fatal("the link did not fall back to the relay")
		}
		if !relayed {
			t.Error("connected, but not through the relay")
		}
	})
}
