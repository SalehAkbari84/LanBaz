package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// befriend makes two running daemons friends and returns the host's and the
// guest's friend keys.
func befriend(t *testing.T, hc, gc *protocol.Client) (hostPub, guestPub string) {
	t.Helper()
	var hl protocol.FriendsList
	if err := call(t, hc, protocol.MethodFriendsList, nil, &hl); err != nil {
		t.Fatal(err)
	}
	var added protocol.Friend
	waitFor(t, "friends.add", 10*time.Second, func() bool {
		return call(t, gc, protocol.MethodFriendsAdd, protocol.FriendAddRequest{Code: hl.Code}, &added) == nil
	})
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
	return added.Pub, guestPub
}

func connectedTo(t *testing.T, c *protocol.Client, peer string) bool {
	var rooms []protocol.RoomSummary
	if call(t, c, protocol.MethodRoomList, nil, &rooms) != nil {
		return false
	}
	for _, r := range rooms {
		for _, p := range r.Peers {
			if string(p.PeerID) == peer && (p.State == protocol.PeerActive || p.State == protocol.PeerNetworkReady) {
				return true
			}
		}
	}
	return false
}

// addressing returns a host's room subnet and the guest's address in it.
func addressing(t *testing.T, c *protocol.Client, guest string) (subnet, guestAddr string) {
	var rooms []protocol.RoomSummary
	if call(t, c, protocol.MethodRoomList, nil, &rooms) != nil || len(rooms) != 1 {
		return "", ""
	}
	for _, p := range rooms[0].Peers {
		if string(p.PeerID) == guest {
			guestAddr = p.VirtualAddress
		}
	}
	return rooms[0].Subnet, guestAddr
}

// A kept network survives the host restarting: the room is reopened by
// itself and the guest is back in it without anybody clicking anything.
func TestKeptNetworkComesBackAfterHostRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real WebRTC links")
	}
	keepEvery, keepAskEvery = 500*time.Millisecond, 2*time.Second
	t.Cleanup(func() { keepEvery, keepAskEvery = 15*time.Second, time.Minute })

	bus := &social.MemBus{}
	host, hostCfg := newTestDaemonBus(t, bus)
	guest, guestCfg := newTestDaemonBus(t, bus)
	hc := startDaemon(t, host, hostCfg)
	gc := startDaemon(t, guest, guestCfg)
	hostPub, _ := befriend(t, hc, gc)

	var created protocol.RoomCreateResponse
	if err := call(t, hc, protocol.MethodRoomCreate, protocol.RoomCreateRequest{Name: "Always on"}, &created); err != nil {
		t.Fatal(err)
	}
	if err := call(t, hc, protocol.MethodNetworkKeep, protocol.KeepRequest{RoomID: created.Room.RoomID, Keep: true}, nil); err != nil {
		t.Fatal(err)
	}

	// First join: the host answers one prompt (not "always").
	prompts := make(chan protocol.JoinPrompt, 4)
	hc.On(protocol.EventJoinPrompt, func(m protocol.Message) {
		var p protocol.JoinPrompt
		if json.Unmarshal(m.Payload, &p) == nil {
			prompts <- p
		}
	})
	if err := call(t, gc, protocol.MethodJoinRequest, protocol.JoinFriendRequest{Pub: hostPub, RoomID: created.Room.RoomID}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-prompts:
		if err := call(t, hc, protocol.MethodJoinRespond, protocol.JoinRespondRequest{ID: p.ID, Accept: true}, nil); err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the host was never asked")
	}
	waitFor(t, "the guest joins", 60*time.Second, func() bool { return connectedTo(t, gc, string(host.id)) })

	var guestRooms []protocol.RoomSummary
	if err := call(t, gc, protocol.MethodRoomList, nil, &guestRooms); err != nil || len(guestRooms) != 1 {
		t.Fatalf("guest rooms %v %v", guestRooms, err)
	}
	var kept protocol.KeptNetworks
	if err := call(t, gc, protocol.MethodNetworkKeep, protocol.KeepRequest{RoomID: guestRooms[0].RoomID, Keep: true}, &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept.Hosts) != 1 || kept.Hosts[0].Pub != hostPub || len(kept.Rooms) != 1 {
		t.Fatalf("guest kept %+v", kept)
	}

	var subnetBefore, addrBefore string
	waitFor(t, "the guest has an address", 30*time.Second, func() bool {
		subnetBefore, addrBefore = addressing(t, hc, string(guest.id))
		return subnetBefore != "" && addrBefore != ""
	})
	// Let the keeper record the subnet and the lease.
	time.Sleep(2 * keepEvery)

	// The host PC restarts: same state directory, new process, new room id.
	host.Shutdown(time.Second)
	<-host.Done()
	cfg := *hostCfg
	host2, err := New(Options{
		Config: cfg, Token: testToken, Logger: testLogger(),
		BuildInfo: BuildInfo{Version: "1.2.3"}, SocialBus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	hc2 := startDaemon(t, host2, &cfg)
	hc2.On(protocol.EventJoinPrompt, func(m protocol.Message) {
		var p protocol.JoinPrompt
		if json.Unmarshal(m.Payload, &p) == nil {
			prompts <- p
		}
	})

	waitFor(t, "the host reopens the kept network", 20*time.Second, func() bool {
		var k protocol.KeptNetworks
		return call(t, hc2, protocol.MethodNetworkKept, nil, &k) == nil && k.Hosting != nil && len(k.Rooms) == 1
	})
	waitFor(t, "the guest is back without a click", 3*time.Minute, func() bool { return connectedTo(t, gc, string(host2.id)) })
	// Everybody keeps their IP: the same subnet, the same guest address.
	var subnetAfter, addrAfter string
	waitFor(t, "the guest gets its address back", 30*time.Second, func() bool {
		subnetAfter, addrAfter = addressing(t, hc2, string(guest.id))
		return addrAfter != ""
	})
	if subnetAfter != subnetBefore || addrAfter != addrBefore {
		t.Fatalf("addresses changed across the restart: %s %s -> %s %s", subnetBefore, addrBefore, subnetAfter, addrAfter)
	}
	select {
	case extra := <-prompts:
		t.Fatalf("the restarted host prompted for a kept member: %+v", extra)
	default:
	}
}
