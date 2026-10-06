package network_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/memory"
	"github.com/lanbaz/lanbaz/core/internal/transport"
)

type l2node struct {
	tr      *fakeTransport
	adapter *memory.Adapter
	sw      *network.Switch
	addr    netip.Addr
}

func newL2Node(t *testing.T, b *bus, addr string, host bool) *l2node {
	t.Helper()
	tr := newFakeTransport(b, transport.PeerID(addr))
	ad := memory.New()
	sw, err := network.NewSwitch(network.SwitchConfig{
		RoomID: "lbzroom-l2", IsHost: host, Transport: tr, Adapter: ad, Backend: "memory",
		Subnet: testSubnet(), Local: netip.MustParseAddr(addr), MTU: 1200, Logger: quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sw.Stop(context.Background()) })
	return &l2node{tr: tr, adapter: ad, sw: sw, addr: netip.MustParseAddr(addr)}
}

func l2join(t *testing.T, a, b *l2node) {
	t.Helper()
	a.tr.link(b.tr.id)
	b.tr.link(a.tr.id)
	_ = a.sw.AddPeer(string(b.tr.id), b.addr.String())
	_ = b.sw.AddPeer(string(a.tr.id), a.addr.String())
}

func frame(dst, src [6]byte, ethertype uint16, payload string) []byte {
	f := make([]byte, 14+len(payload))
	copy(f[0:6], dst[:])
	copy(f[6:12], src[:])
	f[12], f[13] = byte(ethertype>>8), byte(ethertype)
	copy(f[14:], payload)
	return f
}

var (
	bcast = [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	macA  = [6]byte{2, 0, 0, 0, 0, 0xa}
	macB  = [6]byte{2, 0, 0, 0, 0, 0xb}
)

// A broadcast (an ARP or IPX discovery) from one guest reaches the other guest
// through the host, then a unicast reply finds its way back by learned MAC.
func TestSwitchFloodsBroadcastAndLearnsUnicast(t *testing.T) {
	b := newBus()
	host := newL2Node(t, b, "10.200.17.1", true)
	alice := newL2Node(t, b, "10.200.17.2", false)
	bob := newL2Node(t, b, "10.200.17.3", false)
	l2join(t, host, alice)
	l2join(t, host, bob)

	alice.adapter.Inject(network.Packet{Payload: frame(bcast, macA, 0x8137, "ipx-hello")})
	if _, err := bob.adapter.Delivered(3 * time.Second); err != nil {
		t.Fatalf("bob did not get alice's broadcast: %v", err)
	}
	if _, err := host.adapter.Delivered(3 * time.Second); err != nil {
		t.Fatalf("the host did not get alice's broadcast: %v", err)
	}

	bob.adapter.Inject(network.Packet{Payload: frame(macA, macB, 0x8137, "ipx-reply")})
	got, err := alice.adapter.Delivered(3 * time.Second)
	if err != nil {
		t.Fatalf("alice did not get bob's unicast reply: %v", err)
	}
	if string(got.Payload[14:]) != "ipx-reply" {
		t.Fatalf("alice got %q", got.Payload[14:])
	}
	// The host knew alice's MAC, so the reply was forwarded to her alone.
	if extra, _ := host.adapter.Delivered(200 * time.Millisecond); len(extra.Payload) != 0 {
		t.Fatalf("the host delivered a unicast frame meant for alice to itself")
	}
}

// A guest cannot take over a MAC another guest already uses.
func TestSwitchRefusesAStolenMAC(t *testing.T) {
	b := newBus()
	host := newL2Node(t, b, "10.200.17.1", true)
	alice := newL2Node(t, b, "10.200.17.2", false)
	mallory := newL2Node(t, b, "10.200.17.3", false)
	l2join(t, host, alice)
	l2join(t, host, mallory)

	alice.adapter.Inject(network.Packet{Payload: frame(bcast, macA, 0x0806, "arp")})
	if _, err := host.adapter.Delivered(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	_, _ = mallory.adapter.Delivered(time.Second)

	mallory.adapter.Inject(network.Packet{Payload: frame(bcast, macA, 0x0806, "spoof")})
	if got, _ := host.adapter.Delivered(300 * time.Millisecond); len(got.Payload) != 0 {
		t.Fatalf("the host accepted a frame from a stolen MAC: %q", got.Payload[14:])
	}
}
