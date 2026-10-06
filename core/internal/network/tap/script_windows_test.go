package tap

import (
	"net/netip"
	"os/exec"
	"strings"
	"testing"
)

// The address script is valid PowerShell (parsed, not run).
func TestAddressScriptParses(t *testing.T) {
	s := addressScript("LanBaz-L2", netip.MustParseAddr("10.200.74.1"), 24)
	if !strings.Contains(s, "Enable-NetAdapterBinding -Name 'LanBaz-L2' -ComponentID ms_tcpip") {
		t.Fatalf("no binding fix:\n%s", s)
	}
	check := "$e=$null; [System.Management.Automation.Language.Parser]::ParseInput(@'\n" + s + "\n'@, [ref]$null, [ref]$e) | Out-Null; if ($e) { $e | % { $_.Message } } else { 'OK' }"
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "OK" {
		t.Fatalf("does not parse: %v %s", err, out)
	}
}
