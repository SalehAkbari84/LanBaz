//go:build windows

package tap

import (
	"golang.org/x/sys/windows"
)

// ProbeResult is one TAP adapter as the diagnostic tool sees it.
type ProbeResult struct {
	GUID, Alias, Description string
	// Eligible is true when LanBaz would consider this adapter.
	Eligible bool
	// Opened is true when the device file could be opened right now.
	Opened bool
	Error  string
}

// Probe lists every tap0901 adapter and tries to open each one, closing it
// again at once. It changes nothing: no rename, no restart, no media status.
func Probe() []ProbeResult {
	var all []candidate
	if list, err := findAdapters(); err == nil {
		all = list
	}
	eligible := map[string]bool{}
	for _, c := range all {
		eligible[c.guid] = true
	}
	var out []ProbeResult
	for _, c := range allTap() {
		r := ProbeResult{GUID: c.guid, Alias: c.alias, Description: c.desc, Eligible: eligible[c.guid]}
		path, _ := windows.UTF16PtrFromString(`\\.\Global\` + c.guid + `.tap`)
		h, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_SYSTEM|windows.FILE_FLAG_OVERLAPPED, 0)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Opened = true
			_ = windows.CloseHandle(h)
		}
		out = append(out, r)
	}
	return out
}
