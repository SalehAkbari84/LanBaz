//go:build windows

package gfwl

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// Runs against the real HKCU key only when it already exists, and restores it.
func TestPointSetsAndRestores(t *testing.T) {
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Skip("GFWL is not installed here")
	}
	before, _, beforeErr := k.GetStringValue(valueName)
	k.Close()

	restore, _, ok, err := Point("LanBaz-test1234")
	if err != nil || !ok {
		t.Fatalf("Point: ok=%v err=%v", ok, err)
	}
	k, _ = registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
	got, _, _ := k.GetStringValue(valueName)
	k.Close()
	if got != "LanBaz-test1234" {
		t.Fatalf("override is %q", got)
	}
	restore()
	k, _ = registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
	after, _, afterErr := k.GetStringValue(valueName)
	k.Close()
	if (beforeErr == nil) != (afterErr == nil) || before != after {
		t.Fatalf("not restored: before %q/%v, after %q/%v", before, beforeErr, after, afterErr)
	}
}
