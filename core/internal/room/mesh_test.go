package room_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// With mesh on, two guests get a direct link to each other after the host
// introduces them, and a packet between them arrives without the host's hop.
func TestGuestsLinkDirectlyWithMesh(t *testing.T) {
	events := make(chan room.Notice, 512)
	stop := drainEvents(events)
	defer stop()

	host := newNetSide(t, "host", events)
	first := newNetSide(t, "first", events)
	second := newNetSide(t, "second", events)
	for _, s := range []*netSide{host, first, second} {
		s.mgr.SetTransportDefaults(room.TransportDefaults{STUN: []string{}, Mesh: true, NameResolution: true, MTU: 1200})
	}

	created, err := host.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "mesh"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	roomID := created.Room.RoomID
	code := created.PairingCode
	join := func(side *netSide, name string) protocol.NetworkStatus {
		t.Helper()
		joined, err := side.mgr.Join(context.Background(), protocol.RoomJoinRequest{PairingCode: code, DisplayName: name})
		if err != nil {
			t.Fatalf("%s join: %v", name, err)
		}
		if err := host.mgr.Accept(context.Background(), protocol.RoomAcceptRequest{RoomID: roomID, AnswerCode: joined.AnswerCode}); err != nil {
			t.Fatalf("%s accept: %v", name, err)
		}
		reissued, err := host.mgr.RegeneratePairing(context.Background(), protocol.RoomRegeneratePairingRequest{RoomID: roomID})
		if err != nil {
			t.Fatalf("%s regenerate: %v", name, err)
		}
		code = reissued.PairingCode
		return waitForStatus(t, side, roomID, func(st protocol.NetworkStatus) bool {
			return st.LocalAddress != "" && st.State == string(network.StateReady)
		})
	}
	firstStatus := join(first, "first")
	secondStatus := join(second, "second")

	// The direct link shows up as an active, non-host peer on both guests.
	direct := func(side *netSide) bool {
		r, err := side.mgr.Get(roomID)
		if err != nil {
			return false
		}
		for _, p := range r.Peers().List() {
			if !p.IsHost && strings.HasPrefix(p.TransportPeerID, "m-") && p.State == protocol.PeerActive {
				return true
			}
		}
		return false
	}
	waitFor(t, "a direct link between the guests", 40*time.Second, func() bool { return direct(first) && direct(second) })
	// And it is in the routing table.
	waitForStatus(t, first, roomID, func(st protocol.NetworkStatus) bool { return len(st.Routes) >= 3 })

	first.adapters[roomID].Inject(network.Packet{
		Payload: udpPacket(firstStatus.LocalAddress, secondStatus.LocalAddress, []byte("lanbaz-direct")),
	})
	got, err := second.adapters[roomID].Delivered(15 * time.Second)
	if err != nil {
		t.Fatalf("the second guest received nothing: %v", err)
	}
	h, err := network.ParseIPv4(got.Payload)
	if err != nil {
		t.Fatalf("not IPv4: %v", err)
	}
	if h.TTL != 64 {
		t.Errorf("ttl = %d, want 64: a direct packet must not pass through the host", h.TTL)
	}

	// Chat from one guest reaches the other exactly once, even though it can
	// now arrive both directly and through the host.
	fr, _ := first.mgr.Get(roomID)
	sr, _ := second.mgr.Get(roomID)
	msg, err := fr.SendChat("gg")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	waitFor(t, "the chat to reach the other guest", 10*time.Second, func() bool {
		for _, m := range sr.ChatHistory() {
			if m.ID == msg.ID {
				return true
			}
		}
		return false
	})
	time.Sleep(500 * time.Millisecond)
	n := 0
	for _, m := range sr.ChatHistory() {
		if m.ID == msg.ID {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the message was delivered %d times, want 1", n)
	}

	// Names: first asks for "second.local" and its own adapter answers with
	// second's room address.
	query := []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 6, 's', 'e', 'c', 'o', 'n', 'd', 5, 'l', 'o', 'c', 'a', 'l', 0, 0, 1, 0, 1}
	first.adapters[roomID].Inject(network.Packet{Payload: mdnsPacket(firstStatus.LocalAddress, query)})
	answer, err := first.adapters[roomID].Delivered(5 * time.Second)
	if err != nil {
		t.Fatalf("no answer for second.local: %v", err)
	}
	ah, _ := network.ParseIPv4(answer.Payload)
	if ah.Src.String() != secondStatus.LocalAddress {
		t.Fatalf("second.local answered by %s, want %s", ah.Src, secondStatus.LocalAddress)
	}

	// Presence: what first plays, and where, reaches second.
	fr.SetLocalGame(&room.LocalGame{GameID: "minecraft", GameName: "Minecraft", Exe: "javaw.exe", Ports: []int{53211}})
	want := firstStatus.LocalAddress + ":53211"
	waitFor(t, "first's game to reach second", 10*time.Second, func() bool {
		for _, p := range sr.Presences() {
			if p.GameID == "minecraft" && p.Hosting && len(p.Endpoints) == 1 && p.Endpoints[0] == want {
				return true
			}
		}
		return false
	})
}

// mdnsPacket wraps a DNS query in IPv4/UDP to 224.0.0.251:5353.
func mdnsPacket(src string, dns []byte) []byte {
	total := 20 + 8 + len(dns)
	b := make([]byte, total)
	b[0] = 0x45
	b[2], b[3] = byte(total>>8), byte(total)
	b[8], b[9] = 255, 17
	s := netip.MustParseAddr(src).As4()
	copy(b[12:16], s[:])
	copy(b[16:20], []byte{224, 0, 0, 251})
	b[20], b[21], b[22], b[23] = 0x14, 0xe9, 0x14, 0xe9
	b[24], b[25] = byte((8+len(dns))>>8), byte(8+len(dns))
	copy(b[28:], dns)
	return b
}
