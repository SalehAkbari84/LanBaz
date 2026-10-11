//go:build windows

package steam

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Locate returns Steam's install folder, or "" when Steam is not installed.
func Locate() string {
	try := []struct {
		root registry.Key
		path string
		name string
	}{
		{registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath"},
	}
	for _, t := range try {
		k, err := registry.OpenKey(t.root, t.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue(t.name)
		k.Close()
		if err != nil || v == "" {
			continue
		}
		p := filepath.Clean(filepath.FromSlash(v))
		if _, err := os.Stat(filepath.Join(p, "steamapps")); err == nil {
			return p
		}
	}
	return ""
}

// Running reports whether the Steam client is running.
func Running() bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), "steam.exe") {
			return true
		}
	}
	return false
}
