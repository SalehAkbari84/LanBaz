//go:build windows

// Package tap implements network.Adapter on the TAP-Windows6 driver (tap0901),
// which exchanges whole Ethernet frames. It backs Classic LAN (L2) rooms; the
// normal L3 rooms use Wintun.
//
// The driver is installed once (by the LanBaz installer, or by OpenVPN's
// TAP-Windows package) and provides a persistent adapter. LanBaz uses the
// adapter named "LanBaz-L2" when there is one, otherwise the first free TAP
// adapter, which it renames.
package tap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// AdapterName is the name LanBaz gives its TAP adapter.
const AdapterName = "LanBaz-L2"

const (
	adapterClassKey = `SYSTEM\CurrentControlSet\Control\Class\{4D36E972-E325-11CE-BFC1-08002BE10318}`
	networkKey      = `SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}`
	// TAP_WIN_IOCTL_SET_MEDIA_STATUS = CTL_CODE(FILE_DEVICE_UNKNOWN, 6, METHOD_BUFFERED, FILE_ANY_ACCESS)
	ioctlSetMediaStatus = 0x00220018
	frameBuf            = 2048
	waitTimeout         = 250 * time.Millisecond
)

// ErrNoDriver means no TAP-Windows adapter exists on this machine.
var ErrNoDriver = errors.New("the TAP-Windows driver is not installed")

// Options configures New.
type Options struct {
	Logger     *slog.Logger
	NoPriority bool
}

// Adapter is a TAP-Windows interface.
type Adapter struct {
	log        *slog.Logger
	noPriority bool

	mu      sync.Mutex
	guid    string
	alias   string
	handle  windows.Handle
	created bool
	addr    network.AddressInfo
	mtu     int
	note    string

	readEv, writeEv windows.Handle
	writeMu         sync.Mutex
}

// Available reports whether a TAP adapter exists.
func Available() error {
	_, err := findAdapters()
	return err
}

// New prepares a TAP adapter; the device itself is picked and opened in Create.
func New(opts Options) (*Adapter, error) {
	cands, err := findAdapters()
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{log: log, guid: cands[0].guid, alias: cands[0].alias, noPriority: opts.NoPriority, mtu: 1186}, nil
}

// candidate is one TAP-Windows adapter this machine has.
type candidate struct {
	guid, alias, desc string
}

// findAdapters lists the TAP adapters LanBaz may use, best first.
//
// Only plain "TAP-Windows Adapter V9" devices qualify. VPN products ship the
// same driver under their own name (e.g. "HotspotShield TAP-Windows Adapter
// V9"); taking one of those breaks that product, and it is usually held open
// anyway. An adapter already named LanBaz-L2 comes first.
func findAdapters() ([]candidate, error) {
	all := allTap()
	if all == nil {
		return nil, ErrNoDriver
	}
	return orderCandidates(all)
}

// allTap lists every tap0901 adapter in the registry, usable or not.
func allTap() []candidate {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, adapterClassKey, registry.ENUMERATE_SUB_KEYS|registry.READ)
	if err != nil {
		return nil
	}
	defer k.Close()
	subs, _ := k.ReadSubKeyNames(-1)
	var all []candidate
	for _, s := range subs {
		sk, err := registry.OpenKey(k, s, registry.READ)
		if err != nil {
			continue
		}
		comp, _, _ := sk.GetStringValue("ComponentId")
		id, _, _ := sk.GetStringValue("NetCfgInstanceId")
		desc, _, _ := sk.GetStringValue("DriverDesc")
		sk.Close()
		comp = strings.ToLower(comp)
		if (comp == "tap0901" || comp == `root\tap0901`) && id != "" {
			all = append(all, candidate{guid: id, alias: connectionName(id), desc: desc})
		}
	}
	return all
}

// orderCandidates applies the selection rules of findAdapters.
func orderCandidates(all []candidate) ([]candidate, error) {
	var mine, generic []candidate
	for _, c := range all {
		switch {
		case strings.EqualFold(c.alias, AdapterName):
			mine = append(mine, c)
		case c.desc == "" || strings.HasPrefix(strings.ToLower(c.desc), "tap-windows adapter v9"):
			generic = append(generic, c)
		}
	}
	out := append(mine, generic...)
	if len(out) == 0 {
		if len(all) > 0 {
			return nil, fmt.Errorf("%w: the only TAP adapters here belong to other VPN programs", ErrNoDriver)
		}
		return nil, ErrNoDriver
	}
	return out, nil
}

// defaultAlias reports whether a connection still has the name Windows gave it,
// so renaming it to LanBaz-L2 takes nothing away from another program.
func defaultAlias(alias string) bool {
	a := strings.ToLower(alias)
	for _, p := range []string{"ethernet", "local area connection", "network", "اترنت"} {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return alias == ""
}

func connectionName(guid string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, networkKey+`\`+guid+`\Connection`, registry.READ)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("Name")
	return v
}

// Name implements network.Adapter.
func (a *Adapter) Name() string { return "tap" }

// Note reports a partial configuration.
func (a *Adapter) Note() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.note
}

// Create opens the TAP device and connects its virtual cable.
//
// Every candidate adapter is tried in turn. A device Windows reports as "not
// functioning" (ERROR_GEN_FAILURE) or missing is disabled and re-enabled once,
// which is what clears a TAP adapter left wedged by a crashed program.
func (a *Adapter) Create(ctx context.Context, _ string, mtu int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.created {
		return network.ErrAdapterExists
	}
	if mtu > 0 {
		a.mtu = mtu
	}
	cands, err := findAdapters()
	if err != nil {
		return protocol.NewErrorf(protocol.CodeWintunCreateFailed, "tap: %v", err)
	}
	var (
		h        windows.Handle
		picked   candidate
		inUse    bool
		lastErr  error
		openedOK bool
	)
	for _, c := range cands {
		h, err = openTap(ctx, c, a.log)
		if err == nil {
			picked, openedOK = c, true
			break
		}
		lastErr = err
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_BUSY) {
			inUse = true
		}
		a.log.Info("TAP adapter not usable; trying the next one", "adapter", c.alias, "description", c.desc, "error", err)
	}
	if !openedOK {
		if inUse {
			return protocol.NewErrorf(protocol.CodeWintunCreateFailed,
				"tap: the classic LAN adapter is in use by another program (a VPN, or another classic LAN room); close it and try again (%v)", lastErr)
		}
		return protocol.NewErrorf(protocol.CodeWintunCreateFailed,
			"tap: the classic LAN adapter is not working even after restarting it; reinstall the TAP driver from Settings → Network or restart Windows (%v)", lastErr)
	}
	var on uint32 = 1
	var ret uint32
	if err := windows.DeviceIoControl(h, ioctlSetMediaStatus, (*byte)(unsafe.Pointer(&on)), 4,
		(*byte)(unsafe.Pointer(&on)), 4, &ret, nil); err != nil {
		windows.CloseHandle(h)
		return protocol.NewErrorf(protocol.CodeWintunCreateFailed, "tap: could not connect the adapter: %v", err)
	}
	a.guid, a.alias = picked.guid, picked.alias
	// Give the adapter a recognisable name, unless another program named it.
	if !strings.EqualFold(a.alias, AdapterName) && defaultAlias(a.alias) {
		script := fmt.Sprintf("Rename-NetAdapter -Name '%s' -NewName '%s' -ErrorAction Stop", psq(a.alias), AdapterName)
		if _, err := ps(ctx, script); err == nil {
			a.alias = AdapterName
		}
	}
	a.readEv, _ = windows.CreateEvent(nil, 1, 0, nil)
	a.writeEv, _ = windows.CreateEvent(nil, 1, 0, nil)
	a.handle = h
	a.created = true
	a.log.Info("classic LAN adapter opened", "adapter", a.alias)
	return nil
}

// openTap opens one adapter's device file, restarting the adapter once when
// Windows says the device is not functioning.
func openTap(ctx context.Context, c candidate, log *slog.Logger) (windows.Handle, error) {
	path, _ := windows.UTF16PtrFromString(`\\.\Global\` + c.guid + `.tap`)
	open := func() (windows.Handle, error) {
		return windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_SYSTEM|windows.FILE_FLAG_OVERLAPPED, 0)
	}
	h, err := open()
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, windows.ERROR_GEN_FAILURE) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) &&
		!errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return 0, err
	}
	if c.alias == "" {
		return 0, err
	}
	log.Info("restarting the TAP adapter", "adapter", c.alias, "error", err)
	script := fmt.Sprintf("$ErrorActionPreference='Stop';"+
		"Disable-NetAdapter -Name '%[1]s' -Confirm:$false;Start-Sleep -Milliseconds 800;"+
		"Enable-NetAdapter -Name '%[1]s' -Confirm:$false", psq(c.alias))
	if out, perr := ps(ctx, script); perr != nil {
		log.Info("could not restart the TAP adapter", "adapter", c.alias, "error", perr, "output", out)
		return 0, err
	}
	for i := 0; i < 3; i++ {
		time.Sleep(time.Duration(i+1) * 700 * time.Millisecond)
		if h, err = open(); err == nil {
			return h, nil
		}
	}
	return 0, err
}

// Configure sets the address, MTU, priority, profile and firewall rule.
func (a *Adapter) Configure(ctx context.Context, addr network.AddressInfo) error {
	a.mu.Lock()
	alias, mtu, noPriority := a.alias, a.mtu, a.noPriority
	a.mu.Unlock()
	host, bits := addr.IPv4.Addr(), addr.IPv4.Bits()
	if !host.Is4() {
		return protocol.NewError(protocol.CodeConfigInvalid, "tap: no IPv4 address to configure")
	}
	script := fmt.Sprintf(
		"$ErrorActionPreference='Stop';"+
			"$i=Get-NetAdapter -Name '%[1]s';"+
			"Remove-NetIPAddress -InterfaceIndex $i.ifIndex -AddressFamily IPv4 -Confirm:$false -ErrorAction SilentlyContinue;"+
			"Set-NetIPInterface -InterfaceIndex $i.ifIndex -Dhcp Disabled -ErrorAction SilentlyContinue;"+
			"New-NetIPAddress -InterfaceIndex $i.ifIndex -IPAddress '%[2]s' -PrefixLength %[3]d | Out-Null",
		psq(alias), host, bits)
	if out, err := ps(ctx, script); err != nil {
		return protocol.NewErrorf(protocol.CodeWintunCreateFailed, "tap: could not configure %s: %v (%s)", host, err, out)
	}
	var notes []string
	metric := "-AutomaticMetric Disabled -InterfaceMetric 1 "
	if noPriority {
		metric = ""
	}
	if out, err := ps(ctx, fmt.Sprintf("$i=Get-NetAdapter -Name '%s';Set-NetIPInterface -InterfaceIndex $i.ifIndex -AddressFamily IPv4 -NlMtuBytes %d %s-ErrorAction Stop",
		psq(alias), mtu, metric)); err != nil {
		a.log.Warn("could not set the classic LAN adapter MTU/metric", "error", err, "output", out)
		notes = append(notes, "could not set the adapter MTU/priority")
	}
	if !noPriority {
		go network.PreferForDiscovery(context.Background(), alias, ps, a.log)
	}
	rule := "LanBaz-In-" + alias
	if out, err := ps(ctx, fmt.Sprintf("Remove-NetFirewallRule -Name '%[1]s' -ErrorAction SilentlyContinue;"+
		"New-NetFirewallRule -Name '%[1]s' -DisplayName 'LanBaz classic LAN' -Group 'LanBaz' -Direction Inbound -Action Allow -Profile Any "+
		"-InterfaceAlias '%[2]s' -ErrorAction Stop | Out-Null", psq(rule), psq(alias))); err != nil {
		a.log.Warn("could not add the classic LAN firewall rule", "error", err, "output", out)
		notes = append(notes, "Windows Firewall rule could not be added")
	}
	go func() {
		for i := 0; i < 20; i++ {
			out, _ := ps(context.Background(), fmt.Sprintf("$p=Get-NetConnectionProfile -InterfaceAlias '%[1]s' -ErrorAction SilentlyContinue;"+
				"if ($p) { if ($p.NetworkCategory -ne 'Private') { Set-NetConnectionProfile -InterfaceAlias '%[1]s' -NetworkCategory Private }; 'ok' }", psq(alias)))
			if strings.Contains(out, "ok") {
				return
			}
			time.Sleep(time.Second)
		}
	}()
	a.mu.Lock()
	a.addr = addr
	a.addr.Interface = alias
	a.note = strings.Join(notes, "; ")
	a.mu.Unlock()
	return nil
}

// ReadPacket implements network.Adapter: one Ethernet frame.
func (a *Adapter) ReadPacket(ctx context.Context) (network.Packet, error) {
	a.mu.Lock()
	h, ev, ok := a.handle, a.readEv, a.created
	a.mu.Unlock()
	if !ok {
		return network.Packet{}, network.ErrClosed
	}
	buf := make([]byte, frameBuf)
	var ov windows.Overlapped
	ov.HEvent = ev
	_ = windows.ResetEvent(ev)
	var n uint32
	err := windows.ReadFile(h, buf, &n, &ov)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return network.Packet{}, err
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		for {
			r, _ := windows.WaitForSingleObject(ev, uint32(waitTimeout.Milliseconds()))
			if r == windows.WAIT_OBJECT_0 {
				break
			}
			if ctx.Err() != nil {
				// Wait for the cancellation to complete: the kernel owns buf
				// and ov until it does.
				_ = windows.CancelIoEx(h, &ov)
				_ = windows.GetOverlappedResult(h, &ov, &n, true)
				return network.Packet{}, network.ErrClosed
			}
		}
		if err := windows.GetOverlappedResult(h, &ov, &n, false); err != nil {
			return network.Packet{}, err
		}
	}
	return network.Packet{Payload: buf[:n], ReceivedAt: time.Now()}, nil
}

// WritePacket implements network.Adapter: one Ethernet frame.
func (a *Adapter) WritePacket(_ context.Context, pkt network.Packet) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.mu.Lock()
	h, ev, ok := a.handle, a.writeEv, a.created
	a.mu.Unlock()
	if !ok {
		return network.ErrClosed
	}
	var ov windows.Overlapped
	ov.HEvent = ev
	_ = windows.ResetEvent(ev)
	var n uint32
	err := windows.WriteFile(h, pkt.Payload, &n, &ov)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return err
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		if r, _ := windows.WaitForSingleObject(ev, 1000); r != windows.WAIT_OBJECT_0 {
			_ = windows.CancelIoEx(h, &ov)
			_ = windows.GetOverlappedResult(h, &ov, &n, true)
			return errors.New("tap: write timed out")
		}
		return windows.GetOverlappedResult(h, &ov, &n, false)
	}
	return nil
}

// Address implements network.Adapter.
func (a *Adapter) Address() (network.AddressInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.created {
		return network.AddressInfo{}, network.ErrNotStarted
	}
	return a.addr, nil
}

// Routes implements network.Adapter.
func (a *Adapter) Routes() ([]network.Route, error) { return nil, nil }

// Destroy disconnects the cable, removes the address and the firewall rule.
// The adapter itself stays installed for the next classic LAN room.
func (a *Adapter) Destroy(ctx context.Context) error {
	a.mu.Lock()
	h, alias, ok := a.handle, a.alias, a.created
	a.created = false
	a.mu.Unlock()
	if !ok {
		return nil
	}
	var off, ret uint32
	_ = windows.DeviceIoControl(h, ioctlSetMediaStatus, (*byte)(unsafe.Pointer(&off)), 4, (*byte)(unsafe.Pointer(&off)), 4, &ret, nil)
	_ = windows.CloseHandle(h)
	_ = windows.CloseHandle(a.readEv)
	_ = windows.CloseHandle(a.writeEv)
	_, _ = ps(ctx, fmt.Sprintf("$ErrorActionPreference='SilentlyContinue';$i=Get-NetAdapter -Name '%[1]s';"+
		"if ($i) { Remove-NetIPAddress -InterfaceIndex $i.ifIndex -AddressFamily IPv4 -Confirm:$false };"+
		"Remove-NetFirewallRule -Name 'LanBaz-In-%[1]s'", psq(alias)))
	a.log.Info("classic LAN adapter released", "adapter", alias)
	return nil
}

func ps(ctx context.Context, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// psq escapes s for a single-quoted PowerShell string. PowerShell treats
// the typographic quotes U+2018..U+201B as single quotes too, so each of them
// must be doubled as well, or a name containing one ends the string early.
func psq(s string) string {
	return strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’",
		"‚", "‚‚", "‛", "‛‛").Replace(s)
}

var _ network.Adapter = (*Adapter)(nil)

// Alias is the Windows connection name of the adapter in use.
func (a *Adapter) Alias() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.alias
}
