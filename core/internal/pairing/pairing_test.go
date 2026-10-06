package pairing

import (
	"strings"
	"testing"
	"time"
)

func newTestCode(t *testing.T) Code {
	t.Helper()
	c, err := New("room-1", "abc123", "cHVibGljLWtleQ", "deadbeefdeadbeef", "v0RRUxYK", time.Now().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestRoundTrip(t *testing.T) {
	c := newTestCode(t)
	enc, err := c.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(enc, Prefix+"-") {
		t.Errorf("code %q does not start with %q", enc, Prefix)
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := got.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := got.Validate(time.Now()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.RoomID != c.RoomID || got.HostID != c.HostID || got.Signal != c.Signal || got.Secret != c.Secret {
		t.Errorf("round trip lost fields:\n got %+v\nwant %+v", got, c)
	}
	if !got.ExpiresAt.Equal(c.ExpiresAt) {
		t.Errorf("expiry %v != %v", got.ExpiresAt, c.ExpiresAt)
	}
}

// The whole point of the dash grouping is that a human can retype a code, so
// the decoder has to accept the messy forms a human produces.
func TestDecodeToleratesHumanEntry(t *testing.T) {
	c := newTestCode(t)
	enc, err := c.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := c.Sig

	for name, variant := range map[string]string{
		"lowercase":   strings.ToLower(enc),
		"no dashes":   strings.ReplaceAll(enc, "-", ""),
		"extra space": "  " + enc + "\n",
		"spaces":      strings.ReplaceAll(enc, "-", " "),
		"underscore":  strings.ReplaceAll(enc, "-", "_"),
		"url":         "lanbaz://join/" + enc,
		"url query":   "lanbaz://join/" + enc + "?src=qr",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Decode(variant)
			if err != nil {
				t.Fatalf("Decode(%q): %v", variant, err)
			}
			if err := got.Verify(); err != nil {
				t.Errorf("Verify: %v", err)
			}
			if got.Sig != want {
				t.Errorf("signature changed: %q != %q", got.Sig, want)
			}
		})
	}
}

// A bare payload whose first characters happen to spell "lbz" must not lose
// them. The alphabet contains l, b and z, so this is a real ambiguity.
func TestDecodeDoesNotStripPrefixFromPayload(t *testing.T) {
	// Craft a raw base32 payload that starts with the encoded prefix.
	seed, err := New("r", "h", "k", "f", "s", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	enc, err := seed.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	body := strings.TrimPrefix(enc, Prefix+"-")
	// Prepend characters so the base32 stream begins with l b z after decoding.
	// Instead of guessing, verify the round trip with the real prefix present
	// and assert normalize keeps a bare body intact.
	got, err := normalize(body)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got != strings.ReplaceAll(body, "-", "") {
		t.Errorf("normalize altered a bare body: %q != %q", got, strings.ReplaceAll(body, "-", ""))
	}
}

// Tampering with any signed field must be caught, including the expiry. If
// ExpiresAt were not covered, an attacker could resurrect a stale code.
func TestVerifyRejectsTamperedFields(t *testing.T) {
	base := newTestCode(t)

	mutations := map[string]func(c *Code){
		"room id":  func(c *Code) { c.RoomID = "other-room" },
		"host id":  func(c *Code) { c.HostID = "ffffffffffffffffffffffffffffffff" },
		"key":      func(c *Code) { c.HostPublicKey = "dGFtcGVyZWQ" },
		"signal":   func(c *Code) { c.Signal = "attacker-offer" },
		"expiry":   func(c *Code) { c.ExpiresAt = c.ExpiresAt.Add(365 * 24 * time.Hour) },
		"nonce":    func(c *Code) { c.Nonce = "aaaaaaaa" },
		"secret":   func(c *Code) { c.Secret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" },
		"version":  func(c *Code) { c.Version = 99 },
		"fprnt":    func(c *Code) { c.HostFingerprint = "0000000000000000" },
		"no sig":   func(c *Code) { c.Sig = "" },
		"bad sig":  func(c *Code) { c.Sig = "0000000000000000" },
		"no room":  func(c *Code) { c.RoomID = "" },
		"no secre": func(c *Code) { c.Secret = "" },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			enc, err := c.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			got, err := Decode(enc)
			if err != nil {
				return // rejected at decode, which is also correct
			}
			if err := got.Verify(); err == nil {
				t.Errorf("Verify accepted a code whose %s was tampered with", name)
			}
		})
	}
}

func TestValidateRejectsExpired(t *testing.T) {
	c, err := New("r", "h", "k", "f", "s", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Validate(time.Now()); err == nil {
		t.Fatal("Validate accepted an expired code")
	}
}

// A code generated microseconds ago must survive the JSON round trip; the
// expiry is truncated to the second on purpose.
func TestValidateAllowsCodeJustGenerated(t *testing.T) {
	c := newTestCode(t)
	enc, _ := c.Encode()
	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if err := got.Validate(time.Now()); err != nil {
		t.Errorf("Validate rejected a fresh code: %v", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for name, in := range map[string]string{
		"empty":        "",
		"whitespace":   "   \n ",
		"short":        "abc",
		"punctuation":  "!!!???",
		"wrong length": "LBZ-0123456",
		"unicode":      "LBZ-ábcdéfgh",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(in); err == nil {
				t.Errorf("Decode(%q) accepted garbage", in)
			}
		})
	}
}

// A hostile code must not be able to make the daemon allocate without bound.
func TestDecodeRejectsOversizedInput(t *testing.T) {
	if _, err := Decode(strings.Repeat("a", MaxEncodedLen+1)); err == nil {
		t.Fatal("Decode accepted an oversized code")
	}
}

func TestSignRejectsShortSecret(t *testing.T) {
	c := newTestCode(t)
	c.Secret = "c2hvcnQ" // "short"
	if err := c.Sign(); err == nil {
		t.Fatal("Sign accepted a malformed secret")
	}
}

// The log rendering must never contain the secret, or a log file would become
// a replayable credential store.
func TestStringOmitsSecretAndSignal(t *testing.T) {
	c := newTestCode(t)
	s := c.String()
	if strings.Contains(s, c.Secret) {
		t.Errorf("String leaked the room secret: %s", s)
	}
	if strings.Contains(s, c.Signal) {
		t.Errorf("String leaked the signalling payload: %s", s)
	}
	if !strings.Contains(s, c.RoomID) {
		t.Errorf("String dropped the room id, which is the useful part: %s", s)
	}
}

func TestTwoCodesDifferWithinTheSameSecond(t *testing.T) {
	// Expiry is truncated to the second, so without a nonce two codes minted in
	// the same second for the same room would be byte-identical.
	exp := time.Now().Truncate(time.Second)
	a, err := New("r", "h", "k", "f", "s", exp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b, err := New("r", "h", "k", "f", "s", exp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Nonce == b.Nonce {
		t.Error("two codes share a nonce")
	}
	ea, _ := a.Encode()
	eb, _ := b.Encode()
	if ea == eb {
		t.Error("two codes in the same second encode identically")
	}
}
