//go:build !windows

package games

// scanProcesses is Windows-only for now; elsewhere nothing is detected.
func scanProcesses(func(exe string) bool) ([]Process, error) { return nil, nil }
