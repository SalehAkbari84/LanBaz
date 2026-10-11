package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/steam"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
	"github.com/lanbaz/lanbaz/profiles"
)

// Steam during an outage. See package steam for what this does and, as
// importantly, what it does not do.

const steamExesFile = "steam-exes.json"

type steamState struct {
	mu sync.Mutex
	// match caches the profile and program found in each install folder; a
	// folder walk per game is too slow to repeat on every status call.
	match   map[string]steamMatch
	matchAt time.Time
	// locate and running are swapped in tests.
	locate  func() string
	running func() bool
}

type steamMatch struct {
	profile *profiles.Profile
	exe     string
}

const steamMatchTTL = 2 * time.Minute

func (d *Daemon) steamExesPath() string { return filepath.Join(d.cfg.StateDir, steamExesFile) }

func (d *Daemon) steamExes() map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(d.steamExesPath())
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// steamProfile finds the LanBaz profile for a game folder by looking for one
// of the profile's programs in it. Games that share a program name (Generals
// and Zero Hour) are told apart by the profile's path hints.
func (d *Daemon) steamProfile(dir string) steamMatch {
	names := map[string]bool{}
	byExe := map[string][]profiles.Profile{}
	for _, p := range d.profiles {
		for _, exe := range p.Executables.Windows {
			e := strings.ToLower(exe)
			names[e] = true
			byExe[e] = append(byExe[e], p)
		}
	}
	exe := steam.FindExe(dir, names)
	if exe == "" {
		return steamMatch{}
	}
	cands := byExe[strings.ToLower(filepath.Base(exe))]
	lower := strings.ToLower(exe)
	for i := range cands {
		for _, h := range cands[i].PathHints {
			if h != "" && strings.Contains(lower, strings.ToLower(h)) {
				return steamMatch{profile: &cands[i], exe: exe}
			}
		}
	}
	for i := range cands {
		if len(cands[i].PathHints) == 0 {
			return steamMatch{profile: &cands[i], exe: exe}
		}
	}
	return steamMatch{profile: &cands[0], exe: exe}
}

func (d *Daemon) steamStatus() protocol.SteamStatus {
	d.steam.mu.Lock()
	locate, running := d.steam.locate, d.steam.running
	d.steam.mu.Unlock()
	root := locate()
	out := protocol.SteamStatus{Games: []protocol.SteamGame{}}
	if root == "" {
		return out
	}
	st, err := steam.Scan(root)
	if err != nil {
		return out
	}
	out.Installed = true
	out.Path = root
	out.Running = running()
	if a := st.Current(); a != nil {
		name := a.PersonaName
		if name == "" {
			name = a.AccountName
		}
		out.Account = &protocol.SteamAccount{Name: name, Remembered: a.Remembered, Offline: a.Offline, LastLogin: a.LastLogin}
	}

	d.steam.mu.Lock()
	if d.steam.match == nil || time.Since(d.steam.matchAt) > steamMatchTTL {
		d.steam.match = map[string]steamMatch{}
		d.steam.matchAt = time.Now()
	}
	cache := d.steam.match
	d.steam.mu.Unlock()

	chosen := d.steamExes()
	for _, g := range st.Games {
		d.steam.mu.Lock()
		m, ok := cache[g.InstallDir]
		d.steam.mu.Unlock()
		if !ok {
			m = d.steamProfile(g.InstallDir)
			d.steam.mu.Lock()
			cache[g.InstallDir] = m
			d.steam.mu.Unlock()
		}
		sg := protocol.SteamGame{AppID: g.AppID, Name: g.Name, InstallDir: g.InstallDir, Exe: m.exe}
		if m.profile != nil {
			sg.ProfileID = m.profile.ID
			sg.ProfileName = m.profile.Name
			sg.NeedsL2 = m.profile.NeedsL2
			sg.Availability = m.profile.Availability
		}
		if p := chosen[g.AppID]; p != "" {
			if _, err := os.Stat(p); err == nil {
				sg.Exe, sg.ExeChosen = p, true
			}
		}
		out.Games = append(out.Games, sg)
	}
	return out
}

func (d *Daemon) steamSetOffline(req protocol.SteamSetOfflineRequest) (protocol.SteamStatus, error) {
	d.steam.mu.Lock()
	locate, running := d.steam.locate, d.steam.running
	d.steam.mu.Unlock()
	root := locate()
	if root == "" {
		return protocol.SteamStatus{}, protocol.NewError(protocol.CodeNotFound, "Steam is not installed on this PC")
	}
	acc, err := steam.SetOffline(root, req.On, running())
	switch {
	case err == steam.ErrSteamRunning:
		return protocol.SteamStatus{}, protocol.NewError(protocol.CodeBadRequest, "close Steam first (Steam menu → Exit); it overwrites this setting when it exits")
	case err == steam.ErrNotRemembered:
		return protocol.SteamStatus{}, protocol.NewError(protocol.CodeBadRequest, "Steam has not remembered this account's sign-in, so it cannot start offline. While the internet works, sign in to Steam once with \"Remember me\" ticked.")
	case err != nil:
		return protocol.SteamStatus{}, protocol.NewErrorf(protocol.CodeInternal, "could not change Steam's setting: %v", err)
	}
	d.log.Info("Steam offline mode changed", "on", req.On, "account", acc.AccountName)
	return d.steamStatus(), nil
}

func (d *Daemon) steamSetExe(req protocol.SteamSetExeRequest) (protocol.SteamStatus, error) {
	if req.AppID == "" || strings.ContainsAny(req.AppID, `/\.`) {
		return protocol.SteamStatus{}, protocol.NewError(protocol.CodeBadRequest, "bad app id")
	}
	exes := d.steamExes()
	if req.Path == "" {
		delete(exes, req.AppID)
	} else {
		st, err := os.Stat(req.Path)
		if err != nil || !st.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(req.Path), ".exe") {
			return protocol.SteamStatus{}, protocol.NewError(protocol.CodeBadRequest, "choose the game's .exe file")
		}
		exes[req.AppID] = filepath.Clean(req.Path)
	}
	raw, _ := json.MarshalIndent(exes, "", "  ")
	if err := os.WriteFile(d.steamExesPath(), raw, 0o600); err != nil {
		return protocol.SteamStatus{}, protocol.NewErrorf(protocol.CodeInternal, "could not save: %v", err)
	}
	return d.steamStatus(), nil
}

func (d *Daemon) registerSteam() error {
	d.steam.mu.Lock()
	if d.steam.locate == nil {
		d.steam.locate = steam.Locate
	}
	if d.steam.running == nil {
		d.steam.running = steam.Running
	}
	d.steam.mu.Unlock()
	if err := d.api.Register(protocol.MethodSteamStatus, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return d.steamStatus(), nil
	}); err != nil {
		return err
	}
	if err := d.api.Register(protocol.MethodSteamSetOffline, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req protocol.SteamSetOfflineRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, protocol.NewErrorf(protocol.CodeBadRequest, "bad request: %v", err)
		}
		return d.steamSetOffline(req)
	}); err != nil {
		return err
	}
	return d.api.Register(protocol.MethodSteamSetExe, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req protocol.SteamSetExeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, protocol.NewErrorf(protocol.CodeBadRequest, "bad request: %v", err)
		}
		return d.steamSetExe(req)
	})
}
