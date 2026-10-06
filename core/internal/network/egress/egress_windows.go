//go:build windows

// Package egress makes LanBaz's peer-to-peer sockets leave through the real
// network adapter even while a VPN holds the default route.
//
// Many players in Iran keep a VPN connected all the time. The VPN takes the
// default route, so a socket bound to the Wi-Fi address still leaves through
// the tunnel (Windows uses the weak host model when sending) and the friend
// sees the VPN provider's address behind the VPN's NAT, which is usually
// symmetric. Measured on a player's PC: through the VPN every STUN server saw a
// different port (symmetric); with IP_UNICAST_IF set to the Wi-Fi adapter they
// all saw the same, port-preserving mapping (cone).
//
// IP_UNICAST_IF pins a socket's unicast traffic to one interface regardless of
// the routing table, which is exactly that: game traffic goes direct, the VPN
// keeps everything else. LANBAZ_NO_BYPASS=1 turns it off.
package egress

import (
	"context"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ipUnicastIF   = 31 // IP_UNICAST_IF, level IPPROTO_IP, index in network byte order
	ipv6UnicastIF = 31 // IPV6_UNICAST_IF, level IPPROTO_IPV6, index in host order
)

// Enabled reports whether bypassing is on.
func Enabled() bool { return os.Getenv("LANBAZ_NO_BYPASS") == "" }

// ListenUDP opens a UDP socket pinned to the interface that owns laddr, or,
// for an unspecified laddr, to the physical internet adapter.
func ListenUDP(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
	if !Enabled() {
		return net.ListenUDP(network, laddr)
	}
	idx := 0
	if laddr != nil && laddr.IP != nil && !laddr.IP.IsUnspecified() {
		idx = indexForIP(laddr.IP)
	} else {
		idx, _ = Physical()
	}
	addr := ""
	if laddr != nil {
		addr = laddr.String()
	}
	lc := net.ListenConfig{Control: func(netw, _ string, c syscall.RawConn) error {
		if idx <= 0 {
			return nil
		}
		_ = c.Control(func(fd uintptr) {
			// Failing to pin is not fatal: the socket then simply follows the
			// routing table, as it would have anyway.
			if strings.HasSuffix(netw, "6") {
				_ = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, ipv6UnicastIF, idx)
			} else {
				_ = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIF, int(htonl(uint32(idx))))
			}
		})
		return nil
	}}
	pc, err := lc.ListenPacket(context.Background(), network, addr)
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

func htonl(v uint32) uint32 {
	return v>>24 | (v>>8)&0xff00 | (v<<8)&0xff0000 | v<<24
}

// indexForIP finds the interface that owns an address.
func indexForIP(ip net.IP) int {
	ifs, err := net.Interfaces()
	if err != nil {
		return 0
	}
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return ifc.Index
			}
		}
	}
	return 0
}

var (
	physMu   sync.Mutex
	physAt   time.Time
	physIdx  int
	physName string
)

// Physical returns the real internet adapter: up, Ethernet/Wi-Fi/mobile, with
// an IPv4 gateway, not a tunnel or virtual switch; the lowest metric wins. The
// answer is cached for 20 s. Zero means none was found.
func Physical() (int, string) {
	physMu.Lock()
	defer physMu.Unlock()
	if time.Since(physAt) < 20*time.Second {
		return physIdx, physName
	}
	physIdx, physName = findPhysical()
	physAt = time.Now()
	return physIdx, physName
}

// virtualWords mark adapters that are not the real uplink even when their
// interface type says Ethernet.
var virtualWords = []string{
	"virtual", "hyper-v", "vmware", "virtualbox", "tap-windows", "wintun", "wireguard",
	"vpn", "tunnel", "loopback", "pdanet", "zerotier", "tailscale", "radmin", "hamachi",
	"openvpn", "npcap", "bluetooth", "lanbaz",
}

func findPhysical() (int, string) {
	var size uint32 = 16 * 1024
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return 0, ""
		}
		bestIdx, bestName, bestMetric := 0, "", uint32(1<<31)
		for a := first; a != nil; a = a.Next {
			if a.OperStatus != windows.IfOperStatusUp || a.FirstGatewayAddress == nil {
				continue
			}
			switch a.IfType {
			case 6, 71, 243, 244: // Ethernet, Wi-Fi, WWAN, WWAN2
			default:
				continue
			}
			desc := strings.ToLower(windows.UTF16PtrToString(a.Description) + " " + windows.UTF16PtrToString(a.FriendlyName))
			skip := false
			for _, w := range virtualWords {
				if strings.Contains(desc, w) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			if a.Ipv4Metric < bestMetric {
				bestIdx, bestName, bestMetric = int(a.IfIndex), windows.UTF16PtrToString(a.FriendlyName), a.Ipv4Metric
			}
		}
		return bestIdx, bestName
	}
	return 0, ""
}

// PhysicalGateway returns the real internet adapter's IPv4 address and its
// default gateway (the home router), or invalid addresses when there is none.
// A VPN holding the default route does not change the answer.
func PhysicalGateway() (local, gateway netip.Addr) {
	idx, _ := Physical()
	if idx == 0 {
		return
	}
	var size uint32 = 16 * 1024
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return
		}
		for a := first; a != nil; a = a.Next {
			if int(a.IfIndex) != idx {
				continue
			}
			for u := a.FirstUnicastAddress; u != nil; u = u.Next {
				if ip, ok := netip.AddrFromSlice(u.Address.IP()); ok && ip.Unmap().Is4() {
					local = ip.Unmap()
					break
				}
			}
			for g := a.FirstGatewayAddress; g != nil; g = g.Next {
				if ip, ok := netip.AddrFromSlice(g.Address.IP()); ok && ip.Unmap().Is4() {
					gateway = ip.Unmap()
					break
				}
			}
			return
		}
		return
	}
	return
}
