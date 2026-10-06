package room_test

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/internal/network/memory"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// stopSpy records whether a network was stopped.
type stopSpy struct {
	network.VirtualNetwork
	stopped *atomic.Bool
}

func (s stopSpy) Stop(ctx context.Context) error {
	s.stopped.Store(true)
	return s.VirtualNetwork.Stop(ctx)
}

// A room closed while its adapter is still being set up must not leave the
// adapter behind. In 0.3.1 it did, and the next join of the same room failed
// with "Cannot create a file when that file already exists".
func TestRoomClosedDuringNetworkSetupRemovesTheNetwork(t *testing.T) {
	id, err := generateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 1)
	release := make(chan struct{})
	var stopped atomic.Bool
	m, err := room.NewManager(room.ManagerOptions{
		LocalID:     protocol.PeerID(id.id),
		LocalKey:    id.key,
		DisplayName: "host",
		Factory:     webrtcFactory(),
		STUNServers: []string{},
		MTU:         1200,
		Allocator:   ipam.New(ipam.Pool),
		NetworkFactory: func(ctx context.Context, roomID string, tr transport.Transport,
			subnet netip.Prefix, local netip.Addr, isHost bool) (network.VirtualNetwork, error) {
			entered <- roomID
			<-release
			svc, err := network.NewService(network.ServiceConfig{
				RoomID: roomID, Subnet: subnet, Local: local, IsHost: isHost,
				Transport: tr, Adapter: memory.New(), Backend: "memory",
				MTU: network.DefaultMTU, Logger: quietLogger(),
			})
			if err != nil {
				return nil, err
			}
			return stopSpy{VirtualNetwork: svc, stopped: &stopped}, nil
		},
		Logger: quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())

	// The request that creates the room is cancelled right away, as a UI
	// that disconnects would do; the setup must carry on regardless.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	created := make(chan error, 1)
	go func() {
		_, err := m.Create(reqCtx, protocol.RoomCreateRequest{Name: "race"})
		created <- err
	}()
	var roomID string
	select {
	case roomID = <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the network factory was never called")
	}
	cancelReq()
	left := make(chan struct{})
	go func() {
		_ = m.Leave(context.Background(), roomID)
		close(left)
	}()
	time.Sleep(200 * time.Millisecond)
	close(release)
	select {
	case <-left:
	case <-time.After(30 * time.Second):
		t.Fatal("leaving the room hung")
	}
	<-created
	deadline := time.Now().Add(5 * time.Second)
	for !stopped.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !stopped.Load() {
		t.Fatal("the network that finished starting after the room closed was never stopped")
	}
}
