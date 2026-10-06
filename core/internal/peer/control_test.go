package peer

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lanbaz/lanbaz/core/internal/identity"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

func TestDecodeControlRejectsUnknownTypes(t *testing.T) {
	if _, err := decodeControl([]byte(`{"t":"exec","cmd":"rm -rf /"}`)); err == nil {
		t.Fatal("an unknown control frame type was accepted")
	}
	if _, err := decodeControl(nil); err == nil {
		t.Fatal("an empty control frame was accepted")
	}
	if _, err := decodeControl([]byte("not json")); err == nil {
		t.Fatal("a malformed control frame was accepted")
	}
}

func TestDecodeControlBoundsOversizedFrames(t *testing.T) {
	huge := make([]byte, MaxFrameBytes+1)
	for i := range huge {
		huge[i] = 'a'
	}
	if _, err := decodeControl(huge); err == nil {
		t.Fatal("an oversized control frame was accepted")
	}
}

// Unknown fields are ignored so that two builds with slightly different frame
// formats can still talk; the routing field must be exact, everything else need
// not be.
func TestDecodeControlIgnoresUnknownFields(t *testing.T) {
	raw := []byte(`{"t":"ping","i":7,"future_field":{"a":1}}`)
	f, err := decodeControl(raw)
	if err != nil {
		t.Fatalf("a frame with an unknown field was rejected: %v", err)
	}
	if f.Type != MsgPing || f.Seq != 7 {
		t.Errorf("frame = %+v, want a ping with sequence 7", f)
	}
}

func TestEncodeRefusesOversizedFrames(t *testing.T) {
	f := controlFrame{Type: MsgHello, DisplayName: strings.Repeat("x", MaxFrameBytes)}
	if _, err := f.encode(); err == nil {
		t.Fatal("an oversized frame was encoded")
	}
}

// The identity check is the whole point of the hello exchange: it is what stops
// a peer that reached the room from presenting somebody else's id.

func TestParseHelloAcceptsAMatchingIdentity(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = priv
	id := identity.DeriveID(pub)
	f := controlFrame{
		Type:        MsgHello,
		PeerID:      string(id),
		PublicKey:   base64.RawURLEncoding.EncodeToString(pub),
		Fingerprint: identity.Fingerprint(pub),
	}
	got, gotPub, err := f.parseHello()
	if err != nil {
		t.Fatalf("a valid hello was rejected: %v", err)
	}
	if string(got) != string(id) {
		t.Errorf("parsed id = %s, want %s", got, id)
	}
	if len(gotPub) != identity.PublicKeySize {
		t.Errorf("parsed key is %d bytes, want %d", len(gotPub), identity.PublicKeySize)
	}
}

func TestParseHelloRejectsAStolenPeerID(t *testing.T) {
	victim, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	attacker, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := controlFrame{
		Type:      MsgHello,
		PeerID:    string(identity.DeriveID(victim)),
		PublicKey: base64.RawURLEncoding.EncodeToString(attacker),
	}
	_, _, err = f.parseHello()
	if err == nil {
		t.Fatal("a peer claimed an id its key does not derive")
	}
	var apiErr *protocol.Error
	if !asProtocolError(err, &apiErr) || !apiErr.HasCode(protocol.CodePeerSpoofed) {
		t.Errorf("error = %v, want a PEER_SPOOFED protocol error", err)
	}
}

func TestParseHelloRejectsAMismatchedFingerprint(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := controlFrame{
		Type:        MsgHello,
		PeerID:      string(identity.DeriveID(pub)),
		PublicKey:   base64.RawURLEncoding.EncodeToString(pub),
		Fingerprint: "0000000000000000",
	}
	if _, _, err := f.parseHello(); err == nil {
		t.Fatal("a hello whose fingerprint does not match its key was accepted")
	}
}

func TestParseHelloRejectsMalformedIdentities(t *testing.T) {
	cases := map[string]controlFrame{
		"empty id":       {Type: MsgHello, PublicKey: ""},
		"short id":       {Type: MsgHello, PeerID: "abcd", PublicKey: ""},
		"uppercase id":   {Type: MsgHello, PeerID: strings.ToUpper(string(identity.DeriveID(make([]byte, 32)))), PublicKey: ""},
		"bad base64 key": {Type: MsgHello, PeerID: string(identity.DeriveID(make([]byte, 32))), PublicKey: "!!!not base64!!!"},
		"short key":      {Type: MsgHello, PeerID: string(identity.DeriveID(make([]byte, 32))), PublicKey: base64.RawURLEncoding.EncodeToString([]byte("short"))},
	}
	for name, f := range cases {
		if _, _, err := f.parseHello(); err == nil {
			t.Errorf("%s: the frame was accepted", name)
		}
	}
}

func TestCleanDisplayNameStripsControlCharacters(t *testing.T) {
	if got := cleanDisplayName("  gamer\x00\n\tname  "); got != "gamername" {
		t.Errorf("cleanDisplayName = %q, want %q", got, "gamername")
	}
	if got := cleanDisplayName(""); got != "" {
		t.Errorf("cleanDisplayName(\"\") = %q, want empty", got)
	}
	if got := cleanDisplayName("\x01\x02\x03"); got != "" {
		t.Errorf("cleanDisplayName of pure control characters = %q, want empty", got)
	}
}

func TestCleanDisplayNameBoundsLength(t *testing.T) {
	long := strings.Repeat("ü", MaxDisplayNameLen*3)
	got := []rune(cleanDisplayName(long))
	if len(got) > MaxDisplayNameLen {
		t.Errorf("cleanDisplayName produced %d runes, want at most %d", len(got), MaxDisplayNameLen)
	}
	// It must not truncate a multi-byte rune in half.
	if strings.ContainsRune(cleanDisplayName(long), '\uFFFD') {
		t.Error("cleanDisplayName produced a replacement character, so it split a rune")
	}
}

// asProtocolError is a tiny errors.As specialised to *protocol.Error, kept local
// so the test file does not import errors just for one call.
func asProtocolError(err error, target **protocol.Error) bool {
	for err != nil {
		if pe, ok := err.(*protocol.Error); ok {
			*target = pe
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
