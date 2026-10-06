//go:build windows

package wintun

import (
	"syscall"
	"testing"
)

func TestAlreadyExistsMatchesTheDriverError(t *testing.T) {
	for _, code := range []uintptr{183, 80, 0x800700B7} {
		var err error = syscall.Errno(code)
		if !alreadyExists(lastError(err)) {
			t.Errorf("alreadyExists(%#x) = false", code)
		}
	}
	if alreadyExists(syscall.Errno(5)) {
		t.Error("access denied is not 'already exists'")
	}
}

// PowerShell closes a single-quoted string on U+2018..U+201B as well as on
// ASCII ', so every one of them must be doubled.
func TestPowershellEscapeDoublesEveryQuote(t *testing.T) {
	for _, q := range []string{"'", "‘", "’", "‚", "‛"} {
		if got, want := powershellEscape("a"+q+"b"), "a"+q+q+"b"; got != want {
			t.Errorf("powershellEscape(%q) = %q, want %q", "a"+q+"b", got, want)
		}
	}
}
