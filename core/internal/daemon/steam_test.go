package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
	"github.com/lanbaz/lanbaz/profiles"
)

// steamDaemon is a daemon with just enough wired for the Steam helper, over
// a fake Steam folder holding one game.
func steamDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "config", "loginusers.vdf"), "\"users\"\n{\n\t\"1\"\n\t{\n\t\t\"AccountName\"\t\t\"ali\"\n\t\t\"RememberPassword\"\t\t\"1\"\n\t\t\"MostRecent\"\t\t\"1\"\n\t}\n}\n")
	write(filepath.Join(root, "steamapps", "appmanifest_2700.acf"), "\"AppState\"\n{\n\t\"appid\"\t\t\"2700\"\n\t\"name\"\t\t\"Generals\"\n\t\"installdir\"\t\t\"CNC\"\n\t\"StateFlags\"\t\t\"4\"\n}\n")
	write(filepath.Join(root, "steamapps", "common", "CNC", "Command and Conquer Generals Zero Hour", "generals.exe"), "MZ")

	var gen, zh profiles.Profile
	gen.ID, gen.Name = "cnc-generals", "Generals"
	gen.Executables.Windows = []string{"generals.exe"}
	zh.ID, zh.Name, zh.NeedsL2 = "cnc-zero-hour", "Zero Hour", true
	zh.Executables.Windows = []string{"generals.exe"}
	zh.PathHints = []string{"zero hour"}
	zh.Availability = []string{"drm_free"}

	d := &Daemon{profiles: []profiles.Profile{gen, zh}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	d.cfg.StateDir = t.TempDir()
	d.steam.locate = func() string { return root }
	d.steam.running = func() bool { return false }
	return d, root
}

func TestSteamStatusMatchesProfiles(t *testing.T) {
	d, _ := steamDaemon(t)
	st := d.steamStatus()
	if !st.Installed || st.Account == nil || st.Account.Name != "ali" || !st.Account.Remembered || st.Account.Offline {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Games) != 1 {
		t.Fatalf("games = %+v", st.Games)
	}
	g := st.Games[0]
	// Both profiles run generals.exe; the path hint picks Zero Hour.
	if g.ProfileID != "cnc-zero-hour" || !g.NeedsL2 || len(g.Availability) != 1 || !strings.HasSuffix(g.Exe, "generals.exe") || g.ExeChosen {
		t.Fatalf("game = %+v", g)
	}
}

func TestSteamSetOfflineAndExe(t *testing.T) {
	d, root := steamDaemon(t)
	st, err := d.steamSetOffline(protocol.SteamSetOfflineRequest{On: true})
	if err != nil || !st.Account.Offline {
		t.Fatalf("offline: %+v %v", st.Account, err)
	}
	d.steam.running = func() bool { return true }
	if _, err := d.steamSetOffline(protocol.SteamSetOfflineRequest{On: false}); err == nil || !strings.Contains(err.Error(), "close Steam") {
		t.Fatalf("while Steam runs: %v", err)
	}
	d.steam.running = func() bool { return false }

	exe := filepath.Join(root, "steamapps", "common", "CNC", "other.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = d.steamSetExe(protocol.SteamSetExeRequest{AppID: "2700", Path: exe})
	if err != nil || st.Games[0].Exe != exe || !st.Games[0].ExeChosen {
		t.Fatalf("set exe: %+v %v", st.Games, err)
	}
	if _, err := d.steamSetExe(protocol.SteamSetExeRequest{AppID: "2700", Path: filepath.Join(root, "config", "loginusers.vdf")}); err == nil {
		t.Fatal("a non-.exe was accepted")
	}
	if _, err := d.steamSetExe(protocol.SteamSetExeRequest{AppID: `..\x`, Path: exe}); err == nil {
		t.Fatal("a bad app id was accepted")
	}
	st, _ = d.steamSetExe(protocol.SteamSetExeRequest{AppID: "2700"})
	if st.Games[0].ExeChosen {
		t.Fatal("clearing the choice did not work")
	}
}

func TestSteamNotInstalled(t *testing.T) {
	d, _ := steamDaemon(t)
	d.steam.locate = func() string { return "" }
	if st := d.steamStatus(); st.Installed || st.Games == nil {
		t.Fatalf("status = %+v", st)
	}
	if _, err := d.steamSetOffline(protocol.SteamSetOfflineRequest{On: true}); err == nil {
		t.Fatal("no error without Steam")
	}
}
