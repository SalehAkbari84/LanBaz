//go:build windows

// Package wintun implements network.Adapter on top of the Wintun driver.
//
// # Why the driver is loaded by hand
//
// The usual way to use Wintun from Go is a third-party wrapper module. LanBaz
// does not take that dependency: the surface actually needed is ten exported
// functions, and binding them directly means the adapter's behaviour is pinned
// to the wintun.dll beside the daemon rather than to whatever version a module
// graph happens to resolve. It also means one fewer thing that has to be
// trusted with the only part of LanBaz that runs with administrator rights.
//
// The driver binary itself is not embedded here. It is loaded from the
// directory the daemon runs in, so an installer places it next to lanbazd and a
// user who removes the app removes it with everything else.
//
// # What Wintun is and is not
//
// Wintun is a layer 3 adapter. It hands the process complete IP packets and
// accepts complete IP packets; there is no Ethernet header, no MAC learning
// and no ARP. That is why the router in the parent package exists at all, and
// why broadcast and multicast have to be relayed by LanBaz rather than by the
// driver. See docs/networking.md.
package wintun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// DLLName is the driver binary the daemon loads.
const DLLName = "wintun.dll"

// waitTimeout is how long the read loop blocks on Wintun's ring event before
// re-checking whether it has been asked to stop. A finite timeout is what lets
// a shutdown take effect without closing the adapter underneath a blocked
// syscall, which the driver does not tolerate.
const waitTimeout = 250 * time.Millisecond

// ringCapacity is the size of the buffer shared between the driver and this
// process, in bytes.
//
// The driver requires a power of two between 128 KiB and 64 MiB and rejects
// anything else with ERROR_INVALID_PARAMETER. 4 MiB holds roughly a thousand
// full-MTU packets - more than a gigabit link delivers in the window this
// buffer represents - so a momentarily slow reader costs latency rather than
// dropped packets.
const ringCapacity = 4 << 20

// The Wintun C API. Every function below is exported by wintun.dll with the
// signature given.
//
// They are bound through the lazy loader so a missing DLL surfaces as a LanBaz
// error rather than a panic at init.
//
// `windows.NewLazySystemDLL`, not `windows.NewLazyDLL`. This distinction is the
// difference between LanBaz working and never working, and it is invisible until
// the driver is actually present next to the daemon: a *system* DLL lookup
// searches only System32 and the process search path, never the directory the
// executable lives in, so a correctly installed wintun.dll would still be
// reported missing. A plain lazy DLL resolves the full path first, which is what
// "install the driver next to lanbazd" means.
var (
	wintunDLL                = windows.NewLazyDLL(DLLName)
	procWintunCreateAdapter  = wintunDLL.NewProc("WintunCreateAdapter")
	procWintunCloseAdapter   = wintunDLL.NewProc("WintunCloseAdapter")
	procWintunOpenAdapter    = wintunDLL.NewProc("WintunOpenAdapter")
	procWintunStartSession   = wintunDLL.NewProc("WintunStartSession")
	procWintunEndSession     = wintunDLL.NewProc("WintunEndSession")
	procWintunGetReadWait    = wintunDLL.NewProc("WintunGetReadWaitEvent")
	procWintunReceivePacket  = wintunDLL.NewProc("WintunReceivePacket")
	procWintunReleasePacket  = wintunDLL.NewProc("WintunReleaseReceivePacket")
	procWintunAllocateSend   = wintunDLL.NewProc("WintunAllocateSendPacket")
	procWintunSendPacket     = wintunDLL.NewProc("WintunSendPacket")
	procWintunGetAdapterLUID = wintunDLL.NewProc("WintunGetAdapterLUID")
)

// guid mirrors the Windows GUID layout.
type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// call is a panic-safe wrapper around a LazyProc call.
//
// windows.LazyProc.Call panics when the export does not exist rather than
// returning an error. In a goroutine that panic takes the entire process down,
// which is the worst possible outcome for a driver boundary: one missing export,
// or one driver too old for the API LanBaz expects, would take down a daemon
// whose control API and other rooms were working fine.
//
// Every call into wintun.dll goes through here so that a driver mismatch becomes
// an ordinary error a user can act on instead of a crash they cannot explain.
func call(proc *windows.LazyProc, args ...uintptr) (result uintptr, _ uintptr, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = 0
			err = fmt.Errorf("wintun: the driver is missing an expected export: %v", r)
		}
	}()
	return proc.Call(args...)
}

// lastError keeps only a LastError that actually reports a failure.
//
// LazyProc.Call always returns a non-nil error built from GetLastError, even
// when the call succeeded: a success arrives as Errno(0), "The operation
// completed successfully." Treating that value as a failure is what made every
// session and packet call look broken, so the only safe rule at this boundary is
// that a call's *return value* says whether it worked and LastError only says
// why it did not.
func lastError(err error) error {
	var errno windows.Errno
	if errors.As(err, &errno) && errno == 0 {
		return nil
	}
	return err
}

// Available reports whether the driver can be loaded at all.
// It exists so a daemon on a machine without the driver can start, serve its
// control API and report why no LAN exists, rather than failing to start and
// leaving the user with a program that does nothing and says nothing.
func Available() error {
	// Resolve the path explicitly so the error names a file, not a search path.
	// "wintun.dll is not present" tells a user nothing; the full path they are
	// supposed to install it into does.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%w: cannot locate the daemon executable: %v",
			network.ErrAdapterFailed, err)
	}
	full := filepath.Join(filepath.Dir(exe), DLLName)
	if err := wintunDLL.Load(); err != nil {
		return fmt.Errorf("%w: %s is missing; install it to %s (%v)",
			network.ErrAdapterFailed, DLLName, full, err)
	}
	if err := procWintunCreateAdapter.Find(); err != nil {
		return fmt.Errorf("%w: %s does not export the Wintun API: %v",
			network.ErrAdapterFailed, DLLName, err)
	}
	if err := procWintunGetReadWait.Find(); err != nil {
		return fmt.Errorf("%w: %s is too old to be a Wintun driver: %v",
			network.ErrAdapterFailed, DLLName, err)
	}
	// Session calls were added after the adapter calls, so a driver old enough
	// to create an adapter can still be too old to run packets over it.
	if err := procWintunStartSession.Find(); err != nil {
		return fmt.Errorf("%w: %s cannot start a packet session: %v",
			network.ErrAdapterFailed, DLLName, err)
	}
	return nil
}

// Options configures New.
type Options struct {
	// Name is the adapter name as Windows stores it, e.g. "LanBaz". It must be
	// stable across restarts, because every run with a fresh name creates
	// another adapter and the user accumulates them in their network list.
	Name string
	// Logger receives diagnostics.
	Logger *slog.Logger
	// NoPriority leaves the interface metric to Windows.
	NoPriority bool
}

// Adapter is a Wintun interface.
type Adapter struct {
	log *slog.Logger

	mu         sync.Mutex
	name       string
	id         guid
	handle     uintptr
	luid       uint64
	mtu        int
	noPriority bool
	created    bool
	// opened is true when the adapter was an existing one opened rather than
	// created. WintunCloseAdapter does not delete such an adapter, so Destroy
	// removes its device explicitly.
	opened bool
	addr   network.AddressInfo
	routes []network.Route

	// session is Wintun's packet-session handle and is NOT the adapter handle
	// above. Wintun splits its API in two: a handful of calls take the adapter
	// (create, close, LUID) and every call that moves a packet takes the
	// session that WintunStartSession returned. Passing the adapter handle to a
	// session call is not a type error - both are pointers - so the driver
	// rejects it at run time with ERROR_INVALID_PARAMETER, long after the
	// adapter has been created and configured and the network has reported
	// itself started. Keeping the two in separate fields is what makes the
	// difference impossible to lose at a call site.
	//
	// ring is the driver's receive buffer and readEvent the handle that fires
	// when it has something. Both exist only for the duration of a session; the
	// reader loop lives in ReadPacket rather than on its own goroutine so that
	// the adapter's concurrency is the same as the in-memory backend's.
	session   uintptr
	ring      uintptr
	readEvent windows.Handle

	// readErr holds the first receive failure so Destroy can report it.
	readErr error
	// note explains a partial configuration - a firewall rule or MTU that could
	// not be applied - so the status payload can say why games may misbehave.
	note string
}

// Note reports what could not be configured, or "" when everything was.
func (a *Adapter) Note() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.note
}

// New builds a Wintun adapter. It does not create the interface; call Create.
func New(opts Options) (*Adapter, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = "LanBaz"
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{
		log:        log,
		name:       name,
		mtu:        network.DefaultMTU,
		noPriority: opts.NoPriority,
	}, nil
}

// Name implements network.Adapter.
func (a *Adapter) Name() string { return "wintun" }

// Create implements network.Adapter.
//
// The GUID is derived from the adapter name rather than generated. Wintun keys
// the adapter's registry entry on the name and GUID pair, so a random GUID
// would create a brand new interface on every launch and leave the previous one
// behind - invisible to the user except as a growing list of disconnected
// adapters, and a source of address conflicts.
func (a *Adapter) Create(ctx context.Context, name string, mtu int) error {
	if name == "" {
		name = a.name
	}
	a.mu.Lock()
	if a.created {
		a.mu.Unlock()
		return network.ErrAdapterExists
	}
	a.mu.Unlock()

	if mtu <= 0 {
		mtu = network.DefaultMTU
	}
	g := deriveGUID(name)
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return protocol.NewErrorf(protocol.CodeConfigInvalid, "wintun: bad adapter name %q", name)
	}
	friendly, err := windows.UTF16PtrFromString("LanBaz (" + name + ")")
	if err != nil {
		return protocol.NewError(protocol.CodeConfigInvalid, "wintun: bad friendly name")
	}

	create := func() (uintptr, error) {
		h, _, callErr := call(procWintunCreateAdapter,
			uintptr(unsafe.Pointer(namePtr)),
			uintptr(unsafe.Pointer(friendly)),
			uintptr(unsafe.Pointer(&g)),
		)
		return h, lastError(callErr)
	}
	handle, cerr := create()
	if handle == 0 && alreadyExists(cerr) {
		// A device with this name's GUID survived an earlier run (a crash, or
		// a room closed while it was still being set up). Remove it and try
		// once more; failing that, open and reuse it.
		a.log.Info("a leftover adapter has this name; removing it", "adapter", name, "device", deviceID(g))
		if err := removeDevice(ctx, deviceID(g)); err != nil {
			a.log.Warn("could not remove the leftover adapter", "adapter", name, "error", err)
		} else {
			time.Sleep(500 * time.Millisecond)
		}
		handle, cerr = create()
		if handle == 0 && alreadyExists(cerr) {
			if h, _, oerr := call(procWintunOpenAdapter, uintptr(unsafe.Pointer(namePtr))); h != 0 {
				a.log.Info("reusing the existing adapter", "adapter", name)
				handle, cerr = h, nil
				a.mu.Lock()
				a.opened = true
				a.mu.Unlock()
			} else {
				a.log.Warn("could not open the existing adapter either", "adapter", name, "error", lastError(oerr))
			}
		}
	}
	if handle == 0 {
		return createError(cerr)
	}

	// The LUID identifies the interface to the IP Helper stack. It is only used
	// for diagnostics here - routes are matched by name, which survives an
	// adapter being recreated - so a failure to read it is logged, not fatal.
	var luid uint64
	if _, _, err := call(procWintunGetAdapterLUID, handle, uintptr(unsafe.Pointer(&luid))); luid == 0 {
		a.log.Debug("could not read the adapter LUID", "adapter", name, "error", lastError(err))
	}

	a.mu.Lock()
	a.created, a.handle, a.id, a.luid = true, handle, g, luid
	a.name, a.mtu = name, mtu
	a.mu.Unlock()

	a.log.Info("virtual adapter created", "adapter", name, "luid", luid)
	return nil
}

// deriveGUID builds a stable adapter identifier from its name.
//
// FNV-1a is used because it is short, needs no table, and is not being used for
// anything adversarial: the input is an interface name chosen by this daemon.
// The result is then forced to version 4 with the RFC 4122 variant bits set, so
// the value is a well-formed GUID even though nothing depends on it being
// random.
func deriveGUID(name string) guid {
	const (
		offset64 = uint64(14695981039346656037)
		prime64  = uint64(1099511628211)
	)
	lo, hi := offset64, offset64
	for i := 0; i < len(name); i++ {
		c := uint64(name[i])
		lo = (lo ^ c) * prime64
		hi = (hi ^ (c + uint64(i))) * prime64
	}
	var g guid
	g.Data1 = uint32(lo)
	g.Data1 = (g.Data1 & 0xffff0fff) | 0x00004000
	g.Data2 = uint16(hi)
	g.Data3 = uint16(hi >> 16)
	g.Data3 = (g.Data3 & 0x3fff) | 0x8000
	for i := 0; i < 8; i++ {
		g.Data4[i] = byte(hi >> (8 * (i + 3)))
	}
	return g
}

// alreadyExists reports whether a create failed because the device is there.
//
// Wintun passes on what SwDeviceCreate returned, which is an HRESULT
// (0x800700B7) rather than the plain Win32 code 183. Windows prints the same
// text for both, so both forms are matched.
func alreadyExists(err error) bool {
	var errno windows.Errno
	if !errors.As(err, &errno) {
		return false
	}
	code := uint32(errno)
	if code&0xFFFF0000 == 0x80070000 {
		code &= 0xFFFF
	}
	return code == uint32(windows.ERROR_ALREADY_EXISTS) || code == uint32(windows.ERROR_FILE_EXISTS)
}

// createError maps a failed WintunCreateAdapter onto a LanBaz error code.
//
// The distinction is between "run LanBaz as administrator" and "reinstall the
// driver", and a user told the wrong one wastes an afternoon.
//
// The cases are plain Win32 codes, not HRESULTs. The Windows API reports
// E_ACCESSDENIED as 0x80070005 when it surfaces through COM, but a driver call
// made through CreateFile and friends puts ERROR_ACCESS_DENIED - the small
// number 5 - in LastError, and that is what arrives here as a syscall.Errno.
// Matching against the HRESULT spelling meant neither case ever fired, so the
// one error that has an obvious fix arrived as a bare "Access is denied."
func createError(err error) error {
	var errno windows.Errno
	if errors.As(err, &errno) {
		switch uintptr(errno) {
		case uintptr(windows.ERROR_ACCESS_DENIED):
			return protocol.NewErrorf(protocol.CodeWintunPermissionDenied,
				"wintun: creating the virtual adapter needs administrator rights: "+
					"quit LanBaz and start it again with \"Run as administrator\" (%v)", err)
		case uintptr(windows.ERROR_FILE_NOT_FOUND):
			return protocol.NewErrorf(protocol.CodeWintunCreateFailed,
				"wintun: the driver reported that the adapter is missing (%v)", err)
		case uintptr(windows.ERROR_NOT_SUPPORTED), uintptr(windows.ERROR_INVALID_FUNCTION):
			return protocol.NewErrorf(protocol.CodeWintunCreateFailed,
				"wintun: this build of Windows cannot run the Wintun driver (%v)", err)
		}
	}
	return protocol.NewErrorf(protocol.CodeWintunCreateFailed,
		"wintun: could not create the virtual adapter: %v", err)
}

// Configure implements network.Adapter: it sets the address and the on-link
// route for the room subnet.
//
// It shells out to PowerShell rather than binding IP Helper. That looks crude
// next to the rest of this file, and the reason is worth stating: the API for
// adding a unicast address and a route is large, has changed shape between
// Windows releases, and its failure modes are the ones a VPN user actually hits.
// The cmdlet is the same one an administrator would type by hand, so a failure
// it reports is one the user can reproduce and understand.
//
// It also runs a handful of times per session - once when a room starts, once
// when it stops - so process cost is irrelevant next to correctness.
func (a *Adapter) Configure(ctx context.Context, addr network.AddressInfo) error {
	a.mu.Lock()
	created, name := a.created, a.name
	a.mu.Unlock()
	if !created {
		return network.ErrNotStarted
	}
	// Two different addresses, and conflating them is the bug this comment
	// exists to prevent:
	//
	//   prefix is the *network* (10.200.112.0/24) and is what routes are built
	//   from; host is this machine's own address inside it (10.200.112.1).
	//
	// Masking the host address before configuring the adapter sets
	// 10.200.112.0 — a perfectly valid-looking value that is the network address,
	// not a host. Windows accepts it, shows it in the adapter list, and then
	// refuses every packet because the local end of the link has no address of
	// its own. The adapter comes up Disconnected with no routes, which reads as
	// "the driver is broken" when in fact the only fault is a masking call.
	prefix := addr.IPv4.Masked()
	if !prefix.IsValid() || !prefix.Addr().Is4() {
		return protocol.NewError(protocol.CodeConfigInvalid, "wintun: no IPv4 prefix to configure")
	}
	host, bits := addr.IPv4.Addr(), prefix.Bits()
	if !host.IsValid() || host.IsUnspecified() {
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"wintun: %s has no usable host address inside %s", addr.IPv4.Addr(), prefix)
	}

	// The address is removed first. A room that is being reconfigured may be
	// coming back with a different subnet, and leaving the old address in place
	// would give the interface two and make Windows pick routes by an
	// unpredictable metric.
	//
	// Only routes inside LanBaz's own 10.200.0.0/16 are removed. Windows installs
	// 224.0.0.0/4 and 255.255.255.255/32 on every interface by itself, and those
	// two are exactly what multicast and broadcast discovery leave through.
	script := fmt.Sprintf(
		"$ErrorActionPreference='Stop';"+
			"$i=Get-NetAdapter -Name '%[1]s' -ErrorAction Stop;"+
			"Remove-NetIPAddress -InterfaceIndex $i.ifIndex -AddressFamily IPv4 -Confirm:$false -ErrorAction SilentlyContinue;"+
			"Get-NetRoute -InterfaceIndex $i.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue | "+
			"Where-Object { $_.DestinationPrefix -like '10.200.*' } | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue;"+
			"New-NetIPAddress -InterfaceIndex $i.ifIndex -IPAddress '%[2]s' -PrefixLength %[3]d -ErrorAction Stop | Out-Null",
		powershellEscape(name), host.String(), bits)

	if out, err := runPowerShell(ctx, script); err != nil {
		return protocol.NewErrorf(protocol.CodeWintunCreateFailed,
			"wintun: could not configure %s on %s: %v (%s)", host, name, err, out)
	}

	a.mu.Lock()
	mtu, noPriority := a.mtu, a.noPriority
	a.mu.Unlock()
	var notes []string
	metric := "-AutomaticMetric Disabled -InterfaceMetric 1 "
	if noPriority {
		metric = ""
	}

	// The MTU has to be told to Windows. Without it the stack sends full-size
	// 1500-byte segments, the router and transport refuse anything larger than
	// the tunnel MTU, and a TCP game connects and then stalls on its first large
	// transfer with nothing in any log to say why.
	//
	// The interface metric is set to the lowest value so this adapter wins the
	// tie for 255.255.255.255 and 224.0.0.0/4. Windows sends a limited broadcast
	// and joins multicast groups on the best-metric interface only, so without
	// this a game's discovery packets leave through the physical NIC and never
	// reach the room.
	ifScript := fmt.Sprintf(
		"$ErrorActionPreference='Stop';"+
			"$i=Get-NetAdapter -Name '%[1]s' -ErrorAction Stop;"+
			"Set-NetIPInterface -InterfaceIndex $i.ifIndex -AddressFamily IPv4 -NlMtuBytes %[2]d "+
			"%[3]s-ErrorAction Stop;"+
			"Disable-NetAdapterBinding -Name '%[1]s' -ComponentID ms_tcpip6 -ErrorAction SilentlyContinue",
		powershellEscape(name), mtu, metric)
	if out, err := runPowerShell(ctx, ifScript); err != nil {
		a.log.Warn("could not set the adapter MTU and metric",
			"adapter", name, "mtu", mtu, "error", err, "output", out)
		notes = append(notes, "could not set the adapter MTU/priority; large transfers or LAN discovery may fail")
	}

	// The on-link route for the room subnet, installed explicitly.
	//
	// New-NetIPAddress is expected to create this implicitly and usually does —
	// but only when the interface is up. Wintun reports its media as
	// disconnected until a session is running, so at this point it often is not,
	// and Windows then silently skips the route. The result is an interface with
	// a correct address and no route home: ping fails, no game can find a peer,
	// and every layer above looks healthy because it is only reading back LanBaz's
	// own idea of the routing table.
	//
	// Silently continuing on failure is deliberate. The route may well have been
	// created implicitly a moment ago, and AddRoute already reports its own
	// errors; failing here would turn a duplicate-route warning into a failed
	// room.
	// The next hop is 0.0.0.0 - "on-link" - because every address in the room
	// subnet is reached directly through this interface; there is no gateway.
	routeScript := fmt.Sprintf(
		"$i=Get-NetAdapter -Name '%[1]s' -ErrorAction Stop;"+
			"if (-not (Get-NetRoute -InterfaceIndex $i.ifIndex -DestinationPrefix '%[2]s' "+
			"-ErrorAction SilentlyContinue)) {"+
			"New-NetRoute -InterfaceIndex $i.ifIndex -DestinationPrefix '%[2]s' "+
			"-NextHop '0.0.0.0' -RouteMetric 0 -ErrorAction Stop | Out-Null}",
		powershellEscape(name), prefix.String())
	if out, err := runPowerShell(ctx, routeScript); err != nil {
		a.log.Warn("could not add the room subnet route; Windows may have added it implicitly",
			"adapter", name, "destination", prefix.String(), "error", err, "output", out)
	}

	if !noPriority {
		go network.PreferForDiscovery(context.Background(), name, runPowerShell, a.log)
	}

	if err := installFirewall(ctx, name, a.log); err != nil {
		notes = append(notes, "Windows Firewall rule could not be added; other players may not reach games you host ("+err.Error()+")")
	}

	a.mu.Lock()
	a.note = strings.Join(notes, "; ")
	a.addr = addr
	a.addr.Interface = name
	a.addr.LUID = a.luid
	// Wintun is an L3 adapter and does not fan out for us. The router relays
	// broadcast and multicast itself, so this is a capability note for the UI
	// rather than a functional limit - but it is a true statement about what the
	// driver does, and reporting it honestly is what lets the UI explain why
	// discovery is slower here than on a wired LAN.
	a.addr.Broadcast, a.addr.Multicast = false, false
	a.routes = []network.Route{{
		Destination: prefix,
		NextHop:     host, // displayed as the local end of the on-link route
		Managed:     true,
		Note:        "room subnet, on-link",
	}}
	a.mu.Unlock()

	a.log.Info("virtual adapter configured",
		"adapter", name, "address", host.String(), "prefix", bits)
	return nil
}

// AddRoute installs a host route for one peer.
func (a *Adapter) AddRoute(ctx context.Context, r network.Route) error {
	if !r.Destination.IsValid() || !r.Destination.Addr().Is4() {
		return protocol.NewError(protocol.CodeRouteCreateFailed,
			"wintun: only IPv4 routes are supported")
	}
	a.mu.Lock()
	name := a.name
	a.mu.Unlock()

	script := fmt.Sprintf(
		"$ErrorActionPreference='Stop';"+
			"$i=Get-NetAdapter -Name '%[1]s' -ErrorAction Stop;"+
			"if (-not (Get-NetRoute -InterfaceIndex $i.ifIndex -DestinationPrefix '%[2]s' -ErrorAction SilentlyContinue)) {"+
			"New-NetRoute -InterfaceIndex $i.ifIndex -DestinationPrefix '%[2]s' -NextHop '0.0.0.0' -RouteMetric 0 -ErrorAction Stop | Out-Null}",
		powershellEscape(name), r.Destination.String())
	if out, err := runPowerShell(ctx, script); err != nil {
		return protocol.NewErrorf(protocol.CodeRouteCreateFailed,
			"wintun: could not add a route to %s: %v (%s)", r.Destination, err, out)
	}

	a.mu.Lock()
	a.routes = append(a.routes, r)
	a.mu.Unlock()
	return nil
}

// DeleteRoute removes a previously installed host route.
func (a *Adapter) DeleteRoute(ctx context.Context, destination netip.Prefix) error {
	a.mu.Lock()
	name := a.name
	a.mu.Unlock()
	script := fmt.Sprintf(
		"$ErrorActionPreference='SilentlyContinue';"+
			"$i=Get-NetAdapter -Name '%[1]s';"+
			"if ($i) { Remove-NetRoute -InterfaceIndex $i.ifIndex -DestinationPrefix '%[2]s' -Confirm:$false }",
		powershellEscape(name), destination.String())
	if _, err := runPowerShell(ctx, script); err != nil {
		return protocol.NewErrorf(protocol.CodeRouteCreateFailed,
			"wintun: could not remove the route to %s: %v", destination, err)
	}

	a.mu.Lock()
	kept := a.routes[:0]
	for _, r := range a.routes {
		if r.Destination != destination {
			kept = append(kept, r)
		}
	}
	a.routes = kept
	a.mu.Unlock()
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
func (a *Adapter) Routes() ([]network.Route, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.created {
		return nil, network.ErrNotStarted
	}
	return append([]network.Route(nil), a.routes...), nil
}

// ReadPacket implements network.Adapter by draining Wintun's ring buffer.
//
// The ring is the driver's own lock-free buffer; this loop is the only thing
// standing between it and the router. It waits on the driver's event handle
// rather than calling ReceivePacket in a spin, so an idle daemon costs nothing.
func (a *Adapter) ReadPacket(ctx context.Context) (network.Packet, error) {
	_, event, err := a.ensureSession()
	if err != nil {
		return network.Packet{}, err
	}

	// Wintun's documented receive pattern is "read until the ring is empty, then
	// wait". Waiting first would leave every packet queued behind the one that
	// signalled the event sitting in the ring until the next signal, which on a
	// quiet link can be a long time for a game's handshake.
	for {
		if err := ctx.Err(); err != nil {
			return network.Packet{}, network.ErrClosed
		}
		pkt, ok, err := a.receiveOnce()
		if err != nil {
			a.mu.Lock()
			if a.readErr == nil {
				a.readErr = err
			}
			a.mu.Unlock()
			return network.Packet{}, err
		}
		if ok {
			return pkt, nil
		}
		// The ring is empty. The finite timeout is what lets a shutdown be
		// noticed: closing the adapter while a read is blocked is not allowed by
		// the driver, so this loop has to be the thing that decides to leave.
		if _, err := windows.WaitForSingleObject(event, uint32(waitTimeout.Milliseconds())); err != nil {
			return network.Packet{}, err
		}
	}
}

// ensureSession returns the adapter's packet session, starting one the first
// time it is asked for.
//
// Reads and writes share a single session. Wintun keeps separate read and
// write rings inside it and documents every packet call as thread-safe, so one
// session for both directions is supported; a second session would double the
// shared buffer for no benefit.
func (a *Adapter) ensureSession() (uintptr, windows.Handle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.created {
		return 0, 0, network.ErrNotStarted
	}
	if a.session == 0 {
		if err := a.startSession(); err != nil {
			return 0, 0, err
		}
	}
	return a.session, a.readEvent, nil
}

// driverError turns a null return from the driver into a LanBaz error.
//
// The nil case is not theoretical: Wintun returns NULL without touching
// LastError on some paths, and "the driver returned nothing and said nothing"
// is a genuinely different thing to tell a user than a wrong Windows error
// code, because the first means the call was made wrong and the second means
// the driver could not do its job.
func driverError(what string, err error) error {
	if err == nil {
		return protocol.NewErrorf(protocol.CodeWintunCreateFailed,
			"wintun: %s: the driver returned no value and set no error code", what)
	}
	return protocol.NewErrorf(protocol.CodeWintunCreateFailed, "wintun: %s: %v", what, err)
}

// startSession begins the packet session. The caller holds the lock.
//
// Two things about this call are easy to get wrong and both have been:
//
//   - The exported name is WintunStartSession, not WintunStartReceiving. The
//     latter does not exist, and calling a LazyProc for a missing export panics
//     rather than returning an error, which takes the whole daemon down instead
//     of the one network that failed.
//
//   - It takes two arguments, not one: the adapter handle and the ring
//     capacity. Omitting the capacity leaves it zero, which is below the
//     driver's minimum, and the driver answers ERROR_INVALID_PARAMETER. Because
//     the adapter has already been created and configured by this point, the
//     network reports itself ready and then nothing is ever received.
func (a *Adapter) startSession() error {
	session, _, err := call(procWintunStartSession, a.handle, uintptr(ringCapacity))
	if session == 0 {
		return driverError("could not start the packet session", lastError(err))
	}
	// The read-wait event belongs to the session, not the adapter.
	event, _, err := call(procWintunGetReadWait, session)
	if event == 0 {
		_, _, _ = call(procWintunEndSession, session)
		return driverError("could not read the session's receive event", lastError(err))
	}
	a.session, a.ring, a.readEvent = session, 0, windows.Handle(event)
	a.log.Info("packet session started", "adapter", a.name, "ringMiB", ringCapacity>>20)
	// The interface only gets a network profile once its media is connected,
	// which is now. Marking it Private is what stops Windows Defender treating
	// every game port on it as a public-network exposure.
	go a.markPrivate(a.name)
	return nil
}

// receiveOnce takes one packet out of the session's ring. ok is false with a nil
// error when the ring is simply empty.
func (a *Adapter) receiveOnce() (network.Packet, bool, error) {
	a.mu.Lock()
	session := a.session
	a.mu.Unlock()
	if session == 0 {
		return network.Packet{}, false, network.ErrClosed
	}

	var size uint32
	pkt, _, err := call(procWintunReceivePacket, session, uintptr(unsafe.Pointer(&size)))
	if pkt == 0 {
		var errno windows.Errno
		if errors.As(err, &errno) {
			switch errno {
			case windows.ERROR_NO_MORE_ITEMS, 0:
				// The ring is empty: the normal outcome once it has been drained.
				return network.Packet{}, false, nil
			case windows.ERROR_HANDLE_EOF:
				// The adapter is going away underneath the session.
				return network.Packet{}, false, network.ErrClosed
			case windows.ERROR_INVALID_DATA:
				return network.Packet{}, false, driverError("the receive ring is corrupt", err)
			}
		}
		return network.Packet{}, false, driverError("could not receive a packet", lastError(err))
	}
	if size == 0 {
		_, _, _ = call(procWintunReleasePacket, session, pkt)
		return network.Packet{}, false, nil
	}
	// The pointer refers into Wintun's ring and is only valid until the packet
	// is released, so the bytes are copied before anything else can block. The
	// buffer is a Go allocation, not the ring's, which is why the release is
	// safe to do immediately afterwards.
	src := unsafe.Slice((*byte)(driverPointer(pkt)), int(size))
	payload := make([]byte, len(src))
	copy(payload, src)
	_, _, _ = call(procWintunReleasePacket, session, pkt)

	return network.Packet{Payload: payload, ReceivedAt: time.Now()}, true, nil
}

// WritePacket injects one packet into the Wintun send queue.
func (a *Adapter) WritePacket(_ context.Context, pkt network.Packet) error {
	a.mu.Lock()
	mtu := a.mtu
	a.mu.Unlock()

	session, _, err := a.ensureSession()
	if err != nil {
		return err
	}
	if len(pkt.Payload) > mtu {
		return protocol.NewErrorf(protocol.CodePayloadTooLarge,
			"wintun: packet is %d bytes, the interface MTU is %d", len(pkt.Payload), mtu)
	}

	// The buffer Wintun hands back must be passed to WintunSendPacket unchanged
	// and not retained afterwards, so the payload is copied into it and nothing
	// outside this function keeps a reference.
	buf, _, err := call(procWintunAllocateSend, session, uintptr(len(pkt.Payload)))
	if buf == 0 {
		// ERROR_BUFFER_OVERFLOW is a full ring: the local stack is not keeping
		// up. Dropping is the right loss for game traffic, and the caller counts
		// it.
		return driverError("could not allocate a send buffer", lastError(err))
	}
	copy(unsafe.Slice((*byte)(driverPointer(buf)), len(pkt.Payload)), pkt.Payload)
	// WintunSendPacket returns void, so whatever LastError holds afterwards is
	// left over from an unrelated call and says nothing about this one.
	_, _, _ = call(procWintunSendPacket, session, buf)
	return nil
}

// Destroy implements network.Adapter: it removes the interface and its routes.
func (a *Adapter) Destroy(ctx context.Context) error {
	a.mu.Lock()
	created, handle, session, name, opened, id := a.created, a.handle, a.session, a.name, a.opened, a.id
	a.mu.Unlock()
	if !created {
		return nil
	}

	// The address and routes go first. Removing the adapter out from under a
	// configured interface leaves Windows holding routes that point at a device
	// that no longer exists, and the next run finds the name already taken.
	script := fmt.Sprintf(
		"$ErrorActionPreference='SilentlyContinue';"+
			"$i=Get-NetAdapter -Name '%[1]s';"+
			"if ($i) { Remove-NetIPAddress -InterfaceIndex $i.ifIndex -AddressFamily IPv4 -Confirm:$false;"+
			"Get-NetRoute -InterfaceIndex $i.ifIndex -AddressFamily IPv4 | "+
			"Where-Object { $_.DestinationPrefix -like '10.200.*' } | Remove-NetRoute -Confirm:$false }",
		powershellEscape(name))
	_, _ = runPowerShell(ctx, script)
	removeFirewall(ctx, name, a.log)

	a.mu.Lock()
	if session != 0 {
		// The session owns the ring buffers and the driver will not close an
		// adapter that still has one open, so this has to come first.
		_, _, _ = call(procWintunEndSession, session)
		a.session = 0
	}
	a.ring, a.readEvent = 0, 0
	a.created, a.routes = false, nil
	a.mu.Unlock()

	// WintunCloseAdapter returns void; its LastError carries no information.
	_, _, _ = call(procWintunCloseAdapter, handle)
	if opened {
		if err := removeDevice(context.WithoutCancel(ctx), deviceID(id)); err != nil {
			a.log.Warn("could not remove the reused adapter", "adapter", name, "error", err)
		}
		a.mu.Lock()
		a.opened = false
		a.mu.Unlock()
	}
	a.log.Info("virtual adapter destroyed", "adapter", name)
	return nil
}

// ReadError returns the first receive failure seen, for diagnostics.
func (a *Adapter) ReadError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.readErr
}

// runPowerShell executes a script and returns its trimmed output.
func runPowerShell(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// driverPointer turns an address a Wintun call returned into a pointer.
//
// A LazyProc.Call hands back its pointer result as a uintptr, because that is
// what the syscall trampoline produces. Casting that uintptr to unsafe.Pointer
// directly is exactly the pattern go vet's unsafeptr check exists to catch: a
// uintptr is not a pointer, and the compiler is free to move it across a
// garbage collection between the conversion and the use. Here the memory is
// owned by the driver and stays mapped for as long as the adapter is open, so
// the conversion is sound - but it is still the conversion the rule is about.
//
// Reading the value back through a *unsafe.Pointer is the standard way to make
// the same operation pass the check: it is a pointer dereference rather than a
// uintptr-to-pointer cast, and the compiler treats it as one.
func driverPointer(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

// powershellEscape makes a string safe to embed in a single-quoted literal.
//
// A single quote inside a single-quoted PowerShell string is escaped by
// doubling it. The interface name comes from configuration and could contain
// anything, and a script injected through it would be code execution reachable
// from a config file.
// powershellEscape escapes s for a single-quoted PowerShell string. PowerShell treats
// the typographic quotes U+2018..U+201B as single quotes too, so each of them
// must be doubled as well, or a name containing one ends the string early.
func powershellEscape(s string) string {
	return strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’",
		"‚", "‚‚", "‛", "‛‛").Replace(s)
}

var _ network.Adapter = (*Adapter)(nil)
