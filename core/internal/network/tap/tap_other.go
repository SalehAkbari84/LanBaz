//go:build !windows

// Package tap is the TAP-Windows backend for Classic LAN rooms; it only exists
// on Windows.
package tap

import (
	"errors"
	"log/slog"

	"github.com/lanbaz/lanbaz/core/internal/network"
)

// AdapterName is the name LanBaz gives its TAP adapter.
const AdapterName = "LanBaz-L2"

// ErrNoDriver means no TAP adapter exists.
var ErrNoDriver = errors.New("the TAP-Windows driver is only available on Windows")

// Options configures New.
type Options struct {
	Logger     *slog.Logger
	NoPriority bool
}

// Available always fails off Windows.
func Available() error { return ErrNoDriver }

// New always fails off Windows.
func New(Options) (network.Adapter, error) { return nil, ErrNoDriver }
