package network

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"unicode"
)

// Player names as addresses.
//
// Windows resolves "name.local" with mDNS (224.0.0.251:5353) and bare
// single-label names with LLMNR (224.0.0.252:5355), sending the query on every
// interface - the LanBaz adapter included. The other players' computers cannot
// answer for a LanBaz display name, so this node answers itself: the query is
// read off the adapter, matched against the room's player names, and a reply
// is written straight back into the adapter as if it came from that player.
//
// The result: a friend's name works wherever a game accepts a host name.

var (
	mdnsGroup  = netip.AddrFrom4([4]byte{224, 0, 0, 251})
	llmnrGroup = netip.AddrFrom4([4]byte{224, 0, 0, 252})
)

const (
	mdnsPort  = 5353
	llmnrPort = 5355

	dnsTypeA   = 1
	dnsTypeAny = 255
	dnsClassIN = 1
	nameTTL    = 120
)

// NameLabel turns a display name into a DNS label: lowercase ASCII letters,
// digits and hyphens. Names with no such characters (e.g. written entirely in
// another script) have no label; their owners are still reachable by address.
func NameLabel(display string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(display) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '-' || r == '_' || r == '.' || unicode.IsSpace(r):
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// answerNameQuery returns a reply packet for an mDNS or LLMNR query asking for
// one of names, or nil. names maps lowercase labels to addresses.
func answerNameQuery(pkt []byte, names map[string]netip.Addr) []byte {
	if len(names) == 0 {
		return nil
	}
	h, err := ParseIPv4(pkt)
	if err != nil || h.Protocol != ProtocolUDP || len(pkt) < h.HeaderLen+8 {
		return nil
	}
	udp := pkt[h.HeaderLen:]
	srcPort := int(binary.BigEndian.Uint16(udp[0:2]))
	dstPort := int(binary.BigEndian.Uint16(udp[2:4]))
	var llmnr bool
	switch {
	case h.Dst == mdnsGroup && dstPort == mdnsPort:
	case h.Dst == llmnrGroup && dstPort == llmnrPort:
		llmnr = true
	default:
		return nil
	}
	msg := udp[8:]
	if len(msg) < 12 {
		return nil
	}
	id := binary.BigEndian.Uint16(msg[0:2])
	flags := binary.BigEndian.Uint16(msg[2:4])
	if flags&0x8000 != 0 || binary.BigEndian.Uint16(msg[4:6]) != 1 {
		return nil // a response, or not exactly one question
	}
	qname, end, ok := readName(msg, 12)
	if !ok || end+4 > len(msg) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(msg[end : end+2])
	if qtype != dnsTypeA && qtype != dnsTypeAny {
		return nil
	}
	label := strings.ToLower(qname)
	if !llmnr {
		if !strings.HasSuffix(label, ".local") {
			return nil
		}
		label = strings.TrimSuffix(label, ".local")
	}
	if strings.Contains(label, ".") {
		return nil
	}
	addr, found := names[label]
	if !found || !addr.Is4() {
		return nil
	}

	// Reply shape. LLMNR and legacy-unicast mDNS (source port not 5353) get a
	// unicast reply that echoes the id and the question; a normal mDNS query
	// gets a multicast reply, as the protocol expects.
	question := msg[12 : end+4]
	unicast := llmnr || srcPort != mdnsPort
	var dns []byte
	hdr := make([]byte, 12)
	if unicast {
		binary.BigEndian.PutUint16(hdr[0:2], id)
		binary.BigEndian.PutUint16(hdr[4:6], 1)
	}
	if llmnr {
		binary.BigEndian.PutUint16(hdr[2:4], 0x8000) // QR
	} else {
		binary.BigEndian.PutUint16(hdr[2:4], 0x8400) // QR + AA
	}
	binary.BigEndian.PutUint16(hdr[6:8], 1) // one answer
	dns = append(dns, hdr...)
	nameRef := []byte{0xc0, 12}
	if unicast {
		dns = append(dns, question...)
	} else {
		nameRef = encodeName(qname)
	}
	dns = append(dns, nameRef...)
	ans := make([]byte, 10)
	binary.BigEndian.PutUint16(ans[0:2], dnsTypeA)
	class := uint16(dnsClassIN)
	if !llmnr {
		class |= 0x8000 // mDNS cache-flush
	}
	binary.BigEndian.PutUint16(ans[2:4], class)
	binary.BigEndian.PutUint32(ans[4:8], nameTTL)
	binary.BigEndian.PutUint16(ans[8:10], 4)
	dns = append(dns, ans...)
	a4 := addr.As4()
	dns = append(dns, a4[:]...)

	dst, dport := mdnsGroup, mdnsPort
	if unicast {
		dst, dport = h.Src, srcPort
	}
	sport := mdnsPort
	if llmnr {
		sport = llmnrPort
	}
	return buildUDP(addr, dst, sport, dport, dns, 255)
}

// readName decodes an uncompressed DNS name at off (queries never compress).
func readName(msg []byte, off int) (string, int, bool) {
	var parts []string
	for i := 0; i < 128; i++ {
		if off >= len(msg) {
			return "", 0, false
		}
		n := int(msg[off])
		off++
		if n == 0 {
			return strings.Join(parts, "."), off, true
		}
		if n&0xc0 != 0 || off+n > len(msg) {
			return "", 0, false
		}
		parts = append(parts, string(msg[off:off+n]))
		off += n
	}
	return "", 0, false
}

func encodeName(name string) []byte {
	var out []byte
	for _, p := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if p == "" || len(p) > 63 {
			continue
		}
		out = append(out, byte(len(p)))
		out = append(out, p...)
	}
	return append(out, 0)
}

// buildUDP assembles an IPv4/UDP packet with valid checksums.
func buildUDP(src, dst netip.Addr, sport, dport int, payload []byte, ttl uint8) []byte {
	total := MinHeaderLen + 8 + len(payload)
	b := make([]byte, total)
	b[0] = 0x45
	binary.BigEndian.PutUint16(b[2:4], uint16(total))
	b[8] = ttl
	b[9] = ProtocolUDP
	s, d := src.As4(), dst.As4()
	copy(b[12:16], s[:])
	copy(b[16:20], d[:])
	binary.BigEndian.PutUint16(b[10:12], headerChecksum(b[:MinHeaderLen]))
	u := b[MinHeaderLen:]
	binary.BigEndian.PutUint16(u[0:2], uint16(sport))
	binary.BigEndian.PutUint16(u[2:4], uint16(dport))
	binary.BigEndian.PutUint16(u[4:6], uint16(8+len(payload)))
	copy(u[8:], payload)
	// UDP checksum over the pseudo-header.
	var sum uint32
	for i := 0; i < 4; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(s[i : i+2]))
		sum += uint32(binary.BigEndian.Uint16(d[i : i+2]))
	}
	sum += uint32(ProtocolUDP) + uint32(len(u))
	for i := 0; i+1 < len(u); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(u[i : i+2]))
	}
	if len(u)%2 == 1 {
		sum += uint32(u[len(u)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	cs := ^uint16(sum)
	if cs == 0 {
		cs = 0xffff
	}
	binary.BigEndian.PutUint16(u[6:8], cs)
	return b
}
