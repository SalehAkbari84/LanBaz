package network

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func dnsQuery(id uint16, name string, qtype uint16) []byte {
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	msg = append(msg, encodeName(name)...)
	q := make([]byte, 4)
	binary.BigEndian.PutUint16(q[0:2], qtype)
	binary.BigEndian.PutUint16(q[2:4], dnsClassIN)
	return append(msg, q...)
}

func TestNameLabel(t *testing.T) {
	cases := map[string]string{"Ali": "ali", "Sara Gamer": "sara-gamer", "  x__y ": "x-y", "علی": "", "PC-01": "pc-01"}
	for in, want := range cases {
		if got := NameLabel(in); got != want {
			t.Errorf("NameLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnswersMDNSAndLLMNRForPlayers(t *testing.T) {
	names := map[string]netip.Addr{"ali": netip.MustParseAddr("10.200.5.2")}
	me := netip.MustParseAddr("10.200.5.1")

	// mDNS from port 5353: multicast reply, no question echoed.
	q := buildUDP(me, mdnsGroup, mdnsPort, mdnsPort, dnsQuery(0, "Ali.local", dnsTypeA), 255)
	resp := answerNameQuery(q, names)
	if resp == nil {
		t.Fatal("no mDNS answer")
	}
	h, _ := ParseIPv4(resp)
	if h.Src.String() != "10.200.5.2" || h.Dst != mdnsGroup {
		t.Fatalf("mDNS reply %s -> %s", h.Src, h.Dst)
	}
	if got := resp[len(resp)-4:]; got[0] != 10 || got[3] != 2 {
		t.Fatalf("answer rdata %v", got)
	}

	// LLMNR: unicast reply to the asker, id echoed.
	q = buildUDP(me, llmnrGroup, 51000, llmnrPort, dnsQuery(0xbeef, "ali", dnsTypeA), 1)
	resp = answerNameQuery(q, names)
	if resp == nil {
		t.Fatal("no LLMNR answer")
	}
	h, _ = ParseIPv4(resp)
	if h.Dst != me {
		t.Fatalf("LLMNR reply to %s, want %s", h.Dst, me)
	}
	udp := resp[h.HeaderLen:]
	if binary.BigEndian.Uint16(udp[2:4]) != 51000 || binary.BigEndian.Uint16(udp[8:10]) != 0xbeef {
		t.Fatal("LLMNR reply does not echo the port and id")
	}

	// Unknown names and other services are ignored.
	if answerNameQuery(buildUDP(me, mdnsGroup, mdnsPort, mdnsPort, dnsQuery(0, "bob.local", dnsTypeA), 255), names) != nil {
		t.Fatal("answered for an unknown name")
	}
	if answerNameQuery(buildUDP(me, mdnsGroup, mdnsPort, mdnsPort, dnsQuery(0, "_http._tcp.local", 12), 255), names) != nil {
		t.Fatal("answered a PTR query")
	}
}
