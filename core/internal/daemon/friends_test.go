package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

func call(t *testing.T, c *protocol.Client, method string, in, out any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return c.Call(ctx, method, in, out)
}

func waitFor(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// Two installations become friends with one short code, then the guest joins
// the host's room with one click: no invite or reply is copied by anyone.
func TestFriendsJoinWithoutCopyingCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real WebRTC link")
	}
	bus := &social.MemBus{}
	host, hostCfg := newTestDaemonBus(t, bus)
	guest, guestCfg := newTestDaemonBus(t, bus)
	hc := startDaemon(t, host, hostCfg)
	gc := startDaemon(t, guest, guestCfg)

	var hl, gl protocol.FriendsList
	if err := call(t, hc, protocol.MethodFriendsList, nil, &hl); err != nil {
		t.Fatal(err)
	}
	if err := call(t, gc, protocol.MethodFriendsList, nil, &gl); err != nil {
		t.Fatal(err)
	}
	if len(hl.Code) != 15 {
		t.Fatalf("friend code %q", hl.Code)
	}

	// The guest adds the host by code; the profile may take a moment to land.
	var added protocol.Friend
	waitFor(t, "friends.add", 10*time.Second, func() bool {
		return call(t, gc, protocol.MethodFriendsAdd, protocol.FriendAddRequest{Code: hl.Code}, &added) == nil
	})
	hostPub := added.Pub

	var guestPub string
	waitFor(t, "the host sees the request", 10*time.Second, func() bool {
		var l protocol.FriendsList
		_ = call(t, hc, protocol.MethodFriendsList, nil, &l)
		for _, f := range l.Friends {
			if f.State == protocol.FriendPendingIn {
				guestPub = f.Pub
				return true
			}
		}
		return false
	})
	if err := call(t, hc, protocol.MethodFriendsRespond, protocol.FriendRespondRequest{Pub: guestPub, Accept: true}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the guest sees the friendship", 10*time.Second, func() bool {
		var l protocol.FriendsList
		_ = call(t, gc, protocol.MethodFriendsList, nil, &l)
		return len(l.Friends) == 1 && l.Friends[0].State == protocol.FriendConfirmed
	})

	// The host creates a room; the guest asks to join; the host is prompted.
	var created protocol.RoomCreateResponse
	if err := call(t, hc, protocol.MethodRoomCreate, protocol.RoomCreateRequest{Name: "Friday"}, &created); err != nil {
		t.Fatal(err)
	}
	prompts := make(chan protocol.JoinPrompt, 4)
	hc.On(protocol.EventJoinPrompt, func(m protocol.Message) {
		var p protocol.JoinPrompt
		if json.Unmarshal(m.Payload, &p) == nil {
			prompts <- p
		}
	})
	if err := call(t, gc, protocol.MethodJoinRequest,
		protocol.JoinFriendRequest{Pub: hostPub, RoomID: created.Room.RoomID}, nil); err != nil {
		t.Fatal(err)
	}
	var p protocol.JoinPrompt
	select {
	case p = <-prompts:
	case <-time.After(15 * time.Second):
		t.Fatal("the host was never asked")
	}
	if p.Kind != "request" || p.RoomID != created.Room.RoomID || p.Friend.Pub != guestPub {
		t.Fatalf("prompt %+v", p)
	}
	// Accept, and trust the friend from now on.
	if err := call(t, hc, protocol.MethodJoinRespond, protocol.JoinRespondRequest{ID: p.ID, Accept: true, Always: true}, nil); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the guest is connected to the host", 60*time.Second, func() bool {
		var rooms []protocol.RoomSummary
		if call(t, hc, protocol.MethodRoomList, nil, &rooms) != nil || len(rooms) != 1 {
			return false
		}
		for _, peer := range rooms[0].Peers {
			if peer.PeerID == protocol.PeerID(guest.id) && (peer.State == protocol.PeerActive || peer.State == protocol.PeerNetworkReady) {
				return true
			}
		}
		return false
	})

	var l protocol.FriendsList
	_ = call(t, hc, protocol.MethodFriendsList, nil, &l)
	if !l.Friends[0].Trusted {
		t.Fatal("Always did not mark the friend as trusted")
	}

	// Later: the guest leaves and asks again. A trusted friend gets straight
	// back in, without a prompt.
	if err := call(t, gc, protocol.MethodRoomLeave, map[string]string{"room_id": created.Room.RoomID}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host notices the guest left", 30*time.Second, func() bool {
		var rooms []protocol.RoomSummary
		_ = call(t, hc, protocol.MethodRoomList, nil, &rooms)
		return len(rooms) == 1 && rooms[0].PeerCount <= 1
	})
	if err := call(t, gc, protocol.MethodJoinRequest, protocol.JoinFriendRequest{Pub: hostPub}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the trusted friend is back in", 60*time.Second, func() bool {
		var rooms []protocol.RoomSummary
		if call(t, gc, protocol.MethodRoomList, nil, &rooms) != nil || len(rooms) != 1 {
			return false
		}
		for _, peer := range rooms[0].Peers {
			if peer.PeerID == protocol.PeerID(host.id) && (peer.State == protocol.PeerActive || peer.State == protocol.PeerNetworkReady) {
				return true
			}
		}
		return false
	})
	select {
	case extra := <-prompts:
		t.Fatalf("a trusted friend still prompted the host: %+v", extra)
	default:
	}
}
