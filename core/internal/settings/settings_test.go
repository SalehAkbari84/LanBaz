package settings

import (
	"testing"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := EffectiveSTUN(s.Get()); len(got) == 0 {
		t.Fatal("no default STUN servers")
	}
	want := protocol.Settings{
		DisplayName: "  Ali\x01  ",
		STUNServers: []string{"stun:stun.example.com:3478"},
		TURNServers: []protocol.RelayServer{{URL: "turn:relay.example.com:3478?transport=udp", Username: "u", Credential: "p"}},
		AllowRelay:  true,
	}
	if _, err := s.Set(want); err != nil {
		t.Fatalf("set: %v", err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := again.Get()
	if got.DisplayName != "Ali" || !got.AllowRelay || len(got.TURNServers) != 1 || got.STUNServers[0] != "stun:stun.example.com:3478" {
		t.Fatalf("settings did not round trip: %+v", got)
	}
}

func TestSettingsRejectBadInput(t *testing.T) {
	bad := []protocol.Settings{
		{STUNServers: []string{"http://example.com"}},
		{TURNServers: []protocol.RelayServer{{URL: "stun:example.com"}}},
		{AllowRelay: true},
		{STUNServers: []string{"stun:"}},
	}
	for i, b := range bad {
		if _, err := Normalize(b); err == nil {
			t.Errorf("case %d: %+v was accepted", i, b)
		}
	}
}
