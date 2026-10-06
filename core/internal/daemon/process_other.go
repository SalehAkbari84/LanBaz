//go:build !windows

package daemon

import (
	"errors"
	"syscall"
)

// processAlive reports whether a pid still exists. Signal 0 checks existence
// without delivering anything; EPERM means it exists but belongs to someone else.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
