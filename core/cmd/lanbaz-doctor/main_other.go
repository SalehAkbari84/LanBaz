//go:build !windows

// Command lanbaz-doctor diagnoses LanBaz on Windows.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "lanbaz-doctor runs on Windows only")
	os.Exit(1)
}
