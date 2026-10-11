//go:build !windows

package steam

// Locate is Windows-only for now.
func Locate() string { return "" }

// Running is Windows-only for now.
func Running() bool { return false }
