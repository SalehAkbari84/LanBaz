package webrtc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// compactSDP removes what a data-channel-only session never uses, so the
// pairing code - which carries the SDP and has to be pasted into a chat - stays
// short.
//
//   - Component-2 candidates. They exist for RTCP on media sessions; a data
//     channel is a single component, and Pion lists every candidate twice.
//   - The " ufrag <x>" and " generation 0" candidate extensions, which repeat
//     a value already in a=ice-ufrag on every line.
//   - Exact duplicate candidate lines.
//   - Session attributes that only matter for media streams
//     (a=msid-semantic, a=extmap-allow-mixed) and the random session id and
//     version in o=, which nothing on the receiving side checks.
//
// Nothing here changes what the remote side can connect to.
func compactSDP(sdp string) string {
	lines := strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "a=msid-semantic"), strings.HasPrefix(line, "a=extmap-allow-mixed"):
			continue
		case strings.HasPrefix(line, "o="):
			line = "o=- 0 0 IN IP4 0.0.0.0"
		}
		if strings.HasPrefix(line, "a=candidate:") {
			f := strings.Fields(line)
			// a=candidate:<foundation> <component> <transport> ...
			if len(f) > 1 && f[1] != "1" {
				continue
			}
			line = stripCandidateExtension(line, "ufrag")
			line = stripCandidateExtension(line, "generation")
			if seen[line] {
				continue
			}
			seen[line] = true
		}
		out = append(out, line)
	}
	return strings.Join(out, "\r\n")
}

// stripCandidateExtension removes " <key> <value>" from a candidate line.
func stripCandidateExtension(line, key string) string {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == key && i >= 8 { // past the mandatory candidate fields
			return strings.Join(append(f[:i:i], f[i+2:]...), " ")
		}
	}
	return line
}

// Packed SDP.
//
// A pairing code is carried by hand, so its size matters. Everything in a
// data-channel SDP except a handful of values is the same on every machine,
// so the code carries only those values in binary and the receiver rebuilds
// the SDP from a template:
//
//	0xB1, setup, len(ufrag), ufrag, len(pwd), pwd, 32-byte SHA-256 fingerprint,
//	count, then per IPv4 candidate: type, 4-byte address, 2-byte port.
//
// About 120 bytes instead of about 630. Text SDP still decodes (it starts with
// 'v'), so codes from older versions keep working.
const packedMagic = 0xB1

var candTypes = []string{"host", "srflx", "prflx", "relay"}

// packSDP returns the packed form, or ok=false when the SDP has something the
// format cannot express (then the caller keeps the text form).
func packSDP(sdp string) ([]byte, bool) {
	var ufrag, pwd, setup string
	var fp []byte
	type cand struct {
		typ  byte
		ip   [4]byte
		port uint16
	}
	var cands []cand
	for _, line := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "a=ice-ufrag:"):
			ufrag = strings.TrimPrefix(line, "a=ice-ufrag:")
		case strings.HasPrefix(line, "a=ice-pwd:"):
			pwd = strings.TrimPrefix(line, "a=ice-pwd:")
		case strings.HasPrefix(line, "a=setup:"):
			setup = strings.TrimPrefix(line, "a=setup:")
		case strings.HasPrefix(line, "a=fingerprint:sha-256 "):
			raw, err := hex.DecodeString(strings.ReplaceAll(strings.TrimPrefix(line, "a=fingerprint:sha-256 "), ":", ""))
			if err != nil || len(raw) != 32 {
				return nil, false
			}
			fp = raw
		case strings.HasPrefix(line, "a=candidate:"):
			f := strings.Fields(line)
			// a=candidate:<f> <comp> <proto> <prio> <ip> <port> typ <type>
			if len(f) < 8 || f[1] != "1" || !strings.EqualFold(f[2], "udp") {
				continue
			}
			ip := net.ParseIP(f[4]).To4()
			port, err := strconv.Atoi(f[5])
			if ip == nil || err != nil || port <= 0 || port > 65535 {
				continue // IPv6 or mDNS: not carried
			}
			t := -1
			for i, name := range candTypes {
				if f[7] == name {
					t = i
				}
			}
			if t < 0 || len(cands) == 255 {
				continue
			}
			var c cand
			c.typ, c.port = byte(t), uint16(port)
			copy(c.ip[:], ip)
			cands = append(cands, c)
		}
	}
	setups := map[string]byte{"actpass": 0, "active": 1, "passive": 2}
	s, okSetup := setups[setup]
	if !okSetup || fp == nil || ufrag == "" || pwd == "" || len(ufrag) > 255 || len(pwd) > 255 {
		return nil, false
	}
	out := []byte{packedMagic, s, byte(len(ufrag))}
	out = append(out, ufrag...)
	out = append(out, byte(len(pwd)))
	out = append(out, pwd...)
	out = append(out, fp...)
	out = append(out, byte(len(cands)))
	for _, c := range cands {
		out = append(out, c.typ, c.ip[0], c.ip[1], c.ip[2], c.ip[3], byte(c.port>>8), byte(c.port))
	}
	return out, true
}

// isPacked reports whether a description is in packed form.
func isPacked(b []byte) bool { return len(b) > 0 && b[0] == packedMagic }

// unpackSDP rebuilds a full SDP from the packed form.
func unpackSDP(b []byte) (string, error) {
	bad := errors.New("webrtc: the connection data in the code is damaged")
	if !isPacked(b) || len(b) < 3 {
		return "", bad
	}
	setup := [...]string{"actpass", "active", "passive"}
	if int(b[1]) >= len(setup) {
		return "", bad
	}
	i := 2
	str := func() (string, bool) {
		if i >= len(b) {
			return "", false
		}
		n := int(b[i])
		i++
		if i+n > len(b) {
			return "", false
		}
		s := string(b[i : i+n])
		i += n
		return s, true
	}
	ufrag, ok1 := str()
	pwd, ok2 := str()
	if !ok1 || !ok2 || !iceToken(ufrag) || !iceToken(pwd) || i+33 > len(b) {
		return "", bad
	}
	fp := b[i : i+32]
	i += 32
	n := int(b[i])
	i++
	if i+7*n != len(b) {
		return "", bad
	}
	hexfp := make([]string, 32)
	for k, x := range fp {
		hexfp[k] = strings.ToUpper(hex.EncodeToString([]byte{x}))
	}
	var sb strings.Builder
	sb.WriteString("v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n")
	sb.WriteString("a=fingerprint:sha-256 " + strings.Join(hexfp, ":") + "\r\n")
	sb.WriteString("a=group:BUNDLE 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\n")
	sb.WriteString("a=setup:" + setup[b[1]] + "\r\na=mid:0\r\na=sendrecv\r\na=sctp-port:5000\r\n")
	sb.WriteString(fmt.Sprintf("a=max-message-size:%d\r\n", maxControlMessage))
	sb.WriteString("a=ice-ufrag:" + ufrag + "\r\na=ice-pwd:" + pwd + "\r\n")
	base := map[byte]uint32{0: 2130706431, 1: 1694498815, 2: 1862270975, 3: 16777215}
	for k := 0; k < n; k++ {
		c := b[i : i+7]
		i += 7
		if int(c[0]) >= len(candTypes) {
			return "", bad
		}
		ip := net.IPv4(c[1], c[2], c[3], c[4]).String()
		port := int(c[5])<<8 | int(c[6])
		line := fmt.Sprintf("a=candidate:%d 1 udp %d %s %d typ %s", k+1, base[c[0]]-uint32(k), ip, port, candTypes[c[0]])
		if c[0] != 0 {
			line += " raddr 0.0.0.0 rport 0"
		}
		sb.WriteString(line + "\r\n")
	}
	sb.WriteString("a=end-of-candidates\r\n")
	return sb.String(), nil
}

// iceToken checks ufrag/pwd characters (RFC 8445 ice-char).
func iceToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '/') {
			return false
		}
	}
	return true
}

// encodeDescription is what goes into a pairing code.
func encodeDescription(sdp string) []byte {
	if p, ok := packSDP(sdp); ok {
		return p
	}
	return []byte(compactSDP(sdp))
}

// decodeDescription accepts both the packed and the text form.
func decodeDescription(data []byte) (string, error) {
	if isPacked(data) {
		return unpackSDP(data)
	}
	return string(data), nil
}
