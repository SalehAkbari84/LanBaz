package room_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/network/memory"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// newDualSide can build both standard (memory) and classic (memory-l2) rooms.
func newDualSide(t *testing.T, name string, events chan<- room.Notice) *room.Manager {
	t.Helper()
	id, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	l3 := func(_ context.Context, roomID string, tr transport.Transport, subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
		return network.NewService(network.ServiceConfig{RoomID: roomID, Subnet: subnet, Local: local, IsHost: isHost,
			Transport: tr, Adapter: memory.New(), Backend: "memory", MTU: 1200, Logger: quietLogger(), OnChange: func(network.Status) {}})
	}
	l2 := func(_ context.Context, roomID string, tr transport.Transport, subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
		return network.NewSwitch(network.SwitchConfig{RoomID: roomID, IsHost: isHost, Transport: tr, Adapter: memory.New(),
			Backend: "memory-l2", Subnet: subnet, Local: local, MTU: 1200, Logger: quietLogger()})
	}
	m, err := room.NewManager(room.ManagerOptions{
		LocalID: protocol.PeerID(id.id), LocalKey: id.key, DisplayName: name, Factory: webrtcFactory(),
		STUNServers: []string{}, MTU: 1200, Allocator: ipam.New(ipam.Pool), Logger: quietLogger(),
		NetworkFactory: l3, L2NetworkFactory: l2,
		OnEvent: func(n room.Notice) { events <- n },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

// The host switches an open room from standard to classic LAN: nobody leaves,
// the guest follows by itself, and everybody keeps their address.
func TestSwitchingNetworkTypeKeepsTheRoom(t *testing.T) {
	events := make(chan room.Notice, 512)
	stop := drainEvents(events)
	defer stop()
	host := newDualSide(t, "host", events)
	guest := newDualSide(t, "guest", events)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	created, err := host.Create(ctx, protocol.RoomCreateRequest{Name: "switch", Mode: protocol.RoomModeL3})
	if err != nil {
		t.Fatal(err)
	}
	roomID := created.Room.RoomID
	joined, err := guest.Join(ctx, protocol.RoomJoinRequest{PairingCode: created.PairingCode})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Accept(ctx, protocol.RoomAcceptRequest{RoomID: roomID, AnswerCode: joined.AnswerCode}); err != nil {
		t.Fatal(err)
	}
	guestAddr := func() string {
		r, err := guest.Get(roomID)
		if err != nil {
			return ""
		}
		return r.Summary().LocalAddress
	}
	waitFor(t, "the guest is connected and addressed", 60*time.Second, func() bool {
		r, err := host.Get(roomID)
		return err == nil && anyActive(r.Summary().Peers) && guestAddr() != ""
	})
	before := guestAddr()

	if _, err := host.SetMode(ctx, roomID, protocol.RoomModeL2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the guest follows to classic LAN", 30*time.Second, func() bool {
		st, err := guest.NetworkStatus(ctx, roomID)
		r, gerr := guest.Get(roomID)
		return err == nil && gerr == nil && r.Mode() == protocol.RoomModeL2 && st.Adapter == "memory-l2"
	})
	st, err := host.NetworkStatus(ctx, roomID)
	if err != nil || st.Adapter != "memory-l2" {
		t.Fatalf("host adapter %q (%v)", st.Adapter, err)
	}
	r, err := host.Get(roomID)
	if err != nil || !anyActive(r.Summary().Peers) {
		t.Fatal("the guest dropped out of the room during the switch")
	}
	if after := guestAddr(); after != before {
		t.Fatalf("the guest's address changed: %s -> %s", before, after)
	}

	// And back, the same way.
	if _, err := host.SetMode(ctx, roomID, protocol.RoomModeL3); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the guest follows back to standard", 30*time.Second, func() bool {
		st, err := guest.NetworkStatus(ctx, roomID)
		return err == nil && st.Adapter == "memory"
	})
}
