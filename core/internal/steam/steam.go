// Package steam helps play during an internet outage with Steam's own
// Offline Mode.
//
// Steam can start and run games with no connection to its servers, as long as
// the account signed in on this PC before, with "Remember password". This
// package reads Steam's own files to say whether a PC is ready for that, lists
// the installed games, and switches Steam's offline-mode setting - the same
// setting Steam's "Go Offline" menu writes - so a player in an outage does
// not have to find it.
//
// It never touches a game's files and never stands in for Steam: a game that
// needs Steam still gets Steam, running offline. Bypassing a game's ownership
// check is out of scope by design.
package steam

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Account is the Steam account Steam will sign in with.
type Account struct {
	ID          string
	AccountName string
	PersonaName string
	// Remembered: Steam keeps the sign-in, so it can start with no
	// connection. Without it, offline mode cannot be entered.
	Remembered bool
	AutoLogin  bool
	MostRecent bool
	// Offline: Steam starts in offline mode next time.
	Offline bool
	// LastLogin is the last online sign-in.
	LastLogin time.Time
}

// Game is one installed Steam game.
type Game struct {
	AppID      string
	Name       string
	InstallDir string // full path
}

// Status is everything the outage helper shows.
type Status struct {
	Root     string
	Accounts []Account
	Games    []Game
}

// Current is the account Steam signs in with next: the most recent one.
func (s Status) Current() *Account {
	var best *Account
	for i := range s.Accounts {
		a := &s.Accounts[i]
		if a.MostRecent {
			return a
		}
		if best == nil || a.LastLogin.After(best.LastLogin) {
			best = a
		}
	}
	return best
}

// Scan reads a Steam installation.
func Scan(root string) (Status, error) {
	st := Status{Root: root}
	if root == "" {
		return st, errors.New("steam: not installed")
	}
	if _, err := os.Stat(filepath.Join(root, "steamapps")); err != nil {
		return st, fmt.Errorf("steam: no steamapps folder in %s", root)
	}
	st.Accounts, _ = readAccounts(root)
	st.Games = scanGames(root)
	return st, nil
}

func loginUsersPath(root string) string { return filepath.Join(root, "config", "loginusers.vdf") }

func readAccounts(root string) ([]Account, error) {
	raw, err := os.ReadFile(loginUsersPath(root))
	if err != nil {
		return nil, err
	}
	doc, err := Parse(string(raw))
	if err != nil {
		return nil, err
	}
	users := doc.Child("users")
	if users == nil {
		return nil, nil
	}
	var out []Account
	for _, u := range users.Children {
		if !u.Obj {
			continue
		}
		a := Account{
			ID:          u.Key,
			AccountName: Unescape(u.Get("AccountName")),
			PersonaName: Unescape(u.Get("PersonaName")),
			Remembered:  u.Get("RememberPassword") == "1",
			AutoLogin:   u.Get("AllowAutoLogin") == "1",
			MostRecent:  u.Get("MostRecent") == "1" || u.Get("mostrecent") == "1",
			Offline:     u.Get("WantsOfflineMode") == "1",
		}
		if ts, err := strconv.ParseInt(u.Get("Timestamp"), 10, 64); err == nil && ts > 0 {
			a.LastLogin = time.Unix(ts, 0).UTC()
		}
		out = append(out, a)
	}
	return out, nil
}

// libraries lists the Steam library folders, the main one first.
func libraries(root string) []string {
	out := []string{root}
	seen := map[string]bool{strings.ToLower(filepath.Clean(root)): true}
	for _, f := range []string{filepath.Join(root, "config", "libraryfolders.vdf"), filepath.Join(root, "steamapps", "libraryfolders.vdf")} {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc, err := Parse(string(raw))
		if err != nil {
			continue
		}
		lf := doc.Child("libraryfolders")
		if lf == nil {
			continue
		}
		for _, c := range lf.Children {
			path := ""
			if c.Obj {
				path = c.Get("path")
			} else if _, err := strconv.Atoi(c.Key); err == nil {
				path = c.Value // the old format: "1" "D:\\Games"
			}
			path = filepath.Clean(Unescape(path))
			if path == "." || path == "" || seen[strings.ToLower(path)] {
				continue
			}
			seen[strings.ToLower(path)] = true
			out = append(out, path)
		}
	}
	return out
}

func scanGames(root string) []Game {
	var out []Game
	seen := map[string]bool{}
	for _, lib := range libraries(root) {
		apps := filepath.Join(lib, "steamapps")
		manifests, _ := filepath.Glob(filepath.Join(apps, "appmanifest_*.acf"))
		for _, m := range manifests {
			raw, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			doc, err := Parse(string(raw))
			if err != nil {
				continue
			}
			app := doc.Child("AppState")
			if app == nil {
				continue
			}
			id, name, dir := app.Get("appid"), Unescape(app.Get("name")), Unescape(app.Get("installdir"))
			if id == "" || dir == "" || seen[id] || isTool(id, name) {
				continue
			}
			// StateFlags bit 4 (value 4) is "fully installed".
			if flags, err := strconv.Atoi(app.Get("StateFlags")); err == nil && flags&4 == 0 {
				continue
			}
			full := filepath.Join(apps, "common", dir)
			if _, err := os.Stat(full); err != nil {
				continue
			}
			seen[id] = true
			out = append(out, Game{AppID: id, Name: name, InstallDir: full})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// isTool leaves out Steam's own runtimes and redistributables.
func isTool(id, name string) bool {
	switch id {
	case "228980", "1070560", "1391110", "1628350", "1493710", "2180100": // Steamworks Common Redistributables, Linux runtimes, Proton
		return true
	}
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "proton ") || strings.HasPrefix(n, "steamworks") || strings.HasPrefix(n, "steam linux runtime")
}

// ErrSteamRunning: Steam rewrites its login file when it exits, so a change
// made while it runs would be lost.
var ErrSteamRunning = errors.New("steam: close Steam first - it overwrites this setting when it exits")

// ErrNotRemembered: offline mode needs a remembered sign-in.
var ErrNotRemembered = errors.New("steam: this account's password is not remembered, so Steam cannot start offline; sign in once online with \"Remember me\"")

// SetOffline turns Steam's offline mode on or off for the account Steam signs
// in with next. It changes WantsOfflineMode and SkipOfflineModeWarning only,
// keeps a backup of the file, and writes it atomically. running says whether
// Steam is running right now.
func SetOffline(root string, on, running bool) (Account, error) {
	if running {
		return Account{}, ErrSteamRunning
	}
	path := loginUsersPath(root)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Account{}, fmt.Errorf("steam: no sign-in on this PC yet (%w)", err)
	}
	doc, err := Parse(string(raw))
	if err != nil {
		return Account{}, err
	}
	accounts, _ := readAccounts(root)
	cur := Status{Accounts: accounts}.Current()
	users := doc.Child("users")
	if cur == nil || users == nil {
		return Account{}, errors.New("steam: no account has signed in on this PC")
	}
	if on && !cur.Remembered {
		return *cur, ErrNotRemembered
	}
	u := users.Child(cur.ID)
	if on {
		u.Set("WantsOfflineMode", "1")
		u.Set("SkipOfflineModeWarning", "1")
	} else {
		u.Set("WantsOfflineMode", "0")
		u.Set("SkipOfflineModeWarning", "0")
	}
	if err := os.WriteFile(path+".lanbaz.bak", raw, 0o644); err != nil {
		return Account{}, fmt.Errorf("steam: cannot keep a backup: %w", err)
	}
	tmp := path + ".lanbaz.tmp"
	if err := os.WriteFile(tmp, []byte(Encode(doc)), 0o644); err != nil {
		return Account{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Account{}, err
	}
	cur.Offline = on
	return *cur, nil
}

// FindExe looks for one of the given program names (lowercase) inside a game's
// folder, a few levels deep, and returns the first match.
func FindExe(dir string, names map[string]bool) string {
	if len(names) == 0 {
		return ""
	}
	base := strings.Count(filepath.Clean(dir), string(filepath.Separator))
	visited := 0
	found := ""
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable folder: skip it, keep looking
		}
		visited++
		if visited > 20000 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if strings.Count(filepath.Clean(p), string(filepath.Separator))-base > 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if names[strings.ToLower(d.Name())] {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// InLibrary reports whether path is inside one of the Steam libraries.
func InLibrary(root, path string) bool {
	p := strings.ToLower(filepath.Clean(path))
	for _, lib := range libraries(root) {
		l := strings.ToLower(filepath.Clean(filepath.Join(lib, "steamapps", "common"))) + string(filepath.Separator)
		if strings.HasPrefix(p, l) {
			return true
		}
	}
	return false
}
