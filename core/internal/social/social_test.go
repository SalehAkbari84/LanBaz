package social

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

type node struct {
	svc   *Service
	mu    sync.Mutex
	joins []Message
}

func newNode(t *testing.T, bus Bus, name string) *node {
	t.Helper()
	sec := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(sec)
	store, _ := OpenStore("")
	n := &node{}
	svc, err := New(Keys{Secret: sec, Public: pub}, bus, store, Hooks{
		Profile: func() Profile { return Profile{Name: name, PeerID: "peer-" + strings.ToLower(name)} },
		Join: func(_ Friend, m Message) {
			n.mu.Lock()
			n.joins = append(n.joins, m)
			n.mu.Unlock()
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	n.svc = svc
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go svc.Run(ctx)
	return n
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func state(n *node, pub string) string {
	f, _ := n.svc.Get(pub)
	return f.State
}

func TestFriendRequestAndAccept(t *testing.T) {
	bus := &MemBus{}
	ali, sara := newNode(t, bus, "Ali"), newNode(t, bus, "Sara")
	eventually(t, "profiles published", func() bool {
		_, _, err := ali.svc.Resolve(context.Background(), sara.svc.Code())
		return err == nil
	})

	f, err := ali.svc.Add(context.Background(), sara.svc.Code())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if f.State != StatePendingOut || f.Name != "Sara" {
		t.Fatalf("after Add: %+v", f)
	}
	eventually(t, "Sara sees the request", func() bool { return state(sara, ali.svc.PublicKey()) == StatePendingIn })
	if g, _ := sara.svc.Get(ali.svc.PublicKey()); g.Name != "Ali" || g.PeerID != "peer-ali" {
		t.Fatalf("request carried the wrong profile: %+v", g)
	}
	if _, err := sara.svc.Respond(context.Background(), ali.svc.PublicKey(), true); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	eventually(t, "both are friends", func() bool {
		return state(ali, sara.svc.PublicKey()) == StateFriend && state(sara, ali.svc.PublicKey()) == StateFriend
	})
	eventually(t, "presence makes Sara online for Ali", func() bool {
		g, _ := ali.svc.Get(sara.svc.PublicKey())
		return ali.svc.IsOnline(g)
	})

	// Join messages flow only between friends.
	if err := ali.svc.Send(context.Background(), sara.svc.PublicKey(), Message{Type: TypeJoinRequest, Room: "lbzroom-abc"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "join request delivered", func() bool {
		sara.mu.Lock()
		defer sara.mu.Unlock()
		return len(sara.joins) == 1 && sara.joins[0].Room == "lbzroom-abc"
	})
}

func TestMutualAddBecomesFriendship(t *testing.T) {
	bus := &MemBus{}
	a, b := newNode(t, bus, "A"), newNode(t, bus, "B")
	eventually(t, "profiles", func() bool {
		_, _, e1 := a.svc.Resolve(context.Background(), b.svc.Code())
		_, _, e2 := b.svc.Resolve(context.Background(), a.svc.Code())
		return e1 == nil && e2 == nil
	})
	if _, err := a.svc.Add(context.Background(), b.svc.Code()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "B has a pending request", func() bool { return state(b, a.svc.PublicKey()) == StatePendingIn })
	if _, err := b.svc.Add(context.Background(), a.svc.Code()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "friends both ways", func() bool {
		return state(a, b.svc.PublicKey()) == StateFriend && state(b, a.svc.PublicKey()) == StateFriend
	})
}

// A squatter publishes a profile under someone else's code; it must not
// resolve, because its key does not hash to that code.
func TestResolveIgnoresSquatters(t *testing.T) {
	bus := &MemBus{}
	victim := newNode(t, bus, "Victim")
	eventually(t, "victim profile", func() bool {
		_, _, err := victim.svc.Resolve(context.Background(), victim.svc.Code())
		return err == nil
	})
	sec := nostr.GeneratePrivateKey()
	content, _ := json.Marshal(Profile{Name: "Fake"})
	ev := nostr.Event{Kind: profileKind, CreatedAt: nostr.Now() + 10,
		Tags: nostr.Tags{{"d", profileTag + victim.svc.Code()}}, Content: string(content)}
	_ = ev.Sign(sec)
	_ = bus.Publish(context.Background(), ev)

	pub, p, err := victim.svc.Resolve(context.Background(), victim.svc.Code())
	if err != nil || pub != victim.svc.PublicKey() || p.Name != "Victim" {
		t.Fatalf("resolved to %s %+v %v, want the victim", pub, p, err)
	}
}

// Strangers may only send friend requests, and only one per hour; join
// messages from them are dropped.
func TestStrangersAreLimited(t *testing.T) {
	bus := &MemBus{}
	target, stranger := newNode(t, bus, "Target"), newNode(t, bus, "Stranger")
	_ = stranger.svc.Send(context.Background(), target.svc.PublicKey(), Message{Type: TypeJoinInvite, Room: "lbzroom-x", Code: "LBZ-evil"})
	_ = stranger.svc.Send(context.Background(), target.svc.PublicKey(), Message{Type: TypeFriendRequest, Name: "S1"})
	_ = stranger.svc.Send(context.Background(), target.svc.PublicKey(), Message{Type: TypeFriendRequest, Name: "S2"})
	eventually(t, "first request recorded", func() bool { return state(target, stranger.svc.PublicKey()) == StatePendingIn })
	time.Sleep(200 * time.Millisecond)
	target.mu.Lock()
	joins := len(target.joins)
	target.mu.Unlock()
	if joins != 0 {
		t.Fatal("a join message from a stranger was delivered")
	}
	if f, _ := target.svc.Get(stranger.svc.PublicKey()); f.Name != "S1" {
		t.Fatalf("the second request was not rate-limited: name %q", f.Name)
	}
}

func TestMessageValidation(t *testing.T) {
	now := time.Now()
	ok := Message{Type: TypeJoinRequest, ID: "0123456789ab", At: now.Unix(), Room: "lbzroom-abc"}
	if err := ok.validate(now); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}
	for name, m := range map[string]Message{
		"stale":     {Type: TypeJoinRequest, ID: "0123456789ab", At: now.Add(-11 * time.Minute).Unix()},
		"future":    {Type: TypeJoinRequest, ID: "0123456789ab", At: now.Add(5 * time.Minute).Unix()},
		"type":      {Type: "rm -rf", ID: "0123456789ab", At: now.Unix()},
		"room":      {Type: TypeJoinRequest, ID: "0123456789ab", At: now.Unix(), Room: "x’;calc"},
		"name":      {Type: TypePresence, ID: "0123456789ab", At: now.Unix(), Name: "a\nb"},
		"oversized": {Type: TypeJoinInvite, ID: "0123456789ab", At: now.Unix(), Code: string(make([]byte, maxCode+1))},
	} {
		if err := m.validate(now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFriendCodeShape(t *testing.T) {
	_, pub := nostr.GeneratePrivateKey(), ""
	pub, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	c := FriendCode(pub)
	if len(c) != 15 || c[:4] != "LBZ-" || NormalizeCode(c) != c {
		t.Fatalf("code %q", c)
	}
	if NormalizeCode("lbz "+c[4:9]+c[10:]) != c {
		t.Fatal("a typed code with spaces and lower case did not normalize")
	}
	if NormalizeCode("hello") != "" {
		t.Fatal("garbage normalized to a code")
	}
}
