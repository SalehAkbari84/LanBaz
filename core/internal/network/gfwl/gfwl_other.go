//go:build !windows

// Package gfwl points Games for Windows Live at the LanBaz adapter; GFWL only
// exists on Windows.
package gfwl

// Point does nothing off Windows.
func Point(string) (func(), string, bool, error) { return func() {}, "", false, nil }
