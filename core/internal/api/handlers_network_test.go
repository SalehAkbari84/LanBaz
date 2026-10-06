package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// fakeNetwork is a NetworkService that answers from a table, so these tests
// are about the API's behaviour - which room a request resolves to, what a
// build with no virtual network says, what a malformed payload does - and not
// about a packet ever crossing a wire.
type fakeNetwork struct {
	rooms  []string
	status map[string]protocol.NetworkStatus
	// failStatus makes NetworkStatus return an error for one room, to prove the
	// error reaches the client as a code rather than an empty answer.
	failStatus string
}

func (f *fakeNetwork) First() (string, bool) {
	if len(f.rooms) == 0 {
		return "", false
	}
	return f.rooms[0], true
}

func (f *fakeNetwork) RoomIDs() []string { return f.rooms }

func (f *fakeNetwork) NetworkStatus(_ context.Context, roomID string) (protocol.NetworkStatus, error) {
	if roomID == f.failStatus {
		return protocol.NetworkStatus{}, protocol.NewErrorf(protocol.CodeNotFound,
			"no room %s", roomID)
	}
	st, ok := f.status[roomID]
	if !ok {
		return protocol.NetworkStatus{}, protocol.NewErrorf(protocol.CodeNotFound,
			"no room %s", roomID)
	}
	return st, nil
}

func (f *fakeNetwork) NetworkRoutes(_ context.Context, roomID string) ([]protocol.RouteEntry, error) {
	st, err := f.NetworkStatus(context.Background(), roomID)
	if err != nil {
		return nil, err
	}
	return st.Routes, nil
}

// withNetwork starts a server whose network service is f, or one with none when
// f is nil.
func withNetwork(t *testing.T, f *fakeNetwork) (*Server, *protocol.Client) {
	t.Helper()
	s, _ := newTestServerBefore(t, nil, func(s *Server) {
		if f == nil {
			return
		}
		if err := s.SetNetworkService(f); err != nil {
			t.Fatalf("SetNetworkService: %v", err)
		}
	})
	return s, dial(t, s)
}

// A room-less query must resolve to the first room: the common case is a user
// with one room open, and making them name it would be a step with no meaning.
func TestNetworkStatusResolvesTheRoomWhenNoneIsNamed(t *testing.T) {
	f := &fakeNetwork{
		rooms: []string{"room-a", "room-b"},
		status: map[string]protocol.NetworkStatus{
			"room-a": {RoomID: "room-a", Subnet: "10.200.7.0/24", LocalAddress: "10.200.7.1"},
			"room-b": {RoomID: "room-b", Subnet: "10.200.8.0/24", LocalAddress: "10.200.8.1"},
		},
	}
	_, c := withNetwork(t, f)
	ctx := context.Background()

	var st protocol.NetworkStatus
	if err := c.Call(ctx, protocol.MethodNetworkStatus, nil, &st); err != nil {
		t.Fatalf("network.status: %v", err)
	}
	if st.RoomID != "room-a" {
		t.Errorf("room = %q, want the first room room-a", st.RoomID)
	}
	if st.Subnet != "10.200.7.0/24" {
		t.Errorf("subnet = %q, want 10.200.7.0/24", st.Subnet)
	}

	// Naming a room must win over the default, or two rooms on one machine
	// would be indistinguishable.
	named := struct {
		RoomID string `json:"room_id"`
	}{RoomID: "room-b"}
	if err := c.Call(ctx, protocol.MethodNetworkStatus, named, &st); err != nil {
		t.Fatalf("network.status for room-b: %v", err)
	}
	if st.RoomID != "room-b" || st.Subnet != "10.200.8.0/24" {
		t.Errorf("named room returned %+v, want room-b's own addressing", st)
	}
}

func TestNetworkRoutesReturnsTheTable(t *testing.T) {
	f := &fakeNetwork{
		rooms: []string{"room-a"},
		status: map[string]protocol.NetworkStatus{
			"room-a": {
				RoomID: "room-a",
				Routes: []protocol.RouteEntry{
					{Destination: "10.200.7.0/24", NextHop: "10.200.7.1", Managed: true, Note: "room subnet, on-link"},
					{Destination: "10.200.7.2/32", NextHop: "10.200.7.2", Peer: "peer-1"},
				},
			},
		},
	}
	_, c := withNetwork(t, f)

	var routes []protocol.RouteEntry
	if err := c.Call(context.Background(), protocol.MethodNetworkRoutes, nil, &routes); err != nil {
		t.Fatalf("network.routes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %+v, want 2 entries", routes)
	}
	if routes[1].Peer != "peer-1" || routes[1].Destination != "10.200.7.2/32" {
		t.Errorf("second route = %+v, want the guest's route", routes[1])
	}
}

// The interface method is the one a user reads off a screen to type an address
// into a game, so it has to be a self-contained answer.
func TestNetworkInterfaceIsTheSmallestUsefulAnswer(t *testing.T) {
	f := &fakeNetwork{
		rooms: []string{"room-a"},
		status: map[string]protocol.NetworkStatus{
			"room-a": {
				RoomID:       "room-a",
				Interface:    "LanBaz-abcdef",
				Adapter:      "wintun",
				Subnet:       "10.200.7.0/24",
				LocalAddress: "10.200.7.1",
				MTU:          1280,
				Broadcast:    true,
				Multicast:    true,
				State:        "ready",
				IsHost:       true,
			},
		},
	}
	_, c := withNetwork(t, f)

	var iface protocol.NetworkInterface
	if err := c.Call(context.Background(), protocol.MethodNetworkInterface, nil, &iface); err != nil {
		t.Fatalf("network.interface: %v", err)
	}
	if iface.Address != "10.200.7.1" || iface.Subnet != "10.200.7.0/24" {
		t.Errorf("interface = %+v, want the room's own addressing", iface)
	}
	if iface.Interface != "LanBaz-abcdef" || iface.Adapter != "wintun" || iface.State != "ready" {
		t.Errorf("interface = %+v, want the adapter named", iface)
	}
	if iface.CheckedAt.IsZero() {
		t.Error("checked_at must be stamped, or a UI cannot tell a stale answer from a fresh one")
	}
}

// The failure paths matter more than the happy one here. A machine with no
// driver, no rights, or no room must be told so in words it can act on.
func TestNetworkMethodsReportWhyThereIsNoNetwork(t *testing.T) {
	t.Run("no network service at all", func(t *testing.T) {
		_, c := withNetwork(t, nil)
		err := c.Call(context.Background(), protocol.MethodNetworkStatus, nil, &protocol.NetworkStatus{})
		if err == nil {
			t.Fatal("network.status succeeded on a build with no virtual network")
		}
		if !strings.Contains(err.Error(), "network.status") {
			t.Errorf("error = %v, want it to name the method that is missing", err)
		}
	})
	t.Run("no room open", func(t *testing.T) {
		_, c := withNetwork(t, &fakeNetwork{})
		err := c.Call(context.Background(), protocol.MethodNetworkStatus, nil, &protocol.NetworkStatus{})
		if err == nil {
			t.Fatal("network.status succeeded with no room open")
		}
		if !strings.Contains(err.Error(), "no room") {
			t.Errorf("error = %v, want it to say no room is open", err)
		}
	})
	t.Run("unknown room", func(t *testing.T) {
		f := &fakeNetwork{rooms: []string{"room-a"}, status: map[string]protocol.NetworkStatus{}}
		_, c := withNetwork(t, f)
		req := struct {
			RoomID string `json:"room_id"`
		}{RoomID: "nope"}
		if err := c.Call(context.Background(), protocol.MethodNetworkStatus, req, &protocol.NetworkStatus{}); err == nil {
			t.Fatal("network.status succeeded for a room that does not exist")
		}
	})
	t.Run("malformed payload", func(t *testing.T) {
		_, c := withNetwork(t, &fakeNetwork{rooms: []string{"room-a"}})
		err := c.Call(context.Background(), protocol.MethodNetworkStatus,
			json.RawMessage(`{"room_id":`), &protocol.NetworkStatus{})
		if err == nil {
			t.Fatal("network.status accepted a truncated payload")
		}
	})
}

// A nil service is a wiring bug, and it should fail at wiring rather than at
// the first query, where the cause would be a confusing "no room" message.
func TestSetNetworkServiceRejectsNil(t *testing.T) {
	var s *Server
	newTestServerBefore(t, nil, func(server *Server) { s = server })
	if err := s.SetNetworkService(nil); err == nil {
		t.Fatal("SetNetworkService accepted nil")
	}
	for _, m := range []string{
		protocol.MethodNetworkStatus,
		protocol.MethodNetworkRoutes,
		protocol.MethodNetworkInterface,
	} {
		if contains(s.Methods(), m) {
			t.Errorf("method %s was registered despite the failure", m)
		}
	}
}

func TestNetworkMethodsAreRegistered(t *testing.T) {
	s, _ := withNetwork(t, &fakeNetwork{
		rooms:  []string{"room-a"},
		status: map[string]protocol.NetworkStatus{"room-a": {RoomID: "room-a"}},
	})
	got := s.Methods()
	for _, m := range []string{
		protocol.MethodNetworkStatus,
		protocol.MethodNetworkRoutes,
		protocol.MethodNetworkInterface,
	} {
		if !contains(got, m) {
			t.Errorf("method %s was not registered: %v", m, got)
		}
	}
}
