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

func newL2Side(t *testing.T, name string, events chan<- room.Notice) *room.Manager {
	t.Helper()
	id, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	l2 := func(_ context.Context, roomID string, tr transport.Transport, subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
		return network.NewSwitch(network.SwitchConfig{RoomID: roomID, IsHost: isHost, Transport: tr, Adapter: memory.New(),
			Backend: "memory-l2", Subnet: subnet, Local: local, MTU: 1200, Logger: quietLogger()})
	}
	m, err := room.NewManager(room.ManagerOptions{
		LocalID: protocol.PeerID(id.id), LocalKey: id.key, DisplayName: name, Factory: webrtcFactory(),
		STUNServers: []string{}, MTU: 1200, Allocator: ipam.New(ipam.Pool), Logger: quietLogger(),
		L2NetworkFactory: l2,
		OnEvent:          func(n room.Notice) { events <- n },
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

// The host's room mode travels in the signed invite and the guest builds the
// same kind of network.
func TestClassicLANModeReachesTheGuest(t *testing.T) {
	events := make(chan room.Notice, 256)
	stop := drainEvents(events)
	defer stop()
	host := newL2Side(t, "host", events)
	guest := newL2Side(t, "guest", events)

	created, err := host.Create(context.Background(), protocol.RoomCreateRequest{Name: "ipx", Mode: protocol.RoomModeL2})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Room.Mode != protocol.RoomModeL2 {
		t.Fatalf("host room mode %q", created.Room.Mode)
	}
	joined, err := guest.Join(context.Background(), protocol.RoomJoinRequest{PairingCode: created.PairingCode})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if joined.Room.Mode != protocol.RoomModeL2 {
		t.Fatalf("guest room mode %q, want l2", joined.Room.Mode)
	}
	st, err := guest.NetworkStatus(context.Background(), created.Room.RoomID)
	if err != nil || st.Adapter != "memory-l2" {
		t.Fatalf("guest network adapter %q (%v), want the L2 backend", st.Adapter, err)
	}
}
