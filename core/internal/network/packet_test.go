package network

import (
	"net/netip"
	"testing"
)

// buildIPv4 assembles a minimal, valid IPv4 packet so a test can state exactly
// which bytes it means. Hand-rolling beats a fixture here because a test that
// fails should point at the field that changed, not at a base64 blob.
func buildIPv4(src, dst string, proto uint8, ttl uint8, payload int) []byte {
	pkt := make([]byte, MinHeaderLen+payload)
	pkt[0] = 0x45 // version 4, 5 words of header
	total := MinHeaderLen + payload
	pkt[2] = byte(total >> 8)
	pkt[3] = byte(total)
	pkt[8] = ttl
	pkt[9] = proto
	s := netip.MustParseAddr(src).As4()
	d := netip.MustParseAddr(dst).As4()
	copy(pkt[srcOffset:srcOffset+4], s[:])
	copy(pkt[dstOffset:dstOffset+4], d[:])
	binaryPutUint16(pkt[checksumOffset:], headerChecksum(pkt[:MinHeaderLen]))
	for i := MinHeaderLen; i < len(pkt); i++ {
		pkt[i] = byte(i)
	}
	return pkt
}

func binaryPutUint16(b []byte, v uint16) {
	b[0] = byte(v >> 8)
	b[1] = byte(v)
}

func TestParseIPv4ReadsTheHeaderFields(t *testing.T) {
	pkt := buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 64, 8)
	h, err := ParseIPv4(pkt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := h.Src.String(), "10.200.17.2"; got != want {
		t.Errorf("src = %s, want %s", got, want)
	}
	if got, want := h.Dst.String(), "10.200.17.3"; got != want {
		t.Errorf("dst = %s, want %s", got, want)
	}
	if h.Protocol != ProtocolUDP {
		t.Errorf("protocol = %d, want %d", h.Protocol, ProtocolUDP)
	}
	if h.TTL != 64 {
		t.Errorf("ttl = %d, want 64", h.TTL)
	}
	if h.HeaderLen != MinHeaderLen {
		t.Errorf("header len = %d, want %d", h.HeaderLen, MinHeaderLen)
	}
	if h.TotalLen != len(pkt) {
		t.Errorf("total len = %d, want %d", h.TotalLen, len(pkt))
	}
}

// A packet that is not IPv4 must be refused rather than parsed with whatever the
// first nibble happened to be: a version 6 packet's fields sit at completely
// different offsets, and reading them would produce a plausible-looking source
// address.
func TestParseIPv4RefusesOtherVersions(t *testing.T) {
	pkt := buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 64, 0)
	pkt[0] = 0x65
	if _, err := ParseIPv4(pkt); err == nil {
		t.Fatal("a version 6 packet was accepted")
	}
}

// A declared header length that runs past the buffer is the classic way to make
// a parser read fields that are not there. It must fail, not clamp.
func TestParseIPv4RefusesATruncatedHeader(t *testing.T) {
	pkt := buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 64, 0)
	pkt[0] = 0x4f // fifteen words: 60 bytes, but only 20 are present
	if _, err := ParseIPv4(pkt); err == nil {
		t.Fatal("a header claiming more bytes than the buffer holds was accepted")
	}
}

func TestParseIPv4RefusesAShortBuffer(t *testing.T) {
	if _, err := ParseIPv4(make([]byte, MinHeaderLen-1)); err == nil {
		t.Fatal("a buffer shorter than a header was accepted")
	}
	if _, err := ParseIPv4(nil); err == nil {
		t.Fatal("an empty buffer was accepted")
	}
}

// Options make the header longer than twenty bytes, and every field after the
// base header moves. A parser that hard-coded twenty would misread all of them.
func TestParseIPv4HandlesAHeaderWithOptions(t *testing.T) {
	pkt := make([]byte, MaxHeaderLen+4)
	pkt[0] = 0x46 // six words: 24 bytes
	s := netip.MustParseAddr("10.200.17.2").As4()
	d := netip.MustParseAddr("10.200.17.9").As4()
	copy(pkt[srcOffset:], s[:])
	copy(pkt[dstOffset:], d[:])
	h, err := ParseIPv4(pkt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.HeaderLen != 24 {
		t.Errorf("header len = %d, want 24", h.HeaderLen)
	}
	if h.Src.String() != "10.200.17.2" || h.Dst.String() != "10.200.17.9" {
		t.Errorf("addresses = %s -> %s, want 10.200.17.2 -> 10.200.17.9", h.Src, h.Dst)
	}
}

// A peer whose transport pads its frames must not have its packets refused.
func TestParseIPv4ToleratesABufferLongerThanTheHeader(t *testing.T) {
	pkt := buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 64, 0)
	pkt = append(pkt, 0xde, 0xad, 0xbe, 0xef)
	h, err := ParseIPv4(pkt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.TotalLen != MinHeaderLen {
		t.Errorf("total len = %d, want the declared length %d", h.TotalLen, MinHeaderLen)
	}
}

func TestBroadcastClassification(t *testing.T) {
	subnet := netip.MustParsePrefix("10.200.17.0/24")
	cases := []struct {
		name      string
		dst       string
		limited   bool
		subnetBrd bool
		multicast bool
	}{
		{"unicast", "10.200.17.4", false, false, false},
		{"limited broadcast", "255.255.255.255", true, false, false},
		{"subnet broadcast", "10.200.17.255", false, true, false},
		{"another subnet's broadcast", "10.200.18.255", false, false, false},
		{"mdns group", "224.0.0.251", false, false, true},
		{"multicast just inside", "224.0.0.0", false, false, true},
		{"multicast just outside", "223.255.255.255", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := ParseIPv4(buildIPv4("10.200.17.2", tc.dst, ProtocolUDP, 64, 0))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := h.IsLimitedBroadcast(); got != tc.limited {
				t.Errorf("IsLimitedBroadcast = %v, want %v", got, tc.limited)
			}
			if got := h.IsSubnetBroadcast(subnet); got != tc.subnetBrd {
				t.Errorf("IsSubnetBroadcast = %v, want %v", got, tc.subnetBrd)
			}
			if got := h.IsMulticast(); got != tc.multicast {
				t.Errorf("IsMulticast = %v, want %v", got, tc.multicast)
			}
			if got, want := h.IsBroadcast(subnet), tc.limited || tc.subnetBrd; got != want {
				t.Errorf("IsBroadcast = %v, want %v", got, want)
			}
		})
	}
}

// Decrementing TTL changes a header field, which invalidates the checksum.
// Windows verifies the checksum on delivery, so a forwarded packet with a stale
// one arrives broken - and looks exactly like a lossy link.
func TestDecrementTTLKeepsTheChecksumValid(t *testing.T) {
	pkt := buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 64, 0)
	if got, want := pkt[8], uint8(64); got != want {
		t.Fatalf("starting ttl = %d, want %d", got, want)
	}
	if !decrementTTL(pkt) {
		t.Fatal("a packet with ttl 64 was expired")
	}
	if got, want := pkt[8], uint8(63); got != want {
		t.Errorf("ttl = %d, want %d", got, want)
	}
	// A verifier recomputes the checksum over the whole header and expects zero.
	// That is the property that matters: Windows does exactly this on delivery,
	// and a header that fails it arrives as silently discarded.
	if v := headerChecksum(pkt[:MinHeaderLen]); v != 0 {
		t.Errorf("recomputing over the decremented header = %#04x, want 0", v)
	}
	// And the checksum the packet carries must be the one computed over the same
	// header with the field zeroed, or the two would not agree.
	zeroed := append([]byte(nil), pkt[:MinHeaderLen]...)
	binaryPutUint16(zeroed[checksumOffset:], 0)
	if got := uint16(pkt[10])<<8 | uint16(pkt[11]); got != headerChecksum(zeroed) {
		t.Errorf("stored checksum = %#04x, want %#04x", got, headerChecksum(zeroed))
	}
}

// A packet that has one hop left is the last one that can be forwarded, and it
// becomes the last one that can *arrive*. Expiring it here is what stops two
// daemons from circulating each other's broadcasts forever.
func TestDecrementTTLExpiresTheLastHop(t *testing.T) {
	if decrementTTL(buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 1, 0)) {
		t.Fatal("a packet with ttl 1 was forwarded")
	}
	if decrementTTL(buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 0, 0)) {
		t.Fatal("a packet with ttl 0 was forwarded")
	}
}

func TestDecrementTTLWithOptions(t *testing.T) {
	pkt := buildIPv4("10.200.17.2", "10.200.17.3", ProtocolUDP, 10, 0)
	pkt[0] = 0x46
	pkt = append(pkt, make([]byte, 4)...)
	if !decrementTTL(pkt) {
		t.Fatal("a packet with ttl 10 was expired")
	}
	if v := headerChecksum(pkt[:24]); v != 0 {
		t.Errorf("checksum over the options-bearing header = %#04x, want 0", v)
	}
}

func TestHostAddressHelpers(t *testing.T) {
	subnet := netip.MustParsePrefix("10.200.17.0/24")
	if got, want := HostAddressOf(subnet).String(), "10.200.17.1"; got != want {
		t.Errorf("host address = %s, want %s", got, want)
	}
	if got, want := SubnetBroadcast(subnet).String(), "10.200.17.255"; got != want {
		t.Errorf("subnet broadcast = %s, want %s", got, want)
	}
	if got, want := HostAddr(subnet, 254).String(), "10.200.17.254"; got != want {
		t.Errorf("host 254 = %s, want %s", got, want)
	}
	// The last host address is one below the broadcast address; asking for the
	// broadcast must not silently hand out the subnet's broadcast address.
	if got := HostAddr(subnet, 255); got.IsValid() {
		t.Errorf("host 255 = %s, want the zero address", got)
	}
}

func TestFormatProtocol(t *testing.T) {
	for proto, want := range map[uint8]string{
		ProtocolUDP:  "udp",
		ProtocolTCP:  "tcp",
		ProtocolICMP: "icmp",
		47:           "ip(47)",
	} {
		if got := FormatProtocol(proto); got != want {
			t.Errorf("FormatProtocol(%d) = %q, want %q", proto, got, want)
		}
	}
}

func TestSubnetBroadcastFollowsThePrefixLength(t *testing.T) {
	cases := map[string]string{
		"10.200.17.0/24": "10.200.17.255",
		"10.200.0.0/16":  "10.200.255.255",
		"10.200.17.0/25": "10.200.17.127",
		"10.200.17.5/24": "10.200.17.255",
	}
	for in, want := range cases {
		if got := SubnetBroadcast(netip.MustParsePrefix(in)).String(); got != want {
			t.Errorf("SubnetBroadcast(%s) = %s, want %s", in, got, want)
		}
	}
}
