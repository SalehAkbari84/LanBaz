//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/wintun"
)

const (
	docAdapter = "LanBaz-Doctor"
	docPrefix  = "10.200.254.1/24"
)

var (
	docLocal  = netip.MustParseAddr("10.200.254.1")
	docFriend = netip.MustParseAddr("10.200.254.9")
)

// layerWintun builds a real LanBaz adapter and pushes packets through it in
// both directions, which is exactly what a room does with game traffic.
func layerWintun(r *report, admin bool) {
	r.section("2. Wintun virtual adapter (data path)")
	if !admin {
		r.fail("skipped: needs administrator rights")
		return
	}
	log := slog.New(slog.NewTextHandler(fileOnly{r}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a, err := wintun.New(wintun.Options{Name: docAdapter, Logger: log})
	if err != nil {
		r.fail("wintun: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t0 := time.Now()
	if err := a.Create(ctx, docAdapter, 1200); err != nil {
		r.fail("create adapter: %v", err)
		return
	}
	r.ok("adapter created in %s", time.Since(t0).Round(time.Millisecond))
	defer func() {
		t := time.Now()
		if err := a.Destroy(context.Background()); err != nil {
			r.fail("remove adapter: %v", err)
		}
		out, _ := ps(`Get-NetAdapter -IncludeHidden -Name '`+docAdapter+`' -ErrorAction SilentlyContinue | ForEach-Object { $_.Status }`, 0)
		if out != "" {
			r.warn("the test adapter is still listed after removal (%s)", out)
		} else {
			r.ok("adapter removed cleanly in %s", time.Since(t).Round(time.Millisecond))
		}
	}()

	t1 := time.Now()
	err = a.Configure(ctx, network.AddressInfo{
		Interface: docAdapter, IPv4: netip.MustParsePrefix(docPrefix),
		Gateway: docLocal, MTU: 1200,
	})
	took := time.Since(t1).Round(time.Millisecond)
	if err != nil {
		r.fail("configure adapter (address, route, firewall): %v", err)
		return
	}
	if took > 15*time.Second {
		r.warn("configuring the adapter took %s (PowerShell is slow on this PC; rooms start slowly)", took)
	} else {
		r.ok("adapter configured in %s", took)
	}
	if n := a.Note(); n != "" {
		r.warn("configuration note: %s", n)
	}
	st, _ := ps(`$i=Get-NetAdapter -Name '`+docAdapter+`'; `+
		`"status: $($i.Status)"; `+
		`Get-NetIPAddress -InterfaceIndex $i.ifIndex -AddressFamily IPv4 | ForEach-Object { "address: $($_.IPAddress)/$($_.PrefixLength)" }; `+
		`Get-NetIPInterface -InterfaceIndex $i.ifIndex -AddressFamily IPv4 | ForEach-Object { "mtu: $($_.NlMtu)  metric: $($_.InterfaceMetric)  automatic metric: $($_.AutomaticMetric)" }; `+
		`Get-NetRoute -InterfaceIndex $i.ifIndex -AddressFamily IPv4 | Where-Object { $_.DestinationPrefix -like '10.200.*' } | ForEach-Object { "route: $($_.DestinationPrefix) via $($_.NextHop)" }; `+
		`Get-NetFirewallRule -Name 'LanBaz-In-`+docAdapter+`' -ErrorAction SilentlyContinue | ForEach-Object { "firewall rule: $($_.DisplayName) enabled=$($_.Enabled)" }`, 0)
	r.info("adapter state as Windows sees it:")
	r.block(st)
	if !strings.Contains(st, "firewall rule:") {
		r.fail("the LanBaz firewall rule for the adapter is missing")
	}
	if !strings.Contains(st, "10.200.254.0/24") {
		r.warn("no 10.200.254.0/24 route on the adapter")
	}

	// Collect everything Windows sends into the adapter.
	pkts := make(chan []byte, 256)
	readCtx, stopRead := context.WithCancel(ctx)
	defer stopRead()
	go func() {
		for {
			p, err := a.ReadPacket(readCtx)
			if err != nil {
				close(pkts)
				return
			}
			select {
			case pkts <- append([]byte(nil), p.Payload...):
			default:
			}
		}
	}()
	time.Sleep(1500 * time.Millisecond) // let the interface come up

	// Outbound: a game on this PC sends to a friend's room address.
	outbound(r, pkts, "unicast to a friend (10.200.254.9)", "10.200.254.9:40000", docLocal, false)
	outbound(r, pkts, "subnet broadcast (10.200.254.255) - LAN game discovery", "10.200.254.255:40001", docLocal, true)
	outbound(r, pkts, "limited broadcast (255.255.255.255) from any address - most old games", "255.255.255.255:40002", netip.Addr{}, true)

	// Inbound: a friend's packet arrives from the room and must reach a
	// program on this PC. This is the firewall test.
	inbound(ctx, r, a)

	// Whatever else Windows sent into the adapter, for the record.
	other := map[string]int{}
	drain := time.After(300 * time.Millisecond)
loop:
	for {
		select {
		case p, ok := <-pkts:
			if !ok {
				break loop
			}
			other[describe(p)]++
		case <-drain:
			break loop
		}
	}
	if len(other) > 0 {
		var b strings.Builder
		for k, v := range other {
			fmt.Fprintf(&b, "%dx %s\n", v, k)
		}
		r.info("other traffic Windows sent into the adapter:")
		r.block(b.String())
	}
}

// outbound sends one UDP datagram from this PC and checks it comes out of the
// adapter.
func outbound(r *report, pkts <-chan []byte, what, dst string, bind netip.Addr, broadcast bool) {
	raddr, _ := net.ResolveUDPAddr("udp4", dst)
	laddr := &net.UDPAddr{}
	if bind.IsValid() {
		laddr.IP = net.IP(bind.AsSlice())
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		_ = c.Control(func(fd uintptr) {
			if broadcast {
				serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_BROADCAST, 1)
			}
		})
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", laddr.String())
	if err != nil {
		r.fail("%s: cannot open a socket: %v", what, err)
		return
	}
	defer pc.Close()
	marker := []byte(fmt.Sprintf("lanbaz-doctor %d", time.Now().UnixNano()))
	for i := 0; i < 3; i++ {
		if _, err := pc.WriteTo(marker, raddr); err != nil {
			r.fail("%s: send failed: %v", what, err)
			return
		}
		deadline := time.After(700 * time.Millisecond)
	wait:
		for {
			select {
			case p, ok := <-pkts:
				if !ok {
					r.fail("%s: the adapter stopped delivering packets", what)
					return
				}
				if bytes.Contains(p, marker) {
					r.ok("%s: left through the LanBaz adapter (src %s)", what, srcOf(p))
					return
				}
			case <-deadline:
				break wait
			}
		}
	}
	if broadcast && !bind.IsValid() {
		r.warn("%s: Windows sent it out of another adapter, not LanBaz (another adapter has a better metric; old games may not find rooms while it is active)", what)
		return
	}
	r.fail("%s: never reached the LanBaz adapter", what)
}

// inbound injects a datagram "from a friend" and checks a local program
// receives it.
func inbound(ctx context.Context, r *report, a *wintun.Adapter) {
	ln, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		r.fail("inbound: cannot listen: %v", err)
		return
	}
	defer ln.Close()
	port := ln.LocalAddr().(*net.UDPAddr).Port
	marker := []byte(fmt.Sprintf("lanbaz-doctor-in %d", time.Now().UnixNano()))
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 2048)
		_ = ln.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			n, from, err := ln.ReadFromUDP(buf)
			if err != nil {
				close(got)
				return
			}
			if bytes.Equal(buf[:n], marker) {
				got <- from.String()
				return
			}
		}
	}()
	pkt := buildUDP(docFriend, docLocal, 40100, uint16(port), marker)
	for i := 0; i < 3; i++ {
		if err := a.WritePacket(ctx, network.Packet{Payload: pkt}); err != nil {
			r.fail("inbound: writing into the adapter failed: %v", err)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	if from, ok := <-got; ok {
		r.ok("inbound: a packet from a friend (%s) reached a program on this PC - firewall OK", from)
	} else {
		r.fail("inbound: a packet from a friend was NOT delivered to the program - a firewall (Windows or third-party) is dropping room traffic")
	}
}

// buildUDP makes an IPv4/UDP datagram with valid checksums.
func buildUDP(src, dst netip.Addr, sport, dport uint16, payload []byte) []byte {
	ulen := 8 + len(payload)
	p := make([]byte, 20+ulen)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[8] = 64
	p[9] = 17
	s4, d4 := src.As4(), dst.As4()
	copy(p[12:16], s4[:])
	copy(p[16:20], d4[:])
	binary.BigEndian.PutUint16(p[10:], checksum(p[:20], 0))
	u := p[20:]
	binary.BigEndian.PutUint16(u[0:], sport)
	binary.BigEndian.PutUint16(u[2:], dport)
	binary.BigEndian.PutUint16(u[4:], uint16(ulen))
	copy(u[8:], payload)
	var pseudo uint32
	pseudo += uint32(s4[0])<<8 | uint32(s4[1])
	pseudo += uint32(s4[2])<<8 | uint32(s4[3])
	pseudo += uint32(d4[0])<<8 | uint32(d4[1])
	pseudo += uint32(d4[2])<<8 | uint32(d4[3])
	pseudo += 17 + uint32(ulen)
	cs := checksum(u, pseudo)
	if cs == 0 {
		cs = 0xffff
	}
	binary.BigEndian.PutUint16(u[6:], cs)
	return p
}

func checksum(b []byte, sum uint32) uint16 {
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func srcOf(p []byte) string {
	if len(p) < 20 {
		return "?"
	}
	a, _ := netip.AddrFromSlice(p[12:16])
	return a.String()
}

// describe names a packet for the "other traffic" list.
func describe(p []byte) string {
	if len(p) < 20 || p[0]>>4 != 4 {
		return "non-IPv4"
	}
	dst, _ := netip.AddrFromSlice(p[16:20])
	ihl := int(p[0]&0x0f) * 4
	switch p[9] {
	case 1:
		return "ICMP to " + dst.String()
	case 2:
		return "IGMP to " + dst.String()
	case 6, 17:
		proto := "UDP"
		if p[9] == 6 {
			proto = "TCP"
		}
		if len(p) >= ihl+4 {
			return fmt.Sprintf("%s to %s:%d", proto, dst, binary.BigEndian.Uint16(p[ihl+2:]))
		}
		return proto + " to " + dst.String()
	}
	return fmt.Sprintf("proto %d to %s", p[9], dst)
}

// fileOnly sends the adapter's own log lines to the report file only.
type fileOnly struct{ r *report }

func (f fileOnly) Write(p []byte) (int, error) {
	_, _ = f.r.f.Write(append([]byte("         log: "), p...))
	return len(p), nil
}
