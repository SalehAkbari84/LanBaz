package social

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// TestLiveRelays connects to real relays. Only runs when asked:
//
//	LANBAZ_LIVE_NOSTR=1 go test ./core/internal/social -run LiveRelays -v
func TestLiveRelays(t *testing.T) {
	if os.Getenv("LANBAZ_LIVE_NOSTR") == "" {
		t.Skip("set LANBAZ_LIVE_NOSTR=1")
	}
	for _, u := range DefaultRelays {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		r, err := nostr.RelayConnect(ctx, u)
		if err != nil {
			t.Logf("%-28s FAIL %v", u, err)
			cancel()
			continue
		}
		t.Logf("%-28s ok (%s)", u, time.Since(start).Round(time.Millisecond))
		_ = r.Close()
		cancel()
	}
}

// TestLiveFriendRequest runs a whole friend request through the real relays.
func TestLiveFriendRequest(t *testing.T) {
	if os.Getenv("LANBAZ_LIVE_NOSTR") == "" {
		t.Skip("set LANBAZ_LIVE_NOSTR=1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newNode(t, NewRelayBus(ctx, DefaultRelays), "LiveA")
	b := newNode(t, NewRelayBus(ctx, DefaultRelays), "LiveB")
	start := time.Now()
	var err error
	for i := 0; i < 10; i++ {
		if _, err = a.svc.Add(ctx, b.svc.Code()); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		t.Fatalf("Add over relays: %v", err)
	}
	t.Logf("code resolved and request sent after %s", time.Since(start).Round(time.Millisecond))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && state(b, a.svc.PublicKey()) != StatePendingIn {
		time.Sleep(200 * time.Millisecond)
	}
	if state(b, a.svc.PublicKey()) != StatePendingIn {
		t.Fatal("the request never arrived through the relays")
	}
	t.Logf("request delivered after %s", time.Since(start).Round(time.Millisecond))
	if _, err := b.svc.Respond(ctx, a.svc.PublicKey(), true); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) && state(a, b.svc.PublicKey()) != StateFriend {
		time.Sleep(200 * time.Millisecond)
	}
	if state(a, b.svc.PublicKey()) != StateFriend {
		t.Fatal("the acceptance never arrived")
	}
	t.Logf("friends both ways after %s", time.Since(start).Round(time.Millisecond))
}
