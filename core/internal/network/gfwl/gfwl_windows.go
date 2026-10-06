//go:build windows

// Package gfwl points Games for Windows Live at the LanBaz adapter.
//
// GFWL games (Gears of War, Fallout 3, GTA IV, Dark Souls, Bulletstorm, ...)
// send and listen for system-link (LAN) traffic only on the adapter GFWL
// considers primary - the Wi-Fi or Ethernet card - so a host on a virtual LAN
// never appears in anybody's list. GFWL honours a per-user override naming the
// adapter to use:
//
//	HKCU\Software\Classes\Software\Microsoft\XLive  ConnectionOverride (REG_SZ) = <connection name>
//
// It is what the Hamachi and Tunngle guides set by hand ("Hamachi"). LanBaz
// sets it to its own adapter while a room is open and puts the previous value
// back afterwards. Nothing is written when GFWL was never installed.
package gfwl

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows/registry"
)

const (
	keyPath   = `Software\Classes\Software\Microsoft\XLive`
	valueName = "ConnectionOverride"
)

var mu sync.Mutex

// Point sets the override to adapter and returns a function that restores the
// previous state. ok is false (and nothing changed) when GFWL is not installed.
func Point(adapter string) (restore func(), previous string, ok bool, err error) {
	mu.Lock()
	defer mu.Unlock()
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		// GFWL creates the key the first time a game runs; when the runtime
		// is installed but no game has run yet, create it so the very first
		// launch already uses LanBaz.
		if !installed() {
			return func() {}, "", false, nil
		}
		k, _, err = registry.CreateKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	}
	if err != nil {
		return func() {}, "", false, err
	}
	defer k.Close()
	prev, _, perr := k.GetStringValue(valueName)
	hadPrev := perr == nil
	if err := k.SetStringValue(valueName, adapter); err != nil {
		return func() {}, prev, false, err
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE|registry.SET_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		// Only undo our own change: another room (or the user) may have set
		// it since.
		if cur, _, err := k.GetStringValue(valueName); err != nil || cur != adapter {
			return
		}
		if hadPrev {
			_ = k.SetStringValue(valueName, prev)
		} else {
			_ = k.DeleteValue(valueName)
		}
	}, prev, true, nil
}

// installed reports whether the GFWL runtime (xlive.dll) is on this PC.
func installed() bool {
	win := os.Getenv("SystemRoot")
	if win == "" {
		win = `C:\Windows`
	}
	for _, dir := range []string{"SysWOW64", "System32"} {
		if _, err := os.Stat(filepath.Join(win, dir, "xlive.dll")); err == nil {
			return true
		}
	}
	return false
}
