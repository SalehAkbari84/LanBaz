//go:build windows

package wintun

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// LazyProc.Call reports success as Errno(0). Every session and packet call
// once treated that as a failure, so no packet ever moved; this pins the rule.
func TestLastErrorTreatsErrnoZeroAsSuccess(t *testing.T) {
	if err := lastError(windows.Errno(0)); err != nil {
		t.Fatalf("lastError(Errno(0)) = %v, want nil", err)
	}
	if err := lastError(nil); err != nil {
		t.Fatalf("lastError(nil) = %v, want nil", err)
	}
	err := lastError(windows.ERROR_ACCESS_DENIED)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("lastError(ERROR_ACCESS_DENIED) = %v, want it kept", err)
	}
}
