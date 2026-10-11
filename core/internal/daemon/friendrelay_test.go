package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/lanbaz/lanbaz/core/internal/friendrelay"
	"github.com/lanbaz/lanbaz/core/internal/network/portmap"
	"github.com/lanbaz/lanbaz/core/internal/settings"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

func relayDaemon(t *testing.T) *Daemon {
	t.Helper()
	store, err := settings.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{settings: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	d.relay.firewall = func(context.Context, int, bool) error { return nil }
	return d
}

func TestFriendRelayOnOffKeepsItsCode(t *testing.T) {
	d := relayDaemon(t)
	d.relay.findPublic = func(context.Context) (netip.Addr, portmap.Client, error) {
		return netip.MustParseAddr("127.0.0.1"), nil, nil
	}
	ctx := context.Background()
	// A port unlikely to be busy in CI.
	cur := d.settings.Get()
	cur.FriendRelay = &protocol.FriendRelayHost{Port: 34880}
	if _, err := d.settings.Set(cur); err != nil {
		t.Fatal(err)
	}
	st, err := d.setFriendRelay(ctx, protocol.FriendRelaySetRequest{Enabled: true})
	if err != nil || !st.Running || st.Public != "127.0.0.1" || st.Port != 34880 || st.Via != "public" || st.Code == "" {
		t.Fatalf("status = %+v, err = %v", st, err)
	}
	code, err := friendrelay.ParseCode(st.Code)
	if err != nil || code.Port != 34880 || code.Pass == "" {
		t.Fatalf("code = %+v %v", code, err)
	}
	// Off and on again: the same credential, so friends keep their code.
	if st, _ := d.setFriendRelay(ctx, protocol.FriendRelaySetRequest{Enabled: false}); st.Running || st.Code != "" {
		t.Fatalf("still running: %+v", st)
	}
	st2, err := d.setFriendRelay(ctx, protocol.FriendRelaySetRequest{Enabled: true})
	if err != nil || st2.Code != st.Code {
		t.Fatalf("the code changed after a restart:\n%s\n%s", st.Code, st2.Code)
	}
	d.stopFriendRelay()
}

func TestFriendRelayExplainsCGNAT(t *testing.T) {
	d := relayDaemon(t)
	d.relay.findPublic = func(context.Context) (netip.Addr, portmap.Client, error) {
		return netip.Addr{}, nil, portmap.ErrNotPublic
	}
	st, err := d.setFriendRelay(context.Background(), protocol.FriendRelaySetRequest{Enabled: true})
	if err != nil || st.Running || st.Error != "cgnat" || !st.Enabled {
		t.Fatalf("status = %+v, err = %v", st, err)
	}
	d.relay.findPublic = func(context.Context) (netip.Addr, portmap.Client, error) {
		return netip.Addr{}, nil, errors.New("no router")
	}
	st, _ = d.setFriendRelay(context.Background(), protocol.FriendRelaySetRequest{Enabled: true})
	if !strings.HasPrefix(st.Error, "no_router") {
		t.Fatalf("error = %q", st.Error)
	}
}

func TestAddingAFriendsRelay(t *testing.T) {
	d := relayDaemon(t)
	code := friendrelay.Code{V: 1, Host: "5.160.1.2", Port: 3478, User: "lanbaz", Pass: "secret-1", Name: "Ali"}.Encode()
	st, err := d.addFriendRelay(protocol.FriendRelayAddRequest{Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if st.STUNServers[0] != "stun:5.160.1.2:3478" || len(st.STUNServers) != 1+len(settings.DefaultSTUN) {
		t.Fatalf("stun = %v (the friend first, the built-in ones kept)", st.STUNServers)
	}
	if len(st.TURNServers) != 1 || st.TURNServers[0].URL != "turn:5.160.1.2:3478?transport=udp" || st.TURNServers[0].Credential != "secret-1" || !st.AllowRelay {
		t.Fatalf("turn = %+v relay = %v", st.TURNServers, st.AllowRelay)
	}
	// The same friend again with a new password: replaced, not duplicated.
	code2 := friendrelay.Code{V: 1, Host: "5.160.1.2", Port: 3478, User: "lanbaz", Pass: "secret-2"}.Encode()
	st, err = d.addFriendRelay(protocol.FriendRelayAddRequest{Code: code2})
	if err != nil || len(st.TURNServers) != 1 || st.TURNServers[0].Credential != "secret-2" || len(st.STUNServers) != 1+len(settings.DefaultSTUN) {
		t.Fatalf("second add: %+v %v", st, err)
	}
	if _, err := d.addFriendRelay(protocol.FriendRelayAddRequest{Code: "lanbaz-relay:garbage"}); err == nil {
		t.Fatal("a damaged code was accepted")
	}
}

func TestNostrRelaySettings(t *testing.T) {
	d := relayDaemon(t)
	cur := d.settings.Get()
	cur.NostrRelays = []string{" wss://relay.example.ir ", ""}
	st, err := d.settings.Set(cur)
	if err != nil || len(st.NostrRelays) != 1 || st.NostrRelays[0] != "wss://relay.example.ir" {
		t.Fatalf("%+v %v", st.NostrRelays, err)
	}
	cur.NostrRelays = []string{"https://not-a-relay"}
	if _, err := d.settings.Set(cur); err == nil {
		t.Fatal("a non-websocket relay was accepted")
	}
}
