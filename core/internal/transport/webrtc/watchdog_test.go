package webrtc

import (
	"context"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// A guest's link starts checking the moment its reply code exists, and the
// reply then travels to the host by hand. 0.3.1 failed such a link after 16 s;
// this proves a reply applied well after that still connects.
func TestLateAnswerStillConnects(t *testing.T) {
	if testing.Short() {
		t.Skip("waits 20 s")
	}
	host := newTestTransport(t, "room", 1)
	guest := newTestTransport(t, "room", 2)
	defer host.Shutdown(context.Background())
	defer guest.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	hostPeer := transport.PeerInfo{ID: "guest", RoomID: "room"}
	guestPeer := transport.PeerInfo{ID: "host", RoomID: "room"}
	offer, err := host.CreateOffer(ctx, hostPeer)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	answer, err := guest.AcceptOffer(ctx, guestPeer, offer)
	if err != nil {
		t.Fatalf("AcceptOffer: %v", err)
	}
	time.Sleep(20 * time.Second) // a person copying the reply back
	if _, ok := guest.link(guestPeer.ID); !ok {
		t.Fatal("the guest dropped its link while waiting for the host")
	}
	if err := host.ApplyAnswer(ctx, hostPeer, answer); err != nil {
		t.Fatalf("ApplyAnswer after 20 s: %v", err)
	}
	if err := waitUsable(ctx, guest, guestPeer.ID); err != nil {
		t.Fatalf("guest: %v", err)
	}
}

// A link that never connects is given up after the pending bound, with a
// failure the room can act on.
func TestPendingLinkGivesUp(t *testing.T) {
	host := newTestTransport(t, "room", 1)
	guest := newTestTransport(t, "room", 2)
	defer host.Shutdown(context.Background())
	defer guest.Shutdown(context.Background())
	guest.pending = 4 * time.Second
	failed := make(chan transport.StateEvent, 4)
	guest.SetStateHandler(func(ev transport.StateEvent) {
		if ev.State == protocol.PeerFailed {
			failed <- ev
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	offer, err := host.CreateOffer(ctx, transport.PeerInfo{ID: "guest", RoomID: "room"})
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	if _, err := guest.AcceptOffer(ctx, transport.PeerInfo{ID: "host", RoomID: "room"}, offer); err != nil {
		t.Fatalf("AcceptOffer: %v", err)
	}
	select {
	case ev := <-failed:
		if ev.Code != protocol.CodeICEFailed {
			t.Errorf("code = %q, want %q", ev.Code, protocol.CodeICEFailed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the never-connected link was not given up")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := guest.link("host"); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the failed link is still registered")
}
