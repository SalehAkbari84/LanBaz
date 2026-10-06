package games

import (
	"net/netip"
	"testing"

	"github.com/lanbaz/lanbaz/profiles"
)

func fake(procs ...Process) *Detector {
	ps, _ := profiles.Load()
	d := New(ps)
	d.scan = func() ([]Process, error) { return procs, nil }
	return d
}

func TestDetectPrefersAHostingKnownGame(t *testing.T) {
	any4 := netip.IPv4Unspecified()
	d := fake(
		Process{PID: 1, Exe: "chrome.exe", Sockets: []Socket{{Proto: "tcp", Addr: any4, Port: 443}}},
		Process{PID: 2, Exe: "javaw.exe", Sockets: []Socket{{Proto: "tcp", Addr: any4, Port: 53211}, {Proto: "udp", Addr: any4, Port: 4445}}},
	)
	got, ok := d.Detect()
	if !ok || got.ID() != "minecraft" {
		t.Fatalf("detected %+v ok=%v, want minecraft", got, ok)
	}
	if !got.Hosting() || got.Ports[0] != 53211 {
		t.Fatalf("ports = %v, want the TCP listener 53211", got.Ports)
	}
}

func TestDetectFindsAnUnknownServerBoundToLanBaz(t *testing.T) {
	d := fake(Process{PID: 3, Exe: "mygame.exe", Sockets: []Socket{{Proto: "udp", Addr: netip.MustParseAddr("10.200.5.1"), Port: 9000}}})
	got, ok := d.Detect()
	if !ok || got.ID() != "unknown" || got.Exe != "mygame.exe" || got.Ports[0] != 9000 {
		t.Fatalf("detected %+v ok=%v", got, ok)
	}
}

func TestDetectIgnoresOrdinaryPrograms(t *testing.T) {
	d := fake(Process{PID: 1, Exe: "explorer.exe"}, Process{PID: 2, Exe: "chrome.exe", Sockets: []Socket{{Proto: "tcp", Addr: netip.IPv4Unspecified(), Port: 9222}}})
	if got, ok := d.Detect(); ok {
		t.Fatalf("detected %+v, want nothing", got)
	}
}

// The real scanner runs and returns this process's own list without error.
func TestScanProcessesRuns(t *testing.T) {
	procs, err := scanProcesses(func(string) bool { return true })
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	_ = procs
}

func TestDetectTellsZeroHourFromGenerals(t *testing.T) {
	zh := fake(Process{PID: 7, Exe: "generals.exe", Path: `D:\Games\Command and Conquer Generals Zero Hour\generals.exe`})
	if got, ok := zh.Detect(); !ok || got.ID() != "cnc-generals-zh" {
		t.Fatalf("detected %q, want cnc-generals-zh", got.ID())
	}
	base := fake(Process{PID: 7, Exe: "generals.exe", Path: `D:\Games\Command and Conquer Generals\generals.exe`})
	if got, ok := base.Detect(); !ok || got.ID() != "cnc-generals" {
		t.Fatalf("detected %q, want cnc-generals", got.ID())
	}
}

// Windows' own NetBIOS listener binds to every adapter, LanBaz's included, and
// was reported as "system is hosting on 137, 138, 139".
func TestDetectIgnoresWindowsServicesOnTheLanBazAdapter(t *testing.T) {
	lan := netip.MustParseAddr("10.200.5.2")
	d := fake(
		Process{PID: 4, Exe: "system", Sockets: []Socket{{Proto: "udp", Addr: lan, Port: 137}, {Proto: "udp", Addr: lan, Port: 138}, {Proto: "tcp", Addr: lan, Port: 139}}},
		Process{PID: 900, Exe: "svchost.exe", Sockets: []Socket{{Proto: "udp", Addr: lan, Port: 50000}}},
		Process{PID: 901, Exe: "tool.exe", Sockets: []Socket{{Proto: "udp", Addr: lan, Port: 5353}}},
	)
	if got, ok := d.Detect(); ok {
		t.Fatalf("detected %+v, want nothing", got)
	}
}

// Steam binds its LAN discovery and streaming ports on every adapter, LanBaz's
// included, and was reported as the game while Gears of War was running.
func TestDetectSeesTheGameNotSteam(t *testing.T) {
	lan := netip.MustParseAddr("10.200.5.2")
	steam := Process{PID: 10, Exe: "steam.exe", Sockets: []Socket{{Proto: "udp", Addr: lan, Port: 27036}, {Proto: "tcp", Addr: lan, Port: 27037}}}
	helper := Process{PID: 11, Exe: "steamwebhelper.exe", Sockets: []Socket{{Proto: "tcp", Addr: lan, Port: 50123}}}
	discord := Process{PID: 12, Exe: "discord.exe", Sockets: []Socket{{Proto: "udp", Addr: lan, Port: 50500}}}
	gow := Process{PID: 20, Exe: "wargame-g4wlive.exe", Sockets: []Socket{{Proto: "udp", Addr: netip.IPv4Unspecified(), Port: 3074}}}

	got, ok := fake(steam, helper, discord, gow).Detect()
	if !ok || got.ID() != "gears-of-war" || !got.Hosting() || got.Ports[0] != 3074 {
		t.Fatalf("detected %+v ok=%v, want gears-of-war hosting on 3074", got, ok)
	}
	if got, ok := fake(steam, helper, discord).Detect(); ok {
		t.Fatalf("launchers alone detected as %+v", got)
	}
}
