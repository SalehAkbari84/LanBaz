//go:build windows

package daemon

import (
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32")
	openProcess        = kernel32.NewProc("OpenProcess")
	closeHandle        = kernel32.NewProc("CloseHandle")
	processStillActive = kernel32.NewProc("GetExitCodeProcess")
)

const (
	// exitCodeStillActive is the value GetExitCodeProcess reports for a process
	// that has not exited. Checking the exit code as well as the handle matters
	// because a handle can outlive its process and Windows reuses pids.
	exitCodeStillActive = 259 // STILL_ACTIVE
	// processQueryLimitedInformation is the narrowest right that still allows
	// GetExitCodeProcess, and it is granted across elevation boundaries - an
	// elevated daemon can query a non-elevated shell and vice versa.
	processQueryLimitedInformation = 0x1000
	errorInvalidParameter          = 87
)

// processAlive reports whether a pid still exists.
//
// Only ERROR_INVALID_PARAMETER - "there is no such pid" - counts as dead. Any
// other failure, access denied in particular, is treated as alive: a daemon
// left running is recoverable by the next launch, a wrong kill is not.
func processAlive(pid int) bool {
	handle, _, err := openProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		if errno, ok := err.(syscall.Errno); ok && errno == errorInvalidParameter {
			return false
		}
		return true
	}
	var code uint32
	ret, _, _ := processStillActive.Call(handle, uintptr(unsafe.Pointer(&code)))
	_, _, _ = closeHandle.Call(handle)
	if ret == 0 {
		return true
	}
	return code == exitCodeStillActive
}
