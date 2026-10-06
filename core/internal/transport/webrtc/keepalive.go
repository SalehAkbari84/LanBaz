package webrtc

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun/v4"
	ptransport "github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

// natKeepalive is how often every ICE socket sends a STUN request while a
// link is being set up.
//
// The invite carries the host's public address:port as its router mapped it
// at the moment the invite was made. Until the reply comes back, nothing is
// sent from those sockets, and a home router forgets an idle UDP mapping after
// roughly 30 to 120 seconds. The reply then lands on a port that no longer
// leads anywhere: the guest's checks go nowhere, and the host's checks leave
// from a new port that the guest's port-restricted router rejects. A tiny STUN
// request every 15 s keeps the mapping, so the address in the invite stays
// valid for as long as the invite does. The same goes for the guest's reply.
const natKeepalive = 15 * time.Second

// keepNet records every UDP socket ICE opens so the transport can keep its
// router mapping alive.
type keepNet struct {
	ptransport.Net

	mu    sync.Mutex
	conns map[ptransport.UDPConn]struct{}
}

func newKeepNet(base ptransport.Net) (*keepNet, error) {
	if base == nil {
		n, err := stdnet.NewNet()
		if err != nil {
			return nil, err
		}
		base = n
	}
	return &keepNet{Net: base, conns: map[ptransport.UDPConn]struct{}{}}, nil
}

// ListenUDP implements transport.Net.
func (k *keepNet) ListenUDP(network string, laddr *net.UDPAddr) (ptransport.UDPConn, error) {
	c, err := k.Net.ListenUDP(network, laddr)
	if err == nil {
		k.mu.Lock()
		k.conns[c] = struct{}{}
		k.mu.Unlock()
	}
	return c, err
}

// refresh sends one STUN binding request from every live socket. Pion ignores
// the answers (unknown transaction); what matters is the outgoing packet, which
// is what keeps the router's mapping. A socket that can no longer send has
// been closed and is forgotten.
func (k *keepNet) refresh(server *net.UDPAddr) {
	k.mu.Lock()
	conns := make([]ptransport.UDPConn, 0, len(k.conns))
	for c := range k.conns {
		conns = append(conns, c)
	}
	k.mu.Unlock()
	for _, c := range conns {
		m, err := stun.Build(stun.TransactionID, stun.BindingRequest)
		if err != nil {
			continue
		}
		if _, err := c.WriteTo(m.Raw, server); err != nil {
			k.mu.Lock()
			delete(k.conns, c)
			k.mu.Unlock()
		}
	}
}

// keepMappings refreshes the mappings until the transport shuts down.
func (t *Transport) keepMappings(k *keepNet, stunURLs []string, every time.Duration) {
	k.keep(stunURLs, every, t.done)
}

// KeepAliveNet returns LanBaz's network for a raw Pion PeerConnection (the
// diagnostic tool uses it): VPN bypass plus mapping keepalive until stop is
// closed.
func KeepAliveNet(stunURLs []string, stop <-chan struct{}) (ptransport.Net, error) {
	k, err := newKeepNet(NewBypassNet())
	if err != nil {
		return nil, err
	}
	go k.keep(stunURLs, natKeepalive, stop)
	return k, nil
}

func (k *keepNet) keep(stunURLs []string, every time.Duration, stop <-chan struct{}) {
	var server *net.UDPAddr
	for _, u := range stunURLs {
		hp := strings.TrimPrefix(strings.TrimPrefix(u, "stun:"), "stuns:")
		if i := strings.IndexByte(hp, '?'); i >= 0 {
			hp = hp[:i]
		}
		if a, err := k.ResolveUDPAddr("udp4", hp); err == nil {
			server = a
			break
		}
	}
	if server == nil {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			k.refresh(server)
		}
	}
}
