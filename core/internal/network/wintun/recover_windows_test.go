//go:build windows

package wintun

import (
	"regexp"
	"testing"
)

var deviceIDForm = regexp.MustCompile(`^SWD\\WINTUN\\\{[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\}$`)

func TestDeviceIDMatchesWindowsFormat(t *testing.T) {
	id := deviceID(deriveGUID("LanBaz-faf04140"))
	if !deviceIDForm.MatchString(id) {
		t.Fatalf("device id %q is not in the SWD/WINTUN/{GUID} form", id)
	}
	if deviceID(deriveGUID("LanBaz-faf04140")) != id {
		t.Fatal("the device id is not stable for a name")
	}
}
