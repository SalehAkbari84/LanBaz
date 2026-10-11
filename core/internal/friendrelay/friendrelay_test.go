package friendrelay

import (
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestCodeRoundTrip(t *testing.T) {
	c := Code{V: 1, Host: "5.160.1.2", Port: 3478, User: "lanbaz", Pass: NewSecret(), Name: "Ali"}
	s := c.Encode()
	if !strings.HasPrefix(s, "lanbaz-relay:") {
		t.Fatalf("code = %s", s)
	}
	got, err := ParseCode("  " + s + "\n")
	if err != nil || got != c {
		t.Fatalf("parse: %+v %v", got, err)
	}
	stun, turn := got.Servers()
	if stun != "stun:5.160.1.2:3478" || turn.URL != "turn:5.160.1.2:3478?transport=udp" || turn.Username != "lanbaz" || turn.Credential != c.Pass {
		t.Fatalf("servers: %s %+v", stun, turn)
	}
	for _, bad := range []string{"", "hello", "lanbaz-relay:", "lanbaz-relay:!!!", (Code{V: 2, Host: "1.2.3.4", Port: 1, User: "u", Pass: "p"}).Encode(), (Code{V: 1, Host: "not-an-ip", Port: 1, User: "u", Pass: "p"}).Encode(), (Code{V: 1, Host: "1.2.3.4", Port: 0, User: "u", Pass: "p"}).Encode()} {
		if _, err := ParseCode(bad); err == nil {
			t.Errorf("ParseCode(%q) accepted", bad)
		}
	}
}

func TestLimiterCapsThroughput(t *testing.T) {
	l := newLimiter(100_000) // 100 kB/s, burst 25 kB
	sent := 0
	for i := 0; i < 1000; i++ {
		if l.allow(1000) {
			sent += 1000
		}
	}
	if sent > 26_000 || sent < 24_000 {
		t.Fatalf("a burst let through %d bytes", sent)
	}
	time.Sleep(200 * time.Millisecond) // +20 kB
	more := 0
	for i := 0; i < 100; i++ {
		if l.allow(1000) {
			more += 1000
		}
	}
	if more < 15_000 || more > 26_000 {
		t.Fatalf("refill let through %d bytes", more)
	}
}

// Two players who cannot reach each other except through the relay (ICE
// forced to relay-only) connect and exchange data through it.
func TestPlayersConnectThroughTheRelay(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	pass := NewSecret()
	srv, err := Start(Config{Port: 34780, Public: netip.MustParseAddr("127.0.0.1"), User: "lanbaz", Pass: pass, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	code, err := ParseCode(srv.Code("host").Encode())
	if err != nil {
		t.Fatal(err)
	}
	_, relay := code.Servers()
	cfg := webrtc.Configuration{
		ICEServers:         []webrtc.ICEServer{{URLs: []string{relay.URL}, Username: relay.Username, Credential: relay.Credential}},
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	}
	a, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	got := make(chan string, 1)
	b.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(m webrtc.DataChannelMessage) { got <- string(m.Data) })
	})
	dc, err := a.CreateDataChannel("x", nil)
	if err != nil {
		t.Fatal(err)
	}
	dc.OnOpen(func() { _ = dc.SendText("through the friend relay") })

	exchange := func(from, to *webrtc.PeerConnection, offer bool) {
		t.Helper()
		var d webrtc.SessionDescription
		if offer {
			d, err = from.CreateOffer(nil)
		} else {
			d, err = from.CreateAnswer(nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		done := webrtc.GatheringCompletePromise(from)
		if err := from.SetLocalDescription(d); err != nil {
			t.Fatal(err)
		}
		<-done
		if err := to.SetRemoteDescription(*from.LocalDescription()); err != nil {
			t.Fatal(err)
		}
	}
	exchange(a, b, true)
	exchange(b, a, false)
	if !strings.Contains(a.LocalDescription().SDP, "typ relay") {
		t.Fatalf("no relay candidate gathered:\n%s", a.LocalDescription().SDP)
	}

	select {
	case m := <-got:
		if m != "through the friend relay" {
			t.Fatalf("got %q", m)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no data through the relay")
	}
	if srv.Relayed() == 0 {
		t.Fatal("the relay counted no traffic")
	}
}
