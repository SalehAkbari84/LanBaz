package steam

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const loginUsers = `"users"
{
	"76561198000000001"
	{
		"AccountName"		"old_account"
		"PersonaName"		"Old"
		"RememberPassword"		"1"
		"MostRecent"		"0"
		"Timestamp"		"1700000000"
	}
	"76561198000000002"
	{
		"AccountName"		"ali"
		"PersonaName"		"Ali \"The\" Player"
		"RememberPassword"		"1"
		"WantsOfflineMode"		"0"
		"AllowAutoLogin"		"1"
		"MostRecent"		"1"
		"Timestamp"		"1760000000"
		"SomeFutureKey"		"keep me"
	}
}
`

// fixture builds a Steam folder with a second library and a few games.
func fixture(t *testing.T) (root, lib2 string) {
	t.Helper()
	root = t.TempDir()
	lib2 = t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "config", "loginusers.vdf"), loginUsers)
	write(filepath.Join(root, "config", "libraryfolders.vdf"), `// written by Steam
"libraryfolders"
{
	"0"
	{
		"path"		"`+strings.ReplaceAll(root, `\`, `\\`)+`"
	}
	"1"
	{
		"path"		"`+strings.ReplaceAll(lib2, `\`, `\\`)+`"
		"apps"
		{
			"9480"		"123"
		}
	}
}
`)
	manifest := func(lib, id, name, dir, flags string) {
		write(filepath.Join(lib, "steamapps", "appmanifest_"+id+".acf"), `"AppState"
{
	"appid"		"`+id+`"
	"name"		"`+name+`"
	"installdir"		"`+dir+`"
	"StateFlags"		"`+flags+`"
}
`)
	}
	manifest(root, "2700", "Command & Conquer Generals", "Generals", "4")
	write(filepath.Join(root, "steamapps", "common", "Generals", "Zero Hour", "generals.exe"), "MZ")
	manifest(lib2, "9480", "Age of Empires II", "AoE2DE", "4")
	write(filepath.Join(lib2, "steamapps", "common", "AoE2DE", "AoE2DE_s.exe"), "MZ")
	manifest(root, "228980", "Steamworks Common Redistributables", "Steamworks Shared", "4")
	write(filepath.Join(root, "steamapps", "common", "Steamworks Shared", "x.txt"), "")
	manifest(root, "555", "Half Downloaded", "Half", "1026")
	write(filepath.Join(root, "steamapps", "common", "Half", "half.exe"), "MZ")
	return root, lib2
}

func TestParseRoundTripKeepsEverything(t *testing.T) {
	doc, err := Parse(loginUsers)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(Encode(doc))
	if err != nil {
		t.Fatal(err)
	}
	if Encode(again) != Encode(doc) {
		t.Fatal("encode is not stable")
	}
	u := again.Child("users").Child("76561198000000002")
	if u.Get("SomeFutureKey") != "keep me" || Unescape(u.Get("PersonaName")) != `Ali "The" Player` {
		t.Fatalf("lost data: %+v", u)
	}
}

func TestParseRejectsBrokenFiles(t *testing.T) {
	for _, s := range []string{`"a" { "b" "c"`, `"a" "b" }`, `"unterminated`} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded", s)
		}
	}
}

func TestScanFindsGamesAndAccount(t *testing.T) {
	root, _ := fixture(t)
	st, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range st.Games {
		names = append(names, g.Name)
	}
	if strings.Join(names, "|") != "Age of Empires II|Command & Conquer Generals" {
		t.Fatalf("games = %v (tools and half-installed games must be left out)", names)
	}
	cur := st.Current()
	if cur == nil || cur.AccountName != "ali" || !cur.Remembered || cur.Offline {
		t.Fatalf("current = %+v", cur)
	}
	if cur.LastLogin.Year() != 2025 {
		t.Fatalf("last login = %v", cur.LastLogin)
	}
}

func TestSetOfflineChangesOnlyTwoFields(t *testing.T) {
	root, _ := fixture(t)
	if _, err := SetOffline(root, true, true); !errors.Is(err, ErrSteamRunning) {
		t.Fatalf("while Steam runs: err = %v", err)
	}
	acc, err := SetOffline(root, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !acc.Offline || acc.AccountName != "ali" {
		t.Fatalf("account = %+v", acc)
	}
	path := filepath.Join(root, "config", "loginusers.vdf")
	bak, err := os.ReadFile(path + ".lanbaz.bak")
	if err != nil || string(bak) != loginUsers {
		t.Fatalf("backup missing or changed: %v", err)
	}
	raw, _ := os.ReadFile(path)
	doc, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := Parse(loginUsers)
	u := doc.Child("users").Child("76561198000000002")
	if u.Get("WantsOfflineMode") != "1" || u.Get("SkipOfflineModeWarning") != "1" {
		t.Fatalf("not switched: %s", raw)
	}
	// Everything else is as it was.
	u.Set("WantsOfflineMode", "0")
	u.Children = u.Children[:len(u.Children)-1] // SkipOfflineModeWarning was added at the end
	if Encode(doc) != Encode(before) {
		t.Fatalf("other fields changed:\n%s", raw)
	}
	// The other account was not touched.
	if doc.Child("users").Child("76561198000000001").Child("WantsOfflineMode") != nil {
		t.Fatal("the other account was changed")
	}
	st, _ := Scan(root)
	if !st.Current().Offline {
		t.Fatal("scan does not see offline mode")
	}
	if _, err := SetOffline(root, false, false); err != nil {
		t.Fatal(err)
	}
	st, _ = Scan(root)
	if st.Current().Offline {
		t.Fatal("still offline after turning it off")
	}
}

func TestSetOfflineNeedsARememberedSignIn(t *testing.T) {
	root, _ := fixture(t)
	path := filepath.Join(root, "config", "loginusers.vdf")
	s := strings.Replace(loginUsers, `"RememberPassword"		"1"
		"WantsOfflineMode"`, `"RememberPassword"		"0"
		"WantsOfflineMode"`, 1)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetOffline(root, true, false); !errors.Is(err, ErrNotRemembered) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path + ".lanbaz.bak"); err == nil {
		t.Fatal("a refused change must not write anything")
	}
}

func TestFindExeAndLibraries(t *testing.T) {
	root, lib2 := fixture(t)
	gen := filepath.Join(root, "steamapps", "common", "Generals")
	if got := FindExe(gen, map[string]bool{"generals.exe": true}); !strings.HasSuffix(got, filepath.Join("Zero Hour", "generals.exe")) {
		t.Fatalf("FindExe = %q", got)
	}
	if got := FindExe(gen, map[string]bool{"nothing.exe": true}); got != "" {
		t.Fatalf("FindExe found %q", got)
	}
	if !InLibrary(root, filepath.Join(lib2, "steamapps", "common", "AoE2DE", "AoE2DE_s.exe")) {
		t.Fatal("second library not recognised")
	}
	if InLibrary(root, filepath.Join(os.TempDir(), "evil.exe")) {
		t.Fatal("a path outside the libraries was accepted")
	}
}

func TestParseSkipsAByteOrderMark(t *testing.T) {
	doc, err := Parse(bom + loginUsers)
	if err != nil || doc.Child("users") == nil {
		t.Fatalf("a file starting with a BOM did not parse: %v", err)
	}
}
