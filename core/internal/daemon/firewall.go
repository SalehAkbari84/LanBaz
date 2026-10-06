package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network/ipam"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Windows Firewall block rules for a game.
//
// When somebody once pressed "Cancel" on Windows' "allow this app?" prompt,
// Windows saved inbound *Block* rules for the game. Block beats allow, so the
// rule LanBaz adds for its adapter does not help and friends cannot join a
// game hosted on this PC, with nothing in the game to say why. The daemon
// looks for such rules when a game starts (read-only) and the UI offers a fix:
// disable exactly those rules and allow the game from the LanBaz address range
// only. Nothing changes unless the user presses the button.

type firewallState struct {
	mu   sync.Mutex
	last protocol.GameFirewall
}

// Windows names a prompt's rules "UDP Query User{GUID}C:\path\game.exe":
// any printable text. They are only ever used single-quoted (psQuote).
func printable(s string) bool {
	if s == "" || len(s) > 1024 {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// blockRulesScript lists enabled inbound block rules for one program, one
// "name|display name" per line.
func blockRulesScript(path string) string {
	return fmt.Sprintf("Get-NetFirewallApplicationFilter -Program %s -ErrorAction SilentlyContinue | Get-NetFirewallRule -ErrorAction SilentlyContinue | "+
		"Where-Object { $_.Enabled -eq 'True' -and $_.Direction -eq 'Inbound' -and $_.Action -eq 'Block' } | "+
		"ForEach-Object { $_.Name + '|' + $_.DisplayName }; exit 0", psQuote(path))
}

func parseBlockRules(out string) []protocol.FirewallRule {
	var rules []protocol.FirewallRule
	for _, line := range strings.Split(out, "\n") {
		name, display, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || !printable(name) {
			continue
		}
		rules = append(rules, protocol.FirewallRule{Name: name, DisplayName: display})
	}
	return rules
}

// fixScript disables the given block rules and allows the program from the
// LanBaz range (all profiles), replacing an earlier LanBaz rule for it.
func fixScript(gameID, gameName, path string, rules []protocol.FirewallRule) string {
	var names []string
	for _, r := range rules {
		names = append(names, psQuote(r.Name))
	}
	ruleName := "LanBaz-Game-" + gameID
	return fmt.Sprintf("$ErrorActionPreference='Stop';"+
		"Get-NetFirewallRule -Name @(%s) | Disable-NetFirewallRule;"+
		"Remove-NetFirewallRule -Name %s -ErrorAction SilentlyContinue;"+
		"New-NetFirewallRule -Name %s -DisplayName %s -Group 'LanBaz' -Direction Inbound -Action Allow "+
		"-Program %s -RemoteAddress %s -Profile Any | Out-Null",
		strings.Join(names, ","), psQuote(ruleName), psQuote(ruleName), psQuote("LanBaz: "+gameName),
		psQuote(path), psQuote(ipam.Pool.String()))
}

func runPS(ctx context.Context, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	return string(out), err
}

// checkGameFirewall looks for block rules for the running game.
func (d *Daemon) checkGameFirewall(ctx context.Context, gameID, gameName, path string) {
	if runtime.GOOS != "windows" || path == "" {
		return
	}
	out, err := runPS(ctx, blockRulesScript(path))
	if err != nil {
		d.log.Debug("could not read the firewall rules for the game", "game", gameName, "error", err)
		return
	}
	st := protocol.GameFirewall{GameID: gameID, GameName: gameName, Path: path, Blocked: parseBlockRules(out)}
	d.fw.mu.Lock()
	d.fw.last = st
	d.fw.mu.Unlock()
	if len(st.Blocked) > 0 {
		d.log.Warn(fmt.Sprintf("Windows Firewall blocks incoming connections to %s, so friends cannot join a game you host; use Fix in LanBaz", gameName),
			"rules", len(st.Blocked), "path", path)
	}
	d.api.PublishEvent(protocol.EventGameFirewall, st)
}

func (d *Daemon) fixGameFirewall(ctx context.Context) (protocol.GameFirewall, error) {
	d.fw.mu.Lock()
	st := d.fw.last
	d.fw.mu.Unlock()
	if len(st.Blocked) == 0 {
		return st, nil
	}
	if out, err := runPS(ctx, fixScript(st.GameID, st.GameName, st.Path, st.Blocked)); err != nil {
		return st, protocol.NewErrorf(protocol.CodeInternal, "could not change the firewall: %v (%s)", err, strings.TrimSpace(out))
	}
	d.log.Info(fmt.Sprintf("Windows Firewall now lets friends reach %s through LanBaz", st.GameName), "disabled_rules", len(st.Blocked))
	d.checkGameFirewall(ctx, st.GameID, st.GameName, st.Path)
	d.fw.mu.Lock()
	defer d.fw.mu.Unlock()
	return d.fw.last, nil
}

func (d *Daemon) registerFirewall() error {
	if err := d.api.Register(protocol.MethodGameFirewall, func(context.Context, json.RawMessage) (any, error) {
		d.fw.mu.Lock()
		defer d.fw.mu.Unlock()
		return d.fw.last, nil
	}); err != nil {
		return err
	}
	return d.api.Register(protocol.MethodGameFirewallFix, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return d.fixGameFirewall(ctx)
	})
}
