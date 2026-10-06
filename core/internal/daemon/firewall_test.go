package daemon

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

func TestParseBlockRules(t *testing.T) {
	// What Windows really prints for rules made by a cancelled prompt.
	real := `UDP Query User{467B4010-47A7-4F6A-9352-61A74896C11C}C:\games\gow\wargame-g4wlive.exe|wargame-g4wlive.exe`
	out := real + "\r\n" + "{1a2b3c4d-0000-1111-2222-333344445555}|wargame\r\n" + "no separator here\r\n\r\n"
	got := parseBlockRules(out)
	if len(got) != 2 || got[0].Name != `UDP Query User{467B4010-47A7-4F6A-9352-61A74896C11C}C:\games\gow\wargame-g4wlive.exe` || got[1].DisplayName != "wargame" {
		t.Fatalf("parsed %+v", got)
	}
}

func TestFixScriptQuotesAndScopes(t *testing.T) {
	s := fixScript("gears-of-war", "Gears' War", `C:\Games\it's\wargame.exe`, []protocol.FirewallRule{{Name: "{abc}"}, {Name: "x'; Remove-NetFirewallRule *"}})
	for _, want := range []string{"'{abc}'", "'x''; Remove-NetFirewallRule *'", `'C:\Games\it''s\wargame.exe'`, "'LanBaz: Gears'' War'", "'10.200.0.0/16'", "Disable-NetFirewallRule", "-Direction Inbound -Action Allow"} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
}

// The read-only query is valid PowerShell (it lists nothing for Notepad).
func TestBlockRulesScriptRuns(t *testing.T) {
	if runtime.GOOS != "windows" || testing.Short() {
		t.Skip("Windows only")
	}
	out, err := runPS(context.Background(), blockRulesScript(`C:\Windows\System32\notepad.exe`))
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.Contains(strings.ToLower(out), "error") || strings.Contains(out, "is not recognized") {
		t.Fatalf("script failed: %s", out)
	}
}
