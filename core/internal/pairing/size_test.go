package pairing

import (
	"strings"
	"testing"
	"time"
)

// TestPairingCodeCarriesRealisticSignalling measures what a code actually costs
// with a real SDP-sized payload, rather than the toy payload the other tests use.
//
// This number is a product constraint, not an implementation detail: LanBaz has
// no server, so the WebRTC handshake has to travel inside the code, and the size
// of the code is therefore the size of the join UX. It is measured here so a
// future change to the encoder or to candidate gathering notices the effect on
// it instead of discovering it from a user complaint.
func TestPairingCodeCarriesRealisticSignalling(t *testing.T) {
	// A gathered Pion SDP with three candidates runs to roughly 2 KiB. This is
	// that shape rather than the literal bytes, since embedding a real SDP would
	// make this test fail for unrelated formatting reasons.
	var sdp strings.Builder
	sdp.WriteString("v=0\r\no=- 4611731400430051336 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n")
	sdp.WriteString("a=group:BUNDLE 0 1\r\na=extmap-allow-mixed\r\na=msid-semantic: WMS\r\n")
	for i := 0; i < 3; i++ {
		sdp.WriteString("a=candidate:1 1 udp 2130706431 192.168.1.10 ")
		sdp.WriteString("51999 typ host\r\n")
		sdp.WriteString("a=candidate:2 1 udp 1694498815 203.0.113.7 ")
		sdp.WriteString("52000 typ srflx raddr 192.168.1.10 rport 51999\r\n")
		sdp.WriteString("a=end-of-candidates\r\n")
	}
	sdp.WriteString("a=ice-ufrag:8Yk2\r\na=ice-pwd:2/1muCWoOi3uLifh0NuRHlZ6\r\n")
	sdp.WriteString("a=fingerprint:sha-256 8D:6C:1B:9A:37:5F:9F:1E:4C:0E:AA:1E:63:0F:2E:0A\r\n")
	sdp.WriteString("a=setup:actpass\r\na=mid:0\r\na=sctp-port:5000\r\na=max-message-size:262144\r\n")
	raw := sdp.String()

	code, err := New("lbzroom-test", "host", "cHVibGlja2V5", "deadbeefdeadbeef", raw, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	encoded, err := code.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Round trip must work at this size or the measurement is meaningless.
	back, err := Decode(encoded)
	if err != nil {
		t.Fatalf("a code of this size failed to decode: %v", err)
	}
	if err := back.Verify(); err != nil {
		t.Fatalf("a code of this size failed to verify: %v", err)
	}
	if back.Signal != raw {
		t.Error("the signalling payload did not survive the round trip")
	}

	t.Logf("raw sdp %d bytes -> code %d characters (%d groups)", len(raw), len(encoded), len(encoded)/GroupSize)
	t.Logf("compression ratio %.2f", float64(len(encoded))/float64(len(raw)))

	// The budget below is a UX constraint rather than a correctness one. It is
	// generous on purpose: the intended transfer is a clipboard or a URI, and
	// the limit exists to catch an encoder regression that doubles the size, not
	// to police the product decision.
	const budget = 4096
	if len(encoded) > budget {
		t.Errorf("the code is %d characters, over the %d character budget", len(encoded), budget)
	}
}

// The decoder must survive everything a user can do to a code by hand, at any
// size, because at this length somebody will.
func TestDecodeToleratesReformatting(t *testing.T) {
	code, err := New("lbzroom-test", "host", "cHVibGlja2V5", "deadbeefdeadbeef",
		strings.Repeat("v=0\r\n", 40), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := code.Encode()
	if err != nil {
		t.Fatal(err)
	}

	bare := strings.ReplaceAll(encoded, "-", "")
	uri := "lanbaz://join/" + encoded
	withQuery := uri + "?from=chat"
	spaced := strings.ReplaceAll(encoded, "-", " - ")
	lower := strings.ToLower(encoded)

	for name, input := range map[string]string{
		"canonical":  encoded,
		"no dashes":  bare,
		"url":        uri,
		"url+query":  withQuery,
		"spaced":     spaced,
		"lowercase":  lower,
		"prefix run": strings.Replace(encoded, "LBZ-", "LBZ", 1),
	} {
		got, err := Decode(input)
		if err != nil {
			t.Errorf("%s: Decode failed: %v", name, err)
			continue
		}
		if got.RoomID != code.RoomID {
			t.Errorf("%s: room id = %q, want %q", name, got.RoomID, code.RoomID)
		}
		if err := got.Verify(); err != nil {
			t.Errorf("%s: Verify failed: %v", name, err)
		}
	}
}

// A code that is altered by a single character must fail verification, which is
// the whole integrity story. It must never decode into a valid but different
// payload.
func TestSingleCharacterChangeBreaksTheCode(t *testing.T) {
	code, err := New("lbzroom-test", "host", "cHVibGlja2V5", "deadbeefdeadbeef",
		strings.Repeat("a=candidate:1 1 udp 1 1.2.3.4 5 typ host\r\n", 5),
		time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := code.Encode()
	if err != nil {
		t.Fatal(err)
	}

	// Flip one character deep inside the payload, leaving the prefix and the
	// surrounding structure untouched.
	mid := len(encoded) / 2
	runes := []rune(encoded)
	if runes[mid] == 'a' {
		runes[mid] = 'b'
	} else {
		runes[mid] = 'a'
	}
	tampered := string(runes)

	got, err := Decode(tampered)
	if err != nil {
		// Failing to decode is an acceptable outcome: base32 caught it.
		return
	}
	if err := got.Verify(); err == nil {
		t.Fatal("a code altered by one character still verified")
	}
}

// The GuestID field carries the reserved transport id across both codes. It must
// be covered by the signature, or a third party could point an answer at a link
// that is not theirs.
func TestGuestIDIsSigned(t *testing.T) {
	code, err := New("lbzroom-test", "host", "cHVibGlja2V5", "deadbeefdeadbeef",
		"v=0\r\n", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if code.GuestID != "" {
		t.Errorf("a fresh offer carries guest id %q, want empty", code.GuestID)
	}
	code.GuestID = "abc123"
	if err := code.Sign(); err != nil {
		t.Fatal(err)
	}
	encoded, err := code.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if back.GuestID != "abc123" {
		t.Errorf("guest id = %q after a round trip, want abc123", back.GuestID)
	}
	// Changing it afterwards must invalidate the code.
	back.GuestID = "zzz999"
	if err := back.Verify(); err == nil {
		t.Error("changing the guest id did not invalidate the signature")
	}
}
