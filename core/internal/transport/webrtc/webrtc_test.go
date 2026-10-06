package webrtc

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/lanbaz/lanbaz/core/internal/transport"
)

// testKey returns a deterministic-but-valid 64-byte Ed25519 private key for
// tests. The transport only requires the length; it never signs with it.
func testKey(b byte) []byte {
	k := make([]byte, 64)
	for i := range k {
		k[i] = b
	}
	return k
}

func newTestTransport(t *testing.T, roomID string, b byte) *Transport {
	t.Helper()
	tr, err := New(Options{
		RoomID:     roomID,
		PrivateKey: testKey(b),
		// A non-nil empty slice means "no STUN", so these tests stay offline and
		// resolve purely over local host candidates. Real deployments get STUN
		// from the configuration.
		STUNServers: []string{},
		MTU:         1200,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

// waitUsable blocks until the link reports ready, which now means the data
// channels are open rather than merely ICE-connected.
func waitUsable(ctx context.Context, tr *Transport, peer transport.PeerID) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if tr.Ready(peer) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return errors.New("timed out waiting for the link to become usable")
}

// waitDataOpen blocks until the unreliable data channel is open on a link.
//
// Ready reports the *control* channel, and the two open independently: the
// control channel is created first on the offerer but the answerer adopts
// whichever arrives first, so on a loaded machine the data channel can still be
// negotiating when Ready turns true. Asserting on Send against a link that is
// only control-ready produced a failure that looked like a transport bug and was
// really a race in the test.
func waitDataOpen(ctx context.Context, tr *Transport, peer transport.PeerID) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if l, ok := tr.link(peer); ok {
			l.stateMu.RLock()
			dc := l.data
			l.stateMu.RUnlock()
			if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return errors.New("timed out waiting for the data channel to open")
}

func TestNewRejectsBadKeyLength(t *testing.T) {
	for _, n := range []int{0, 32, 63, 65} {
		if _, err := New(Options{RoomID: "r", PrivateKey: make([]byte, n)}); err == nil {
			t.Errorf("New accepted a %d-byte private key", n)
		}
	}
}

func TestNameAndSignallingCapability(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	defer tr.Shutdown(context.Background())

	if tr.Name() != "webrtc" {
		t.Errorf("Name() = %q, want webrtc", tr.Name())
	}
	if !tr.SupportsSignalling() {
		t.Error("the webrtc transport must support out-of-band signalling")
	}
}

// This is the central claim of Phase 1: two peers with no server, no signalling
// channel and no shared state can negotiate over a pair of opaque descriptions
// and end up with a working reliable control channel and an unreliable data
// channel.
func TestServerlessHandshakeEstablishesBothChannels(t *testing.T) {
	host := newTestTransport(t, "room", 1)
	guest := newTestTransport(t, "room", 2)
	defer host.Shutdown(context.Background())
	defer guest.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	hostPeer := transport.PeerInfo{ID: "guest", RoomID: "room"}
	guestPeer := transport.PeerInfo{ID: "host", RoomID: "room"}

	// The host offers.
	offer, err := host.CreateOffer(ctx, hostPeer)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if offer.Kind != transport.DescriptionOffer {
		t.Errorf("offer kind = %q, want %q", offer.Kind, transport.DescriptionOffer)
	}
	if len(offer.Data) == 0 {
		t.Fatal("the offer carries no data")
	}
	// A gathered offer must contain ICE credentials and candidates, otherwise
	// the peer on the other side of a NAT has nothing to work with.
	sdp, err := decodeDescription(offer.Data)
	if err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	for _, want := range []string{"a=ice-ufrag:", "a=ice-pwd:", "a=candidate:"} {
		if !strings.Contains(sdp, want) {
			t.Errorf("the offer is missing %q, so a peer behind NAT cannot use it", want)
		}
	}

	// The guest answers.
	answer, err := guest.AcceptOffer(ctx, guestPeer, offer)
	if err != nil {
		t.Fatalf("AcceptOffer: %v", err)
	}
	if answer.Kind != transport.DescriptionAnswer {
		t.Errorf("answer kind = %q, want %q", answer.Kind, transport.DescriptionAnswer)
	}

	// The host applies it and waits.
	if err := host.ApplyAnswer(ctx, hostPeer, answer); err != nil {
		t.Fatalf("ApplyAnswer: %v", err)
	}
	if !host.Ready(hostPeer.ID) {
		t.Error("the host does not consider the peer ready after applying the answer")
	}

	// Wait for the guest side too.
	if err := guest.WaitConnected(ctx, guestPeer); err != nil {
		t.Fatalf("the guest never reported connected: %v", err)
	}

	// ICE connecting is not the same as being able to send: the channels open
	// after DTLS. Wait for them on both sides before asserting on Send, or the
	// test races the handshake and reports a false failure.
	if err := waitUsable(ctx, guest, guestPeer.ID); err != nil {
		t.Fatalf("the guest control channel never opened: %v", err)
	}
	if err := waitDataOpen(ctx, guest, guestPeer.ID); err != nil {
		t.Fatalf("the guest data channel never opened: %v", err)
	}

	// The reliable control channel must round trip a message.
	ping := []byte(`{"type":"ping","id":"1"}`)
	if err := guest.SendControl(guestPeer.ID, ping); err != nil {
		t.Fatalf("SendControl: %v", err)
	}

	received := make(chan []byte, 1)
	host.SetControlHandler(func(peer transport.PeerID, payload []byte) {
		select {
		case received <- append([]byte(nil), payload...):
		default:
		}
	})
	// The handler may need to be installed before the frame arrives, so retry
	// the send a few times rather than assuming a fixed ordering.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case got := <-received:
			if string(got) != string(ping) {
				t.Errorf("control payload = %q, want %q", got, ping)
			}
			// The control path works; move on.
			goto dataPhase
		case <-deadline:
			t.Fatal("the control message never arrived")
		case <-time.After(200 * time.Millisecond):
			// The first send may have raced the handler installation.
			if err := guest.SendControl(guestPeer.ID, ping); err != nil {
				t.Fatalf("SendControl retry: %v", err)
			}
		}
	}

dataPhase:
	// The unreliable data channel must carry a payload in the other direction.
	got := make(chan transport.Packet, 1)
	go func() {
		for p := range host.Receive() {
			select {
			case got <- p:
			default:
				return
			}
		}
	}()

	payload := []byte("lanbaz-game-packet")
	if err := guest.Send(guestPeer.ID, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case p := <-got:
		if string(p.Payload) != string(payload) {
			t.Errorf("data payload = %q, want %q", p.Payload, payload)
		}
		// Peer is the link the packet arrived on; Src and Dst stay empty until
		// the virtual LAN assigns addresses, so a transport that filled them
		// with a peer id would collide with a real virtual address later.
		if p.Peer != "guest" {
			t.Errorf("packet peer = %q, want guest", p.Peer)
		}
		if p.Src != "" || p.Dst != "" {
			t.Errorf("packet src/dst = %q/%q, want empty before the virtual LAN", p.Src, p.Dst)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the data packet never arrived")
	}
}

// Applying an answer for a peer with no link must not panic or create one.
func TestApplyAnswerRejectsUnknownPeer(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	defer tr.Shutdown(context.Background())

	err := tr.ApplyAnswer(context.Background(),
		transport.PeerInfo{ID: "nobody"}, transport.Description{Data: []byte("v=0")})
	if err == nil {
		t.Fatal("ApplyAnswer accepted a peer with no link")
	}
	if err != transport.ErrPeerUnknown {
		t.Errorf("ApplyAnswer error = %v, want ErrPeerUnknown", err)
	}
}

func TestSendRejectsUnknownPeer(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	defer tr.Shutdown(context.Background())

	if err := tr.Send("nobody", []byte("x")); err != transport.ErrPeerUnknown {
		t.Errorf("Send error = %v, want ErrPeerUnknown", err)
	}
	if err := tr.SendControl("nobody", []byte("x")); err != transport.ErrPeerUnknown {
		t.Errorf("SendControl error = %v, want ErrPeerUnknown", err)
	}
}

// A payload larger than the MTU must be refused locally rather than fragmenting
// into something the game cannot reassemble.
func TestSendRejectsOversizedPayload(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	defer tr.Shutdown(context.Background())

	// Reach the size check before the peer check by using a known link.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	peer := transport.PeerInfo{ID: "guest"}
	if _, err := tr.CreateOffer(ctx, peer); err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}

	big := make([]byte, 4096)
	err := tr.Send("guest", big)
	if err == nil {
		t.Fatal("Send accepted a payload larger than the MTU")
	}
	var tooLarge *transport.PayloadTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("Send error = %v, want a PayloadTooLargeError", err)
	}
	if tooLarge.Max != 1200 {
		t.Errorf("the reported limit is %d, want the configured MTU 1200", tooLarge.Max)
	}
}

func TestCloseUnknownPeerIsNotAnError(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	defer tr.Shutdown(context.Background())
	if err := tr.Close("nobody"); err != nil {
		t.Errorf("Close on an unknown peer returned %v, want nil", err)
	}
}

func TestShutdownClosesReceive(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case _, ok := <-tr.Receive():
		if ok {
			t.Error("Receive yielded a packet after shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Receive was not closed by Shutdown")
	}
	// A second shutdown must be a no-op rather than a panic on a closed channel.
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown returned %v", err)
	}
}

func TestOperationsAfterShutdownFail(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	if err := tr.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	_, err := tr.CreateOffer(context.Background(), transport.PeerInfo{ID: "x"})
	if err != transport.ErrClosed {
		t.Errorf("CreateOffer after shutdown = %v, want ErrClosed", err)
	}
}

func TestAcceptOfferRejectsEmptyDescription(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	defer tr.Shutdown(context.Background())

	if _, err := tr.AcceptOffer(context.Background(),
		transport.PeerInfo{ID: "host"}, transport.Description{}); err == nil {
		t.Fatal("AcceptOffer accepted an empty description")
	}
}

func TestStatsReportsTransportName(t *testing.T) {
	tr := newTestTransport(t, "room", 1)
	defer tr.Shutdown(context.Background())

	if _, err := tr.Stats("nobody"); err != transport.ErrPeerUnknown {
		t.Errorf("Stats on an unknown peer = %v, want ErrPeerUnknown", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	peer := transport.PeerInfo{ID: "guest"}
	if _, err := tr.CreateOffer(ctx, peer); err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	st, err := tr.Stats("guest")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Transport != "webrtc" {
		t.Errorf("stats transport = %q, want webrtc", st.Transport)
	}
}

func TestCandidateFilterDropsAddressesNoFriendCanReach(t *testing.T) {
	keep := []string{"192.168.1.4", "172.22.16.1", "26.234.232.134", "2001:db8::1", "8.8.8.8"}
	drop := []string{"127.0.0.1", "::1", "fe80::1", "fdfd::1aea:e886", "10.200.17.1", "100.127.255.253", "169.254.1.1"}
	for _, s := range keep {
		if !usableCandidateIP(net.ParseIP(s)) {
			t.Errorf("%s was dropped, want it offered", s)
		}
	}
	for _, s := range drop {
		if usableCandidateIP(net.ParseIP(s)) {
			t.Errorf("%s was offered, want it dropped", s)
		}
	}
}
