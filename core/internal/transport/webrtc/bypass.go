package webrtc

import (
	"net"

	ptransport "github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"

	"github.com/lanbaz/lanbaz/core/internal/network/egress"
)

// bypassNet is Pion's standard network with one change: every UDP socket ICE
// opens is pinned to the interface it belongs to (see package egress), so a
// connected VPN cannot pull LanBaz's peer-to-peer traffic into its tunnel and
// behind its symmetric NAT.
type bypassNet struct {
	*stdnet.Net
}

// NewBypassNet returns nil when bypassing is off or unavailable; the caller
// then keeps Pion's default network.
func NewBypassNet() ptransport.Net {
	if !egress.Enabled() {
		return nil
	}
	n, err := stdnet.NewNet()
	if err != nil {
		return nil
	}
	return bypassNet{n}
}

// ListenUDP implements transport.Net.
func (b bypassNet) ListenUDP(network string, laddr *net.UDPAddr) (ptransport.UDPConn, error) {
	return egress.ListenUDP(network, laddr)
}
