//go:build windows && wtprobe

// Command wtprobe reports what the Wintun driver actually does, step by step.
//
// It deliberately does not use the lanbaz wintun package. That package wraps
// every call and reports a curated error, which is exactly what makes a driver
// level failure hard to see: the real cause of a broken LAN was a single
// missing argument to WintunStartSession, and by the time it reached the daemon
// log it read as "The parameter is incorrect" with nothing pointing at the call.
//
// This prints the raw GetLastError after every step and lists the exports the
// DLL actually has, so a signature mismatch is visible instead of inferred. It
// loads the driver by explicit path rather than through the search path, which
// also answers the question "is the DLL next to the app the one I think it is".
//
// Build:
//
//	go build -tags wtprobe -o wtprobe.exe ./scripts/wtprobe
//
// Run as administrator; creating an adapter needs it:
//
//	wtprobe.exe "C:\Program Files\LanBaz\wintun.dll"   (per-machine install)
//
// Expected output ends with:
//
//	WintunStartSession(adapter, 4MiB) -> 0x...  lasterr=<nil>
//	WintunGetReadWaitEvent(session) -> 0x... lasterr=<nil>
//	WintunEndSession(session) ok
//	OK
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")
	procLoadLib = modkernel32.NewProc("LoadLibraryW")
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func loadDLL(path string) (uintptr, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, _, err := procLoadLib.Call(uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return 0, err
	}
	return h, nil
}

func lastErr() error { return syscall.GetLastError() }

func proc2(dll uintptr, name string) (uintptr, error) {
	p, err := syscall.GetProcAddress(syscall.Handle(dll), name)
	if err != nil {
		return 0, fmt.Errorf("GetProcAddress(%s): %w", name, err)
	}
	return p, nil
}

// On Windows syscall.Syscall takes (trap, nargs, a1, a2, a3).
func call1(fn uintptr, arg uintptr) uintptr {
	r, _, _ := syscall.Syscall(fn, 1, arg, 0, 0)
	return r
}

func call2(fn uintptr, a, b uintptr) uintptr {
	r, _, _ := syscall.Syscall(fn, 2, a, b, 0)
	return r
}

func call3(fn uintptr, a, b, c uintptr) uintptr {
	r, _, _ := syscall.Syscall(fn, 3, a, b, c)
	return r
}

func ptr(s string) uintptr {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return uintptr(unsafe.Pointer(p))
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: wtprobe <path-to-wintun.dll>")
		os.Exit(2)
	}
	path := os.Args[1]

	dll, err := loadDLL(path)
	if err != nil {
		fmt.Printf("FAIL load %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("loaded %s handle=%#x\n\n", path, dll)

	want := []string{
		"WintunCreateAdapter", "WintunCreateAdapterEx",
		"WintunCloseAdapter", "WintunStartSession", "WintunEndSession",
		"WintunGetReadWaitEvent", "WintunReceivePacket",
		"WintunReleaseReceivePacket", "WintunAllocateSendPacket",
		"WintunSendPacket", "WintunGetAdapterLUID", "WintunSetLogger",
	}
	fmt.Println("exports:")
	found := map[string]uintptr{}
	for _, n := range want {
		p, err := proc2(dll, n)
		if err != nil {
			fmt.Printf("  %-26s MISSING (%v)\n", n, err)
			continue
		}
		fmt.Printf("  %-26s ok %#x\n", n, p)
		found[n] = p
	}
	fmt.Println()

	create, ok1 := found["WintunCreateAdapter"]
	start, ok2 := found["WintunStartSession"]
	luidFn, ok3 := found["WintunGetAdapterLUID"]
	waitFn, ok4 := found["WintunGetReadWaitEvent"]
	endFn, _ := found["WintunEndSession"]
	if !ok1 || !ok2 || !ok3 || !ok4 {
		fmt.Println("FAIL: the DLL does not export the core Wintun API")
		os.Exit(1)
	}

	g := guid{Data1: 0x1badb002, Data2: 0x4a10, Data3: 0x9c17}
	g.Data4 = [8]byte{'l', 'a', 'n', 'b', 'a', 'z', '0', '1'}

	name := "LanBazProbe"
	adapter := call3(create, ptr(name), ptr("LanBaz probe"), uintptr(unsafe.Pointer(&g)))
	fmt.Printf("WintunCreateAdapter -> %#x  lasterr=%v\n", adapter, lastErr())
	if adapter == 0 {
		fmt.Println("FAIL: could not create adapter (run as administrator?)")
		os.Exit(1)
	}
	defer func() {
		if closeFn, ok := found["WintunCloseAdapter"]; ok {
			call2(closeFn, adapter, 0)
		}
	}()

	var luid uint64
	call2(luidFn, adapter, uintptr(unsafe.Pointer(&luid)))
	fmt.Printf("WintunGetAdapterLUID -> luid=%#x lasterr=%v\n", luid, lastErr())

	session := call2(start, adapter, 4<<20)
	fmt.Printf("WintunStartSession(adapter, 4MiB) -> %#x  lasterr=%v\n", session, lastErr())
	if session == 0 {
		fmt.Println("FAIL: StartSession refused the adapter this DLL just created")
		os.Exit(1)
	}

	event := call1(waitFn, session)
	fmt.Printf("WintunGetReadWaitEvent(session) -> %#x lasterr=%v\n", event, lastErr())
	if event == 0 {
		fmt.Println("FAIL: no receive event for the session")
		os.Exit(1)
	}
	if endFn != 0 {
		call1(endFn, session)
		fmt.Println("WintunEndSession(session) ok")
	}
	fmt.Println("OK")
}
