package daemon

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
	"github.com/lanbaz/lanbaz/profiles"
)

func gowProfile() profiles.Profile {
	var p profiles.Profile
	p.ID, p.Name, p.Notes = "gears-of-war", "Gears' of War", "Games for Windows Live title."
	p.Executables.Windows = []string{"wargame-g4wlive.exe"}
	p.Network.Protocol, p.Network.Ports = "udp", []int{3074, 14001}
	return p
}

func TestPortRulesScopeToLanBaz(t *testing.T) {
	s := portRules(gowProfile())
	for _, want := range []string{"-Protocol UDP", "-LocalPort 3074,14001", "'10.200.0.0/16'", "'LanBaz: Gears'' of War ports UDP'"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(s, "TCP") {
		t.Error("a UDP-only game got a TCP rule")
	}
	if runtime.GOOS == "windows" {
		// Parse (not run) the generated PowerShell.
		check := "$e=$null; [System.Management.Automation.Language.Parser]::ParseInput(@'\n" + s + "\n'@, [ref]$null, [ref]$e) | Out-Null; if ($e) { $e | % { $_.Message } } else { 'OK' }"
		out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "OK" {
			t.Fatalf("generated script does not parse: %v %s", err, out)
		}
	}
}

// Without a path and with the game not running, Apply asks for the program.
func TestApplyAsksForTheGameWhenItCannotFindIt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	d := &Daemon{profiles: []profiles.Profile{gowProfile()}}
	res, err := d.applyGame(context.Background(), protocol.GameApplyRequest{GameID: "gears-of-war"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsPath || len(res.Executables) != 1 || res.Steps[0].Step != "firewall" || res.Steps[0].OK {
		t.Fatalf("result %+v", res)
	}
	res, _ = d.applyGame(context.Background(), protocol.GameApplyRequest{GameID: "gears-of-war", Path: `C:\Windows\win.ini`})
	if res.NeedsPath || res.Steps[0].OK {
		t.Fatalf("a non-exe was accepted: %+v", res)
	}
	if _, err := d.applyGame(context.Background(), protocol.GameApplyRequest{GameID: "nope"}); err == nil {
		t.Fatal("unknown game accepted")
	}
}
