//go:build windows

package wintun

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Recovery from adapters a previous run left behind.
//
// Wintun creates a software device SWD\WINTUN\{GUID}. LanBaz derives the GUID
// from the adapter name (deriveGUID), so a crash, a killed daemon or a room that
// closed half way through setup leaves a device whose GUID the next run of the
// same room asks for again - and WintunCreateAdapter then fails with "Cannot
// create a file when that file already exists". These helpers remove such
// devices; they never touch an adapter that is not LanBaz's own.

// guidString renders a GUID the way Windows prints it in device instance ids.
func guidString(g guid) string {
	return fmt.Sprintf("{%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		g.Data1, g.Data2, g.Data3,
		g.Data4[0], g.Data4[1], g.Data4[2], g.Data4[3], g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// deviceID is the PnP instance id Wintun gives an adapter with this GUID.
func deviceID(g guid) string { return `SWD\WINTUN\` + guidString(g) }

// removeDevice deletes one Wintun device by instance id.
func removeDevice(ctx context.Context, instanceID string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	script := fmt.Sprintf("$ErrorActionPreference='Stop';"+
		"$d=Get-PnpDevice -InstanceId '%[1]s' -ErrorAction SilentlyContinue;"+
		"if ($d) { pnputil.exe /remove-device '%[1]s' | Out-Null; if ($LASTEXITCODE -ne 0) { throw \"pnputil exit $LASTEXITCODE\" } }",
		powershellEscape(instanceID))
	out, err := runPowerShell(ctx, script)
	if err != nil {
		if out != "" {
			return fmt.Errorf("%v: %s", err, firstLine(out))
		}
		return err
	}
	return nil
}

// staleListScript prints "name|instanceId" for every LanBaz Wintun adapter.
// Both the network view (by connection name) and the device view (by device
// description, which also lists devices that are no longer present) are
// consulted. LanBaz-L2 is the TAP adapter and is never listed.
const staleListScript = `$ErrorActionPreference='SilentlyContinue';` +
	`$seen=@{};` +
	`Get-NetAdapter -IncludeHidden | Where-Object { $_.Name -like 'LanBaz-*' -and $_.Name -ne 'LanBaz-L2' -and $_.PnPDeviceID -like 'SWD\WINTUN\*' } | ForEach-Object { $seen[$_.PnPDeviceID]=1; "$($_.Name)|$($_.PnPDeviceID)" };` +
	`Get-PnpDevice -Class Net | Where-Object { $_.InstanceId -like 'SWD\WINTUN\*' -and $_.FriendlyName -like 'LanBaz*' -and -not $seen[$_.InstanceId] } | ForEach-Object { "|$($_.InstanceId)" }`

// CleanupStale removes LanBaz Wintun adapters and firewall rules left by an
// earlier run. The daemon calls it once at start, before any room exists, so
// nothing it removes can be in use. It returns the instance ids it removed.
func CleanupStale(ctx context.Context, log *slog.Logger) []string {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := runPowerShell(ctx, staleListScript)
	if err != nil && out == "" {
		log.Debug("could not list leftover LanBaz adapters", "error", err)
		return nil
	}
	var removed []string
	for _, line := range strings.Split(out, "\n") {
		name, id, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || !strings.HasPrefix(strings.ToUpper(id), `SWD\WINTUN\`) {
			continue
		}
		if err := removeDevice(ctx, id); err != nil {
			log.Warn("could not remove a leftover LanBaz adapter", "adapter", name, "device", id, "error", err)
			continue
		}
		if name != "" {
			removeFirewall(ctx, name, log)
		}
		log.Info("removed a leftover LanBaz adapter", "adapter", name, "device", id)
		removed = append(removed, id)
	}
	return removed
}
