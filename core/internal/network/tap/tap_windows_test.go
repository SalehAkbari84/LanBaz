//go:build windows

package tap

import (
	"errors"
	"testing"
)

// The layout of the PC the 0.3.1 failure was seen on: a VPN's branded TAP
// adapter, LanBaz's own, and a ghost adapter another program left behind.
func TestOrderCandidatesPrefersLanBazAndSkipsOtherVPNs(t *testing.T) {
	got, err := orderCandidates([]candidate{
		{guid: "a", alias: "HotspotShield Network Adapter", desc: "HotspotShield TAP-Windows Adapter V9"},
		{guid: "b", alias: "TarGame Virtual Adapter", desc: "TAP-Windows Adapter V9 #2"},
		{guid: "c", alias: "LanBaz-L2", desc: "TAP-Windows Adapter V9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].guid != "c" || got[1].guid != "b" {
		t.Fatalf("order = %+v, want LanBaz-L2 then the generic adapter, and no branded one", got)
	}
}

func TestOrderCandidatesOnlyBrandedMeansNoDriver(t *testing.T) {
	_, err := orderCandidates([]candidate{{guid: "a", alias: "VPN", desc: "HotspotShield TAP-Windows Adapter V9"}})
	if !errors.Is(err, ErrNoDriver) {
		t.Fatalf("err = %v, want ErrNoDriver", err)
	}
}

func TestDefaultAlias(t *testing.T) {
	for alias, want := range map[string]bool{"Ethernet 3": true, "Local Area Connection 2": true, "TarGame Virtual Adapter": false, "LanBaz-L2": false} {
		if got := defaultAlias(alias); got != want {
			t.Errorf("defaultAlias(%q) = %v, want %v", alias, got, want)
		}
	}
}
