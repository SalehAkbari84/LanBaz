//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/lanbaz/lanbaz/core/internal/network/wintun"
)

// ps runs a PowerShell script and returns its trimmed output.
func ps(script string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command",
		"[Console]::OutputEncoding=[Text.Encoding]::UTF8;"+script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func isAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// layerSystem checks what LanBaz needs from this PC. It returns whether the
// tool runs as administrator.
func layerSystem(r *report) bool {
	r.section("1. This PC")
	admin := isAdmin()
	if admin {
		r.ok("running as administrator")
	} else {
		r.fail("NOT running as administrator: the adapter tests are skipped (right-click → Run as administrator)")
	}

	if out, err := ps(`$o=Get-CimInstance Win32_OperatingSystem; "$($o.Caption) $($o.Version) build $($o.BuildNumber)"`, 0); err == nil {
		r.info("Windows: %s", out)
	}

	// The installed LanBaz and whether it is running.
	inst := filepath.Join(os.Getenv("ProgramFiles"), "LanBaz", "lanbazd.exe")
	if _, err := os.Stat(inst); err == nil {
		if v, err := exec.Command(inst, "--version").Output(); err == nil {
			r.info("installed engine: %s", strings.TrimSpace(string(v)))
		}
	} else {
		r.warn("LanBaz is not installed in %s", filepath.Dir(inst))
	}
	if out, _ := ps(`Get-Process lanbaz,lanbazd,lanbazd-x86_64-pc-windows-msvc -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Name) pid $($_.Id)" }`, 0); out != "" {
		r.warn("LanBaz is running; close it (tray → Quit) for the cleanest results:")
		r.block(out)
	} else {
		r.ok("LanBaz is not running (clean test)")
	}

	if err := wintun.Available(); err != nil {
		r.fail("Wintun driver: %v", err)
	} else {
		r.ok("Wintun driver (wintun.dll) loads")
	}

	// Every network adapter, so other VPNs and leftovers are visible.
	out, _ := ps(`Get-NetAdapter -IncludeHidden | Sort-Object Status,Name | Format-Table -AutoSize Name,InterfaceDescription,Status,ifIndex,MtuSize | Out-String -Width 200`, 0)
	r.info("network adapters:")
	r.block(out)
	lower := strings.ToLower(out)
	for _, vpn := range []string{"hotspot", "radmin", "hamachi", "zerotier", "tailscale", "wireguard", "openvpn", "nordlynx", "proton", "softether", "targame", "cloudflare warp", "fortinet", "cisco anyconnect", "psiphon"} {
		if strings.Contains(lower, vpn) {
			r.warn("another VPN/virtual network is installed (%q); if it is active it can capture or reroute LanBaz traffic", vpn)
		}
	}

	// LanBaz leftovers from earlier runs.
	left, _ := ps(`Get-NetAdapter -IncludeHidden | Where-Object { $_.Name -like 'LanBaz-*' -and $_.Name -ne 'LanBaz-L2' } | ForEach-Object { "$($_.Name)  $($_.Status)  $($_.PnPDeviceID)" }`, 0)
	if left != "" {
		r.warn("leftover LanBaz adapters (LanBaz removes them at its next start):")
		r.block(left)
	} else {
		r.ok("no leftover LanBaz room adapters")
	}
	rules, _ := ps(`Get-NetFirewallRule -Group 'LanBaz' -ErrorAction SilentlyContinue | ForEach-Object { "$($_.Name)  enabled=$($_.Enabled)" }`, 0)
	if rules != "" {
		r.info("LanBaz firewall rules present:")
		r.block(rules)
	}

	// Firewall and security products: a third-party firewall ignores Windows
	// Firewall rules and is the usual reason inbound game traffic vanishes.
	fw, _ := ps(`Get-NetFirewallProfile | ForEach-Object { "$($_.Name): enabled=$($_.Enabled) inbound=$($_.DefaultInboundAction)" }`, 0)
	r.info("Windows Firewall profiles:")
	r.block(fw)
	sec, _ := ps(`$f=Get-CimInstance -Namespace root/SecurityCenter2 -ClassName FirewallProduct -ErrorAction SilentlyContinue | ForEach-Object { "firewall: $($_.displayName)" }; $a=Get-CimInstance -Namespace root/SecurityCenter2 -ClassName AntiVirusProduct -ErrorAction SilentlyContinue | ForEach-Object { "antivirus: $($_.displayName)" }; $f; $a`, 0)
	if sec != "" {
		r.info("security products registered with Windows:")
		r.block(sec)
		if strings.Contains(strings.ToLower(sec), "firewall:") {
			r.warn("a third-party firewall is installed; it must allow LanBaz (lanbazd.exe) and the 10.200.0.0/16 network itself")
		}
	}

	// How this PC reaches the internet.
	route, _ := ps(`Get-NetRoute -DestinationPrefix 0.0.0.0/0 -ErrorAction SilentlyContinue | Sort-Object { $_.RouteMetric + $_.InterfaceMetric } | Format-Table -AutoSize InterfaceAlias,NextHop,RouteMetric,InterfaceMetric | Out-String -Width 160`, 0)
	r.info("default routes (first one is used):")
	r.block(route)
	prof, _ := ps(`Get-NetConnectionProfile | ForEach-Object { "$($_.InterfaceAlias): $($_.NetworkCategory) ($($_.IPv4Connectivity))" }`, 0)
	r.info("network profiles:")
	r.block(prof)
	return admin
}
