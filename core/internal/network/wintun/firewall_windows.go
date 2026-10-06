//go:build windows

package wintun

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// LanBaz opens exactly one thing in Windows Firewall per adapter: inbound
// traffic that arrives on the LanBaz interface from an address inside LanBaz's
// own 10.200.0.0/16. Nothing on the physical network is exposed, and nothing is
// left behind - the rule is removed together with the adapter.
//
// Without it a game hosted on this machine is unreachable from the room, because
// a new Wintun interface lands in the Public firewall profile, where inbound
// connections to an application are blocked unless the user once clicked
// "allow on public networks". That failure looks exactly like "LanBaz does not
// work", so the rule is part of bringing the adapter up rather than an option.
const (
	firewallGroup  = "LanBaz"
	firewallRemote = "10.200.0.0/16"
)

// firewallRuleName is the stable rule id for one adapter.
func firewallRuleName(adapter string) string { return "LanBaz-In-" + adapter }

// installFirewall adds (or replaces) the inbound allow rule for an adapter.
func installFirewall(ctx context.Context, adapter string, log *slog.Logger) error {
	rule := powershellEscape(firewallRuleName(adapter))
	script := fmt.Sprintf(
		"$ErrorActionPreference='Stop';"+
			"Remove-NetFirewallRule -Name '%[1]s' -ErrorAction SilentlyContinue;"+
			"New-NetFirewallRule -Name '%[1]s' -DisplayName 'LanBaz virtual LAN (%[2]s)' "+
			"-Group '%[3]s' -Direction Inbound -Action Allow -Profile Any "+
			"-InterfaceAlias '%[2]s' -RemoteAddress '%[4]s' -ErrorAction Stop | Out-Null",
		rule, powershellEscape(adapter), firewallGroup, firewallRemote)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := runPowerShell(ctx, script)
	if err != nil {
		log.Warn("could not add the LanBaz firewall rule",
			"adapter", adapter, "error", err, "output", out)
		if out != "" {
			return fmt.Errorf("%v: %s", err, firstLine(out))
		}
		return err
	}
	log.Info("firewall rule added", "adapter", adapter, "rule", firewallRuleName(adapter))
	return nil
}

// removeFirewall deletes the adapter's rule. Failure is logged and ignored: a
// leftover rule only allows traffic on an interface that no longer exists.
func removeFirewall(ctx context.Context, adapter string, log *slog.Logger) {
	script := fmt.Sprintf("Remove-NetFirewallRule -Name '%s' -ErrorAction SilentlyContinue",
		powershellEscape(firewallRuleName(adapter)))
	if out, err := runPowerShell(ctx, script); err != nil {
		log.Debug("could not remove the LanBaz firewall rule", "adapter", adapter, "error", err, "output", out)
	}
}

// markPrivate moves the adapter's network profile to Private.
//
// Windows only creates a connection profile once the interface's media is up,
// which for Wintun is shortly after the packet session starts, and it does so
// asynchronously. So this retries for a while rather than failing on the first
// "no profile yet".
func (a *Adapter) markPrivate(adapter string) {
	name := powershellEscape(adapter)
	script := fmt.Sprintf(
		"$p=Get-NetConnectionProfile -InterfaceAlias '%[1]s' -ErrorAction SilentlyContinue;"+
			"if ($p) { if ($p.NetworkCategory -ne 'Private') { "+
			"Set-NetConnectionProfile -InterfaceAlias '%[1]s' -NetworkCategory Private -ErrorAction Stop }; 'ok' }",
		name)
	for attempt := 0; attempt < 30; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := runPowerShell(ctx, script)
		cancel()
		if err == nil && strings.Contains(out, "ok") {
			a.log.Info("adapter network profile set to Private", "adapter", adapter)
			return
		}
		a.mu.Lock()
		gone := !a.created
		a.mu.Unlock()
		if gone {
			return
		}
		time.Sleep(time.Second)
	}
	a.log.Warn("could not set the adapter's network profile to Private", "adapter", adapter)
}

// firstLine trims PowerShell's multi-line error output to its message.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
