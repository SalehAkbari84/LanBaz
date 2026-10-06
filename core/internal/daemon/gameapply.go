package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lanbaz/lanbaz/core/internal/network/gfwl"
	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
	"github.com/lanbaz/lanbaz/profiles"
)

// "Apply" on a game: everything about it LanBaz can set up from outside the
// game, in one click. In-game steps (System Link → Host ...) stay in the hint.
//
//  1. Windows Firewall: the game may take connections from LanBaz addresses
//     (10.200.0.0/16) only - its program and its ports - and block rules left
//     by a cancelled Windows prompt are turned off.
//  2. Network type: a game that needs classic LAN switches the rooms you host
//     to it, live.
//  3. Game specifics: Games for Windows Live is pointed at the LanBaz adapter.

func (d *Daemon) profileByID(id string) (profiles.Profile, bool) {
	for _, p := range d.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return profiles.Profile{}, false
}

// gameExe finds the game's program: the path given, the running game, or a
// known install location from the profile.
func (d *Daemon) gameExe(p profiles.Profile, given string) string {
	if given != "" {
		if st, err := os.Stat(given); err == nil && st.Mode().IsRegular() && strings.EqualFold(filepath.Ext(given), ".exe") {
			return given
		}
		return ""
	}
	d.fw.mu.Lock()
	running := d.fw.last
	d.fw.mu.Unlock()
	if running.GameID == p.ID && running.Path != "" {
		return running.Path
	}
	for _, hint := range p.PathHints {
		for _, exe := range p.Executables.Windows {
			c := filepath.Join(os.ExpandEnv(hint), exe)
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	return ""
}

// portRules allows the profile's ports from the LanBaz range, one rule per
// protocol, replacing earlier LanBaz rules for the game.
func portRules(p profiles.Profile) string {
	if len(p.Network.Ports) == 0 {
		return ""
	}
	var ports []string
	for _, port := range p.Network.Ports {
		if port > 0 && port < 65536 {
			ports = append(ports, fmt.Sprint(port))
		}
	}
	if len(ports) == 0 {
		return ""
	}
	protos := []string{"UDP", "TCP"}
	switch strings.ToLower(p.Network.Protocol) {
	case "udp":
		protos = []string{"UDP"}
	case "tcp":
		protos = []string{"TCP"}
	}
	var b strings.Builder
	for _, proto := range protos {
		name := fmt.Sprintf("LanBaz-GamePorts-%s-%s", p.ID, proto)
		fmt.Fprintf(&b, "Remove-NetFirewallRule -Name %s -ErrorAction SilentlyContinue;"+
			"New-NetFirewallRule -Name %s -DisplayName %s -Group 'LanBaz' -Direction Inbound -Action Allow "+
			"-Protocol %s -LocalPort %s -RemoteAddress %s -Profile Any | Out-Null;",
			psQuote(name), psQuote(name), psQuote("LanBaz: "+p.Name+" ports "+proto), proto,
			strings.Join(ports, ","), psQuote(ipam.Pool.String()))
	}
	return b.String()
}

func (d *Daemon) applyGame(ctx context.Context, req protocol.GameApplyRequest) (protocol.GameApplyResult, error) {
	p, ok := d.profileByID(req.GameID)
	if !ok {
		return protocol.GameApplyResult{}, protocol.NewError(protocol.CodeNotFound, "game: unknown game")
	}
	res := protocol.GameApplyResult{GameID: p.ID, GameName: p.Name}
	step := func(id string, ok bool, detail string) {
		res.Steps = append(res.Steps, protocol.GameApplyStep{Step: id, OK: ok, Detail: detail})
	}

	// 1. Firewall.
	exe := d.gameExe(p, req.Path)
	switch {
	case runtime.GOOS != "windows":
		step("firewall", true, "not Windows")
	case exe == "" && req.Path != "":
		step("firewall", false, "that file is not the game's program (.exe)")
	case exe == "":
		res.NeedsPath = true
		res.Executables = p.Executables.Windows
		step("firewall", false, "need_path")
	default:
		out, _ := runPS(ctx, blockRulesScript(exe))
		blocked := parseBlockRules(out)
		script := portRules(p)
		if len(blocked) > 0 {
			script += fixScript(p.ID, p.Name, exe, blocked)
		} else {
			name := "LanBaz-Game-" + p.ID
			script += fmt.Sprintf("Remove-NetFirewallRule -Name %s -ErrorAction SilentlyContinue;"+
				"New-NetFirewallRule -Name %s -DisplayName %s -Group 'LanBaz' -Direction Inbound -Action Allow "+
				"-Program %s -RemoteAddress %s -Profile Any | Out-Null",
				psQuote(name), psQuote(name), psQuote("LanBaz: "+p.Name), psQuote(exe), psQuote(ipam.Pool.String()))
		}
		if out, err := runPS(ctx, "$ErrorActionPreference='Stop';"+script); err != nil {
			step("firewall", false, strings.TrimSpace(out))
		} else {
			detail := exe
			if len(blocked) > 0 {
				detail += fmt.Sprintf(" (%d blocking rules turned off)", len(blocked))
			}
			step("firewall", true, detail)
			d.log.Info(fmt.Sprintf("Windows Firewall set up for %s: LanBaz players can reach it", p.Name), "path", exe, "unblocked", len(blocked))
			d.fw.mu.Lock()
			recheck := d.fw.last.GameID == p.ID
			d.fw.mu.Unlock()
			if recheck {
				go d.checkGameFirewall(context.Background(), p.ID, p.Name, exe)
			}
		}
	}

	// 2. Network type.
	want := protocol.RoomModeL3
	if p.NeedsL2 {
		want = protocol.RoomModeL2
	}
	if rooms := d.Rooms(); rooms == nil {
		step("network", true, "no rooms")
	} else {
		changed, failed := 0, ""
		hosted := 0
		for _, r := range rooms.All() {
			if !r.IsHost() {
				continue
			}
			hosted++
			if r.Mode() == want || (want == protocol.RoomModeL3 && !p.NeedsL2) {
				continue // a game that works on standard LAN does not force a classic room back
			}
			if _, err := rooms.SetMode(ctx, r.ID(), want); err != nil {
				failed = err.Error()
			} else {
				changed++
			}
		}
		switch {
		case failed != "":
			step("network", false, failed)
		case changed > 0:
			step("network", true, "switched_"+want)
		case hosted == 0 && p.NeedsL2:
			step("network", true, "create_classic")
		default:
			step("network", true, "already_"+want)
		}
	}

	// 3. Game specifics.
	if strings.Contains(strings.ToLower(p.Notes), "games for windows live") {
		adapter := ""
		if rooms := d.Rooms(); rooms != nil {
			for _, r := range rooms.All() {
				if r.Network() != nil {
					adapter = adapterName(r.ID())
					if r.Mode() == protocol.RoomModeL2 {
						adapter = ""
					}
					break
				}
			}
		}
		if adapter == "" {
			step("gfwl", true, "set when a room opens")
		} else if _, _, ok, err := gfwl.Point(adapter); err != nil || !ok {
			detail := "GFWL is not installed on this PC"
			if err != nil {
				detail = err.Error()
			}
			step("gfwl", false, detail)
		} else {
			step("gfwl", true, adapter)
		}
	}
	return res, nil
}

func (d *Daemon) registerGameApply() error {
	return d.api.Register(protocol.MethodGameApply, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req protocol.GameApplyRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, protocol.NewErrorf(protocol.CodeBadRequest, "bad request: %v", err)
		}
		return d.applyGame(ctx, req)
	})
}
