package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// IPv4 wire format, from RFC 791 section 3.1.
//
// LanBaz parses the header and nothing else. It is not a stack: there is no
// TCP, no UDP and no ICMP here. The header carries exactly three facts the
// router cannot work without - where the packet came from, where it is going,
// and how far it may still travel - and everything below it is opaque bytes
// that the operating system's own stack will handle once the packet is
// delivered. Parsing any deeper would mean reimplementing the host's IP stack
// inside a VPN, which is how a tunnel ends up disagreeing with the machine it
// is supposed to be extending.
const (
	// MinHeaderLen is the length of an IPv4 header carrying no options.
	MinHeaderLen = 20
	// MaxHeaderLen is the largest an IPv4 header can be, with a 15-word IHL.
	MaxHeaderLen = 60
	// Version is the value expected in the high nibble of the first byte.
	Version = 4
	// Protocol numbers LanBaz names. It reports them in logs and metrics but
	// takes no action on them.
	ProtocolICMP = 1
	ProtocolTCP  = 6
	ProtocolUDP  = 17

	ttlOffset      = 8
	checksumOffset = 10
	srcOffset      = 12
	dstOffset      = 16
)

// Errors returned when a buffer cannot be read as an IPv4 packet.
//
// They are plain errors rather than protocol.Error because a malformed packet
// is dropped, not reported: nothing the user did causes it and nothing the user
// could do about it belongs in an API response. The counters in Metrics are
// where a flood of these becomes visible.
var (
	// ErrNotIPv4 means the version nibble was not 4.
	ErrNotIPv4 = errors.New("network: not an IPv4 packet")
	// ErrShortPacket means the buffer is smaller than the header claims.
	ErrShortPacket = errors.New("network: packet is shorter than its IPv4 header")
)

// IPv4 is a parsed IPv4 header. The fields are the ones routing needs; the
// packet it was parsed from is retained so a caller can rewrite the header in
// place without reassembling anything.
type IPv4 struct {
	// Src and Dst are the addresses in the header. They are the only routing
	// input the router trusts from a remote peer.
	Src netip.Addr
	Dst netip.Addr
	// Protocol is the IP protocol number, e.g. 17 for UDP.
	Protocol uint8
	// TTL is the remaining hop count. LanBaz decrements it as it forwards, so a
	// packet cannot circulate between two daemons forever.
	TTL uint8
	// HeaderLen is the header size in bytes, options included.
	HeaderLen int
	// TotalLen is the value of the header's total-length field. It can be
	// smaller than the buffer when the transport handed over trailing padding.
	TotalLen int
}

// LimitedBroadcast is 255.255.255.255, the "everyone, everywhere" address. It
// is distinct from a subnet's own broadcast address and is the one most games
// actually use.
var LimitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})

// MulticastRange is 224.0.0.0/4, the range LAN discovery lives in: mDNS on
// 224.0.0.251:5353, SSDP on 239.255.255.250:1900 and Minecraft Java's "Open to
// LAN" announcement on 224.0.2.60:4445.
var MulticastRange = netip.MustParsePrefix("224.0.0.0/4")

// ParseIPv4 reads the header out of a raw packet.
//
// It is deliberately strict about length and deliberately forgiving about
// everything else. A packet whose declared header runs past the end of the
// buffer is refused, because reading it would mean trusting a length a remote
// peer chose. A packet whose total-length field does not match the buffer is
// accepted: the transport adds no framing of its own, but a peer running a
// different implementation might pad, and refusing on that would turn a
// cosmetic difference into an outage.
func ParseIPv4(b []byte) (IPv4, error) {
	var h IPv4
	if len(b) < MinHeaderLen {
		return h, fmt.Errorf("%w: %d bytes", ErrShortPacket, len(b))
	}
	if v := b[0] >> 4; v != Version {
		return h, fmt.Errorf("%w: version %d", ErrNotIPv4, v)
	}
	// The IHL counts 32-bit words. A zero would make the header length zero and
	// every later field read from the wrong offset, so it is refused rather than
	// defaulted.
	ihl := int(b[0]&0x0f) * 4
	if ihl < MinHeaderLen || ihl > MaxHeaderLen || ihl > len(b) {
		return h, fmt.Errorf("%w: header length %d in %d bytes", ErrShortPacket, ihl, len(b))
	}
	h.HeaderLen = ihl
	h.TotalLen = int(binary.BigEndian.Uint16(b[2:4]))
	// Zero means "the rest of the buffer". Real stacks accept it for packets
	// that came off a TSO path; accepting it here costs nothing and refusing it
	// would drop traffic from a peer that behaves this way.
	if h.TotalLen == 0 || h.TotalLen > len(b) {
		h.TotalLen = len(b)
	}
	h.TTL = b[ttlOffset]
	h.Protocol = b[9]
	h.Src = netip.AddrFrom4([4]byte{b[srcOffset], b[srcOffset+1], b[srcOffset+2], b[srcOffset+3]})
	h.Dst = netip.AddrFrom4([4]byte{b[dstOffset], b[dstOffset+1], b[dstOffset+2], b[dstOffset+3]})
	return h, nil
}

// IsMulticast reports whether the destination is in 224.0.0.0/4.
func (h IPv4) IsMulticast() bool {
	return h.Dst.Is4() && MulticastRange.Contains(h.Dst)
}

// IsLimitedBroadcast reports whether the destination is 255.255.255.255.
//
// This is checked separately from the subnet's own broadcast address because a
// limited broadcast reaches hosts outside the subnet as well, and on a virtual
// LAN "outside the subnet" is exactly the set of peers the hub has to relay to.
func (h IPv4) IsLimitedBroadcast() bool { return h.Dst == LimitedBroadcast }

// IsSubnetBroadcast reports whether the destination is the broadcast address of
// subnet, i.e. the subnet's base address with every host bit set.
func (h IPv4) IsSubnetBroadcast(subnet netip.Prefix) bool {
	if !subnet.IsValid() || !subnet.Addr().Is4() {
		return false
	}
	return h.Dst == SubnetBroadcast(subnet)
}

// IsBroadcast reports whether the packet is addressed to a broadcast address of
// any kind.
func (h IPv4) IsBroadcast(subnet netip.Prefix) bool {
	return h.IsLimitedBroadcast() || h.IsSubnetBroadcast(subnet)
}

// decrementTTL subtracts one from the header's time-to-live in place and
// repairs the header checksum, reporting whether the packet may still be
// forwarded.
//
// The checksum has to be recomputed rather than merely decremented: it is the
// one's complement of the sum of every 16-bit word of the header, so changing
// any field invalidates it. Windows verifies it on delivery, so a forwarded
// packet with a stale checksum would arrive broken and be discarded silently,
// which is indistinguishable from a lossy link.
//
// A packet whose TTL is already 1 expires here. That is the whole point of the
// field in a relayed topology: two daemons forwarding each other's broadcasts
// would otherwise keep them alive indefinitely.
func decrementTTL(b []byte) bool {
	if len(b) <= ttlOffset {
		return false
	}
	if b[ttlOffset] <= 1 {
		return false
	}
	b[ttlOffset]--
	// The checksum covers the whole header, so it is summed again from scratch
	// over the options-included length rather than patched in place.
	binary.BigEndian.PutUint16(b[checksumOffset:checksumOffset+2], 0)
	ihl := int(b[0]&0x0f) * 4
	if ihl < MinHeaderLen || ihl > len(b) {
		ihl = MinHeaderLen
	}
	binary.BigEndian.PutUint16(b[checksumOffset:checksumOffset+2], headerChecksum(b[:ihl]))
	return true
}

// headerChecksum computes the RFC 1071 one's complement checksum of a header.
func headerChecksum(hdr []byte) uint16 {
	var sum uint32
	i := 0
	for ; i+1 < len(hdr); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(hdr[i : i+2]))
	}
	if i < len(hdr) {
		// A trailing odd byte is padded on the right with a zero byte, which is
		// what the wire format specifies.
		sum += uint32(hdr[i]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// SubnetBroadcast returns the directed broadcast address of subnet.
//
// A /24 has 32 addresses: the base, the directed broadcast, and 30 usable host
// addresses. LanBaz reserves the base implicitly (it is never assigned to
// anybody) and the broadcast explicitly, which leaves .1 for the room host and
// .2 through .254 for peers.
func SubnetBroadcast(subnet netip.Prefix) netip.Addr {
	if !subnet.IsValid() || !subnet.Addr().Is4() {
		return netip.Addr{}
	}
	base := subnet.Masked().Addr().As4()
	host := uint32(0xffffffff) >> uint(subnet.Bits())
	if subnet.Bits() >= 32 {
		host = 0
	}
	v := (uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])) | host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// HostAddr returns the n-th usable host address of subnet, counting the base
// as 0. It returns the zero address for anything else.
//
// Offsets 0 and 255 are refused. They are inside the prefix, so a naive bounds
// check would let them through, but they are the subnet's own network and
// broadcast addresses: handing either to a peer produces an address no host on
// the LAN can own, and the symptom - a peer that vanishes only when somebody
// pings it - is a miserable one to debug.
func HostAddr(subnet netip.Prefix, n int) netip.Addr {
	if !subnet.IsValid() || !subnet.Addr().Is4() {
		return netip.Addr{}
	}
	if n <= 0 || n > 254 {
		return netip.Addr{}
	}
	base := subnet.Masked().Addr()
	a := base.As4()
	a[3] = byte(n)
	addr := netip.AddrFrom4(a)
	if !subnet.Contains(addr) {
		return netip.Addr{}
	}
	return addr
}

// FormatProtocol renders a protocol number for a log line.
func FormatProtocol(p uint8) string {
	switch p {
	case ProtocolICMP:
		return "icmp"
	case ProtocolTCP:
		return "tcp"
	case ProtocolUDP:
		return "udp"
	default:
		return fmt.Sprintf("ip(%d)", p)
	}
}
