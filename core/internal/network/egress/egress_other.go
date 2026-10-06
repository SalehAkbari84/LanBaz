//go:build !windows

// Package egress pins LanBaz's sockets to the real network adapter on Windows;
// elsewhere it is a plain listen.
package egress

import (
	"net"
	"net/netip"
)

// Enabled reports whether bypassing is on.
func Enabled() bool { return false }

// ListenUDP opens a UDP socket.
func ListenUDP(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
	return net.ListenUDP(network, laddr)
}

// Physical is not implemented off Windows.
func Physical() (int, string) { return 0, "" }

// PhysicalGateway is not implemented off Windows.
func PhysicalGateway() (local, gateway netip.Addr) { return }
