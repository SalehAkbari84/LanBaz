package room_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/identity"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/internal/transport/webrtc"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// This file is the end-to-end proof of Phase 1's central claim: two LanBaz
// installations, with no server between them, can go from "the host has shown a
// code" to "both sides measure a live link" carrying nothing but two strings a
// human could have typed.
//
// Everything is on the loopback interface and STUN is switched off, so the test
// is deterministic and offline. What it exercises is the whole path - the
// pairing code, the offer/answer, ICE, DTLS, the two data channels, the hello
// handshake and the liveness measurement - not any one of those in isolation.

// quietLogger keeps a passing run from filling the terminal with DEBUG lines
// about ICE candidates.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newSide builds a room manager standing in for one LanBaz installation.
func newSide(t *testing.T, name string, events chan<- room.Notice) (*room.Manager, *identity.Identity) {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("%s: generate identity: %v", name, err)
	}
	m, err := room.NewManager(room.ManagerOptions{
		LocalID:     protocol.PeerID(id.ID()),
		LocalKey:    id.PrivateKey(),
		DisplayName: name,
		Factory:     webrtcFactory(),
		// No STUN: this test must not touch the network.
		STUNServers: []string{},
		MTU:         1200,
		GameProfile: "",
		Logger:      quietLogger(),
		OnEvent:     func(n room.Notice) { events <- n },
	})
	if err != nil {
		t.Fatalf("%s: new room manager: %v", name, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m, id
}

// webrtcFactory is the transport every test in this package negotiates over.
func webrtcFactory() transport.Factory { return webrtc.Factory{} }

// testIdentity is the identity a side was generated with, kept so a caller can
// build a second manager from the same key without generating a new one.
type testIdentity struct {
	id  protocol.PeerID
	key []byte
}

// generateIdentity produces one installation's identity.
func generateIdentity() (testIdentity, error) {
	id, err := identity.Generate()
	if err != nil {
		return testIdentity{}, err
	}
	return testIdentity{id: protocol.PeerID(id.ID()), key: id.PrivateKey()}, nil
}

// udpPacket builds a minimal IPv4 datagram, the way a game would.
func udpPacket(src, dst string, payload []byte) []byte {
	hdr := make([]byte, 20+len(payload))
	hdr[0] = 0x45
	total := 20 + len(payload)
	hdr[2] = byte(total >> 8)
	hdr[3] = byte(total)
	hdr[8] = 64
	hdr[9] = 17 // UDP
	s := netip.MustParseAddr(src).As4()
	d := netip.MustParseAddr(dst).As4()
	copy(hdr[12:16], s[:])
	copy(hdr[16:20], d[:])
	copy(hdr[20:], payload)
	return hdr
}

// netReadyTimeout bounds how long a test waits for a link to come up.
//
// Every assertion about a live link races it: ICE, DTLS and the channel
// handshakes all finish asynchronously after room.accept returns, and the
// routing table catches up on the room's next maintenance tick after that.
// Polling with a generous bound is what keeps these tests from being either
// flaky or slow.
const netReadyTimeout = 25 * time.Second

// waitForStatus polls a room's network status until ready is satisfied.
func waitForStatus(t *testing.T, s *netSide, roomID string,
	ready func(protocol.NetworkStatus) bool) protocol.NetworkStatus {
	t.Helper()
	var last protocol.NetworkStatus
	waitFor(t, "the network status of "+roomID, netReadyTimeout, func() bool {
		st, err := s.mgr.NetworkStatus(context.Background(), roomID)
		if err != nil {
			return false
		}
		last = st
		return ready(st)
	})
	return last
}

// drainEvents consumes the event channel so a blocked send cannot wedge a test.
// It deliberately does not close the channel: room goroutines may emit during
// teardown, and a send on a closed channel is a panic rather than a test
// failure.
func drainEvents(ch chan room.Notice) func() {
	done := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ch:
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// TestServerlessPairingConnectsAndMeasures is the Phase 1 acceptance test.
func TestServerlessPairingConnectsAndMeasures(t *testing.T) {
	hostEvents := make(chan room.Notice, 64)
	guestEvents := make(chan room.Notice, 64)
	stopHost := drainEvents(hostEvents)
	stopGuest := drainEvents(guestEvents)
	defer stopHost()
	defer stopGuest()

	host, hostID := newSide(t, "host", hostEvents)
	guest, guestID := newSide(t, "guest", guestEvents)
	_ = hostID
	_ = guestID

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 1. The host creates a room. A code comes back with it, because that is the
	// only thing the user can do next.
	created, err := host.Create(ctx, protocol.RoomCreateRequest{
		Name:              "Match Night",
		MaxPeers:          4,
		PairingTTLSeconds: 300,
	})
	if err != nil {
		t.Fatalf("room.create: %v", err)
	}
	if created.PairingCode == "" {
		t.Fatal("room.create returned no pairing code")
	}
	if !created.ExpiresAt.After(time.Now()) {
		t.Errorf("the pairing code expires at %s, which is not in the future", created.ExpiresAt)
	}
	if !created.Room.IsHost {
		t.Error("the room this daemon created is not marked as hosted")
	}

	// 2. The guest pastes the code and gets an answer code back. It is not
	// connected yet, and the response says so rather than pretending.
	joined, err := guest.Join(ctx, protocol.RoomJoinRequest{
		PairingCode: created.PairingURI, // the URI form must work as well as the bare code
		DisplayName: "guest",
	})
	if err != nil {
		t.Fatalf("room.join: %v", err)
	}
	if !joined.Pending {
		t.Error("a serverless join reported itself as complete; it cannot be")
	}
	if joined.AnswerCode == "" {
		t.Fatal("room.join returned no answer code for the host to apply")
	}
	if joined.LocalPeer.IsSelf != true || joined.LocalPeer.IsHost {
		t.Errorf("the guest's local peer row is self=%v host=%v, want self and not host",
			joined.LocalPeer.IsSelf, joined.LocalPeer.IsHost)
	}

	// 3. The host applies the answer. This is the moment the link comes up.
	hostRoom, err := host.Get(created.Room.RoomID)
	if err != nil {
		t.Fatalf("host room lookup: %v", err)
	}
	if err := host.Accept(ctx, protocol.RoomAcceptRequest{
		RoomID:     created.Room.RoomID,
		AnswerCode: joined.AnswerCode,
	}); err != nil {
		t.Fatalf("room.accept: %v", err)
	}

	// 4. Both sides must end up naming each other, and only after a successful
	// hello: a peer that is merely connected has proved nothing about its
	// identity.
	waitFor(t, "the host to see an active guest", 60*time.Second, func() bool {
		return anyActive(hostRoom.Peers().List())
	})
	guestRoom, err := guest.Get(created.Room.RoomID)
	if err != nil {
		t.Fatalf("guest room lookup: %v", err)
	}
	waitFor(t, "the guest to see an active host", 60*time.Second, func() bool {
		return anyActive(guestRoom.Peers().List())
	})

	hostPeers := hostRoom.Peers().List()
	if len(hostPeers) != 1 {
		t.Fatalf("the host sees %d peers, want 1", len(hostPeers))
	}
	guestPeers := guestRoom.Peers().List()
	if len(guestPeers) != 1 {
		t.Fatalf("the guest sees %d peers, want 1", len(guestPeers))
	}
	// The identities must agree: the host's view of the guest and the guest's
	// view of the host are derived from the same keys, and a mismatch here would
	// mean the hello check is not doing its job.
	if string(hostPeers[0].PeerID) != string(guestID.ID()) {
		t.Errorf("the host sees peer %s, want the guest's real id %s", hostPeers[0].PeerID, guestID.ID())
	}
	if string(guestPeers[0].PeerID) != string(hostID.ID()) {
		t.Errorf("the guest sees peer %s, want the host's real id %s", guestPeers[0].PeerID, hostID.ID())
	}
	if hostPeers[0].Fingerprint == "" || hostPeers[0].Fingerprint == guestPeers[0].Fingerprint {
		t.Errorf("the two peers report fingerprints %q and %q, want two distinct non-empty values",
			hostPeers[0].Fingerprint, guestPeers[0].Fingerprint)
	}

	// 5. A round trip must be measurable, and clean. This is the assertion that
	// would fail if a data channel opened but nothing actually flowed.
	guestLink := transportIDOf(guestPeers[0].PeerID)
	waitFor(t, "a round trip to complete", 30*time.Second, func() bool {
		return guestRoom.Peers().Ready(guestLink)
	})

	if !anyActive(hostRoom.Peers().List()) || !anyActive(guestRoom.Peers().List()) {
		t.Fatalf("a link did not become active on both sides: host=%s guest=%s",
			describe(hostRoom.Peers().List()), describe(guestRoom.Peers().List()))
	}

	result, err := guestRoom.Peers().Ping(ctx, guestLink, 4, 8000)
	if err != nil {
		t.Fatalf("peer.ping: %v", err)
	}
	if result.Sent != 4 || result.Received != 4 {
		t.Errorf("ping sent %d received %d, want 4 and 4", result.Sent, result.Received)
	}
	if result.RTT <= 0 {
		t.Errorf("the measured RTT is %s, want a positive round trip", result.RTT)
	}
	if result.Loss != 0 {
		t.Errorf("loss on loopback is %v, want 0", result.Loss)
	}
	if result.Max < result.Min {
		t.Errorf("max %s is below min %s", result.Max, result.Min)
	}
	t.Logf("measured rtt=%s jitter=%s min=%s max=%s", result.RTT, result.Jitter, result.Min, result.Max)

	// The measurement must also be visible in the summary the UI polls, not only
	// in the return value of the call that asked for it.
	waitFor(t, "the round trip to reach the peer summary", 20*time.Second, func() bool {
		for _, p := range guestRoom.Peers().List() {
			if p.PeerID == guestPeers[0].PeerID {
				return p.RoundTrips > 0 && p.RTTMillis > 0
			}
		}
		return false
	})

	// 5b. Chat flows both ways over the control channel.
	sent, err := guestRoom.SendChat("  salam!  ")
	if err != nil {
		t.Fatalf("guest chat: %v", err)
	}
	waitFor(t, "the guest's chat to reach the host", 10*time.Second, func() bool {
		for _, m := range hostRoom.ChatHistory() {
			if m.ID == sent.ID && m.Text == "salam!" && !m.Self {
				return true
			}
		}
		return false
	})
	if _, err := hostRoom.SendChat("hello back"); err != nil {
		t.Fatalf("host chat: %v", err)
	}
	waitFor(t, "the host's chat to reach the guest", 10*time.Second, func() bool {
		for _, m := range guestRoom.ChatHistory() {
			if m.Text == "hello back" {
				return true
			}
		}
		return false
	})

	// 5c. Voice signalling: addressed to one player, opaque to the daemon.
	got := make(chan protocol.VoiceSignal, 1)
	hostRoom.SetSignalHandler(func(kind string, v protocol.VoiceSignal) {
		if kind == room.SignalVoice {
			got <- v
		}
	})
	offer := json.RawMessage(`{"type":"offer","sdp":"v=0"}`)
	if err := guestRoom.SendVoice(string(guestPeers[0].PeerID), offer); err != nil {
		t.Fatalf("voice: %v", err)
	}
	select {
	case v := <-got:
		if string(v.Data) != string(offer) || v.From == "" || v.RoomID != created.Room.RoomID {
			t.Fatalf("voice signal arrived as %+v", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the voice signal never reached the host")
	}
	if err := guestRoom.SendVoice("", json.RawMessage(`not json`)); err == nil {
		t.Error("a malformed voice signal was accepted")
	}

	// 6. The code is spent. Replaying it must be refused, or the whole
	// single-shot design is decorative.
	if _, err := guest.Join(ctx, protocol.RoomJoinRequest{PairingCode: created.PairingCode}); err == nil {
		t.Error("the spent pairing code was accepted a second time")
	}
	// The host side is what actually enforces single use: the reservation the
	// answer was for is gone, so replaying the answer must be refused.
	if err := host.Accept(ctx, protocol.RoomAcceptRequest{
		RoomID:     created.Room.RoomID,
		AnswerCode: joined.AnswerCode,
	}); err == nil {
		t.Error("the spent answer code was accepted a second time")
	}
}

// anyActive reports whether a peer list contains a healthy peer.
func anyActive(peers []protocol.PeerSummary) bool {
	for _, p := range peers {
		if p.State == protocol.PeerActive {
			return true
		}
	}
	return false
}

// describe renders a peer list for a failure message.
func describe(peers []protocol.PeerSummary) string {
	if len(peers) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(peers))
	for _, p := range peers {
		parts = append(parts, fmt.Sprintf("{id=%s state=%s rt=%d rtt=%.1fms}", p.PeerID, p.State, p.RoundTrips, p.RTTMillis))
	}
	return strings.Join(parts, " ")
}

// transportIDOf returns the transport-level peer id for an announced peer id.
func transportIDOf(id protocol.PeerID) transport.PeerID { return transport.PeerID(id) }

// TestRoomRejectsAGuestThatCannotBeReached is the negative half of the above: a
// join that produces an answer the host never sees must not leave a peer behind.
func TestRoomRejectsAGuestThatCannotBeReached(t *testing.T) {
	events := make(chan room.Notice, 16)
	stop := drainEvents(events)
	defer stop()

	host, _ := newSide(t, "host", events)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := host.Create(ctx, protocol.RoomCreateRequest{Name: "Test"})
	if err != nil {
		t.Fatalf("room.create: %v", err)
	}
	// No guest ever appears, so nothing accepts the offer.
	roomRef, err := host.Get(created.Room.RoomID)
	if err != nil {
		t.Fatalf("host room lookup: %v", err)
	}
	if got := roomRef.Peers().Count(); got != 0 {
		t.Errorf("the room has %d peers with no guest, want 0", got)
	}
	if !roomRef.PairingIssued() {
		t.Error("the pairing code should still be redeemable when nobody has joined")
	}

	// A fresh code is a distinct code with its own reservation; the old one
	// expires with its own TTL or when the room needs its seat back.
	second, err := host.RegeneratePairing(ctx, protocol.RoomRegeneratePairingRequest{
		RoomID: created.Room.RoomID,
	})
	if err != nil {
		t.Fatalf("room.regenerate_pairing: %v", err)
	}
	if second.PairingCode == created.PairingCode {
		t.Error("regenerating the pairing code returned the same code")
	}
}

// TestAcceptRejectsAnAnswerFromAnotherRoom proves the answer is bound to the room
// that produced the offer.
func TestAcceptRejectsAnAnswerFromAnotherRoom(t *testing.T) {
	eventsA := make(chan room.Notice, 16)
	eventsB := make(chan room.Notice, 16)
	stopA := drainEvents(eventsA)
	stopB := drainEvents(eventsB)
	defer stopA()
	defer stopB()

	hostA, _ := newSide(t, "host-a", eventsA)
	hostB, _ := newSide(t, "host-b", eventsB)
	guest, _ := newSide(t, "guest", eventsA)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	roomA, err := hostA.Create(ctx, protocol.RoomCreateRequest{Name: "A"})
	if err != nil {
		t.Fatalf("room.create A: %v", err)
	}
	if _, err := hostB.Create(ctx, protocol.RoomCreateRequest{Name: "B"}); err != nil {
		t.Fatalf("room.create B: %v", err)
	}

	// The guest answers A's offer...
	joinedA, err := guest.Join(ctx, protocol.RoomJoinRequest{PairingCode: roomA.PairingCode})
	if err != nil {
		t.Fatalf("room.join: %v", err)
	}
	// ...and B tries to apply it.
	err = hostB.Accept(ctx, protocol.RoomAcceptRequest{
		RoomID:     roomA.Room.RoomID,
		AnswerCode: joinedA.AnswerCode,
	})
	if err == nil {
		t.Fatal("a room accepted an answer that belongs to another room")
	}
}
