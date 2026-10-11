package room_test

import (
	"context"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Without direct links, each guest still lists the other guest - learned from
// the host's member list - with its name and room address.
func TestGuestsSeeEachOtherWithoutMesh(t *testing.T) {
	events := make(chan room.Notice, 512)
	stop := drainEvents(events)
	defer stop()

	host := newNetSide(t, "host", events)
	first := newNetSide(t, "first", events)
	second := newNetSide(t, "second", events)
	for _, s := range []*netSide{host, first, second} {
		s.mgr.SetTransportDefaults(room.TransportDefaults{STUN: []string{}, Mesh: false, MTU: 1200})
	}
	created, err := host.mgr.Create(context.Background(), protocol.RoomCreateRequest{Name: "roster"})
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

	sees := func(side *netSide, name, addr string) bool {
		r, err := side.mgr.Get(roomID)
		if err != nil {
			return false
		}
		for _, p := range r.Summary().Peers {
			if p.DisplayName == name && p.VirtualAddress == addr && p.LinkKind == room.LinkHub {
				return true
			}
		}
		return false
	}
	waitFor(t, "the first guest lists the second", 20*time.Second, func() bool { return sees(first, "second", secondStatus.LocalAddress) })
	waitFor(t, "the second guest lists the first", 20*time.Second, func() bool { return sees(second, "first", firstStatus.LocalAddress) })

	// The second guest leaves: the first stops listing it.
	if err := second.mgr.Leave(context.Background(), roomID); err != nil {
		t.Fatalf("leave: %v", err)
	}
	waitFor(t, "the departed guest is dropped from the list", 30*time.Second, func() bool {
		r, err := first.mgr.Get(roomID)
		if err != nil {
			return false
		}
		for _, p := range r.Summary().Peers {
			if p.DisplayName == "second" {
				return false
			}
		}
		return true
	})
}
