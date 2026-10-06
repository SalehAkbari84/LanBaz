package daemon

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// One-click diagnostics: everything needed to understand a friend's problem,
// in one zip they can send. The developer log is already token-masked; the
// settings lose the relay key and TURN credentials here; the Windows part is
// read-only queries of adapters, routes and the LanBaz firewall rules.

const redacted = "<redacted>"

func (d *Daemon) diagBundle(ctx context.Context) (string, error) {
	dir := d.diagDir
	if dir == "" {
		return "", protocol.NewError(protocol.CodeInternal, "diag: no Downloads folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "LanBaz-diagnostics-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)
	add := func(name string, body []byte) {
		if w, err := zw.Create(name); err == nil {
			_, _ = w.Write(body)
		}
	}

	if d.ring != nil {
		var b strings.Builder
		for _, e := range d.ring.Since(0) {
			fmt.Fprintf(&b, "%s %-5s %s %s\n", e.Time.Format(time.RFC3339Nano), e.Level, e.Msg, e.Attrs)
		}
		add("log.txt", []byte(b.String()))
	}

	dctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	report := d.diagnose(dctx)
	cancel()
	add("diagnose.json", readableJSON(report))
	add("status.json", readableJSON(d.diagStatus()))
	add("windows.txt", []byte(windowsNetworkReport(ctx)))

	if err := zw.Close(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	d.log.Info("diagnostics file created", "path", path)
	return path, nil
}

// readableJSON is indented JSON without HTML escaping, for people to read.
func readableJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return b.Bytes()
}

type diagStatus struct {
	Version   string                  `json:"version"`
	Commit    string                  `json:"commit"`
	OS        string                  `json:"os"`
	Time      time.Time               `json:"time"`
	Rooms     []protocol.RoomSummary  `json:"rooms"`
	Kept      *protocol.KeptNetworks  `json:"kept,omitempty"`
	Settings  protocol.Settings       `json:"settings"`
	Transfers []protocol.FileTransfer `json:"transfers,omitempty"`
}

func (d *Daemon) diagStatus() diagStatus {
	s := diagStatus{Version: d.build.Version, Commit: d.build.Commit, OS: runtime.GOOS + "/" + runtime.GOARCH, Time: time.Now().UTC()}
	if rooms := d.Rooms(); rooms != nil {
		s.Rooms = rooms.List()
	}
	if d.friends != nil {
		k := d.friends.kept()
		s.Kept = &k
	}
	if d.settings != nil {
		s.Settings = redactSettings(d.settings.Get())
	}
	if d.files != nil {
		for _, t := range d.files.list() {
			t.Path = filepath.Base(t.Path)
			s.Transfers = append(s.Transfers, t)
		}
	}
	return s
}

// redactSettings drops every secret a settings value can hold.
func redactSettings(st protocol.Settings) protocol.Settings {
	if st.Metered != nil {
		m := *st.Metered
		if m.Key != "" {
			m.Key = redacted
		}
		st.Metered = &m
	}
	turn := make([]protocol.RelayServer, len(st.TURNServers))
	for i, r := range st.TURNServers {
		if r.Credential != "" {
			r.Credential = redacted
		}
		turn[i] = r
	}
	st.TURNServers = turn
	return st
}

// windowsNetworkReport runs read-only PowerShell queries; elsewhere it says so.
func windowsNetworkReport(ctx context.Context) string {
	if runtime.GOOS != "windows" {
		return "not Windows\n"
	}
	queries := []struct{ title, cmd string }{
		{"Adapters", "Get-NetAdapter | Sort-Object ifIndex | Format-Table ifIndex,Name,InterfaceDescription,Status,LinkSpeed -AutoSize"},
		{"IPv4 interfaces (metric decides where LAN broadcasts go)", "Get-NetIPInterface -AddressFamily IPv4 | Sort-Object InterfaceMetric | Format-Table ifIndex,InterfaceAlias,InterfaceMetric,ConnectionState,NlMtu -AutoSize"},
		{"IPv4 addresses", "Get-NetIPAddress -AddressFamily IPv4 | Format-Table InterfaceAlias,IPAddress,PrefixLength -AutoSize"},
		{"IPv4 routes", "Get-NetRoute -AddressFamily IPv4 | Sort-Object RouteMetric | Format-Table DestinationPrefix,NextHop,InterfaceAlias,RouteMetric -AutoSize"},
		{"Broadcast route winner", "Find-NetRoute -RemoteIPAddress 255.255.255.255 | Format-List InterfaceAlias,InterfaceIndex"},
		{"LanBaz firewall rules", "Get-NetFirewallRule -Group LanBaz -ErrorAction SilentlyContinue | Format-Table Name,DisplayName,Enabled,Direction,Action -AutoSize; Get-NetFirewallRule -Name 'LanBaz-*' -ErrorAction SilentlyContinue | Format-Table Name,Enabled,Direction,Action -AutoSize"},
	}
	var b strings.Builder
	for _, q := range queries {
		qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		out, err := exec.CommandContext(qctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			q.cmd+" | Out-String -Width 220").CombinedOutput()
		cancel()
		fmt.Fprintf(&b, "==== %s ====\n%s\n", q.title, strings.TrimSpace(string(out)))
		if err != nil {
			fmt.Fprintf(&b, "(error: %v)\n", err)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (d *Daemon) registerDiag() error {
	return d.api.Register(protocol.MethodDiagBundle, func(ctx context.Context, _ json.RawMessage) (any, error) {
		p, err := d.diagBundle(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": p}, nil
	})
}
