package webrtc

import (
	"strings"
	"testing"
)

func TestCompactSDPKeepsOneComponentAndNoExtensions(t *testing.T) {
	in := strings.Join([]string{
		"v=0",
		"a=ice-ufrag:abc",
		"a=candidate:1 1 udp 2130706431 192.168.1.4 49185 typ host ufrag abc generation 0",
		"a=candidate:1 2 udp 2130706431 192.168.1.4 49185 typ host ufrag abc generation 0",
		"a=candidate:2 1 udp 1694498815 5.238.1.148 64045 typ srflx raddr 192.168.1.4 rport 64045 ufrag abc generation 0",
		"a=candidate:2 1 udp 1694498815 5.238.1.148 64045 typ srflx raddr 192.168.1.4 rport 64045 ufrag abc generation 0",
		"",
	}, "\r\n")
	got := compactSDP(in)
	want := strings.Join([]string{
		"v=0",
		"a=ice-ufrag:abc",
		"a=candidate:1 1 udp 2130706431 192.168.1.4 49185 typ host",
		"a=candidate:2 1 udp 1694498815 5.238.1.148 64045 typ srflx raddr 192.168.1.4 rport 64045",
		"",
	}, "\r\n")
	if got != want {
		t.Fatalf("compactSDP:\n%q\nwant\n%q", got, want)
	}
}

func TestPackedSDPRoundTrip(t *testing.T) {
	sdp := "v=0\r\no=- 0 0 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n" +
		"a=fingerprint:sha-256 F4:2D:9A:04:36:93:65:E3:FE:62:FB:EB:87:4F:E6:5F:CA:B0:9F:D6:B5:4E:50:69:F8:DD:1A:40:D0:7B:BB:B4\r\n" +
		"a=group:BUNDLE 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\na=setup:actpass\r\na=mid:0\r\n" +
		"a=ice-ufrag:mMWCGjrEvbeGyTXF\r\na=ice-pwd:GOVbeuggKJqOuGWbZOxQLQleTnWlTyaI\r\n" +
		"a=candidate:1 1 udp 2130706431 192.168.1.4 61866 typ host\r\n" +
		"a=candidate:2 1 udp 1694498815 5.238.1.148 49964 typ srflx raddr 192.168.1.4 rport 49964\r\n" +
		"a=candidate:3 1 udp 2130706431 fe80::1 61867 typ host\r\na=end-of-candidates\r\n"
	packed, ok := packSDP(sdp)
	if !ok {
		t.Fatal("packSDP refused a normal SDP")
	}
	if len(packed) > 130 {
		t.Errorf("packed size %d, want about 100", len(packed))
	}
	out, err := unpackSDP(packed)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"a=ice-ufrag:mMWCGjrEvbeGyTXF", "a=ice-pwd:GOVbeuggKJqOuGWbZOxQLQleTnWlTyaI", "a=setup:actpass",
		"F4:2D:9A:04", "192.168.1.4 61866 typ host", "5.238.1.148 49964 typ srflx",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rebuilt SDP lacks %q", want)
		}
	}
	if strings.Contains(out, "fe80") {
		t.Error("an IPv6 candidate was carried")
	}
	for _, bad := range [][]byte{{packedMagic}, append(append([]byte(nil), packed...), 1), {packedMagic, 9, 0}} {
		if _, err := unpackSDP(bad); err == nil {
			t.Errorf("damaged packed data %x was accepted", bad)
		}
	}
}
