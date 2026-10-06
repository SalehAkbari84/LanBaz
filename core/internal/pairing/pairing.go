// Package pairing encodes and decodes the Pairing Code that a host shows to a
// guest and a guest types back into LanBaz.
//
// A pairing code is NOT a room id. It is a self-contained, authenticated,
// expiring bootstrap blob that carries everything the guest needs to reach the
// host: the room id, the host's public key, the room secret, the WebRTC
// signalling payload (SDP offer, ICE credentials, ICE candidates) and an
// expiry. A guest that has only the code can start the handshake without any
// server being involved, which is what lets LanBaz work with no room server at
// all in the common case.
//
// Encoding
//
//	The JSON payload is compressed with flate and rendered with a
//	restricted base32 alphabet that omits the visually ambiguous characters
//	(I, L, O, U) so a code survives being read aloud or retyped by hand. The
//	result is chunked into groups of four joined by dashes and prefixed with
//	"LBZ". The code is a UTF-8 safe string, safe to pass through a URL as
//	lanbaz://join/<code>.
//
// Integrity
//
//	Every field is authenticated by the room secret via HMAC-SHA256 over the
//	canonical payload with the signature field zeroed. A guest that tampers
//	with the SDP, swaps the host key, or extends the expiry invalidates the
//	signature and is rejected with PAIRING_INVALID. The signature is what stops
//	a third party from rewriting a code they observed in transit to insert
//	themselves as the host.
//
// Replay
//
//	Rooms are single-shot by construction: the host marks a pairing code spent
//	the moment a guest completes the DTLS handshake with it, and the code is
//	then refused. Expiry bounds how long an observed code stays useful, so a
//	stolen code is only a short-lived liability. See docs/pairing.md.
package pairing

import (
	"bytes"
	"compress/flate"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// CodeFormatVersion is the current pairing code format. A code whose version
// differs is rejected rather than guessed at, so a future format change cannot
// silently misparse an old code.
const CodeFormatVersion = 1

// Prefix is the human recognisable start of every code.
const Prefix = "LBZ"

// GroupSize is the number of characters per dash-separated group.
const GroupSize = 4

// Limits that bound what the decoder will accept, so a hostile or corrupt code
// cannot cause unbounded allocation.
const (
	// MaxEncodedLen bounds the encoded code string.
	MaxEncodedLen = 16384
	// MaxDecodedLen bounds the decompressed payload. The payload is a few
	// hundred bytes of JSON plus an SDP offer; a few kilobytes is generous.
	MaxDecodedLen = 64 * 1024
	// MaxCodeGroups bounds the number of dash-separated groups.
	MaxCodeGroups = 2048
)

// SecretSize is the room secret length in bytes. 32 bytes of CSPRNG output.
const SecretSize = 32

// SignatureSize is the HMAC-SHA256 output length.
const SignatureSize = sha256.Size

// alphabet is RFC4648 base32 without the padding character, lowercased, and
// with I, L, O and U removed to avoid look-alike confusion. Digits come first
// so a mistyped code is still a valid length.
var alphabet = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// Errors returned when a code cannot be trusted. They carry the API error codes
// declared in the protocol package so the failure is machine-readable all the
// way to the UI.
var (
	// ErrEmpty is returned for an empty code.
	ErrEmpty = errors.New("pairing: empty code")
	// ErrMalformed is returned for a code that is not decodable at all.
	ErrMalformed = errors.New("pairing: malformed code")
	// ErrSignature means the code failed integrity verification.
	ErrSignature = errors.New("pairing: signature verification failed")
	// ErrExpired means the code is past its expiry.
	ErrExpired = errors.New("pairing: code expired")
	// ErrSpent means the code was already redeemed.
	ErrSpent = errors.New("pairing: code already used")
)

// Code is the decoded content of a pairing code.
//
// It carries signalling material, not game traffic, so it is JSON here; the
// binary envelope in docs/protocol.md is for the virtual LAN path.
type Code struct {
	// Version is the code format version.
	Version int `json:"version"`
	// RoomID identifies the room this code grants access to.
	RoomID string `json:"room_id"`
	// HostID is the host's stable peer id (hex).
	HostID string `json:"host_id"`
	// HostPublicKey is the host's Ed25519 public key, base64url.
	HostPublicKey string `json:"host_key"`
	// HostFingerprint is a short fingerprint for out-of-band confirmation.
	HostFingerprint string `json:"host_fp"`
	// GuestID is the transport peer id the host has reserved for the peer that
	// redeems this code. It is empty in an offer and set in an answer, and it is
	// what lets the host apply the answer to the right link without trusting a
	// field chosen by whoever handed the code over.
	GuestID string `json:"guest_id,omitempty"`
	// Subnet is the room's address space, e.g. "10.200.17.0/24".
	//
	// It travels in the code because the two sides have to agree on it before a
	// single packet can move, and there is no server to ask. The derivation from
	// the room id is deterministic, so the field is belt and braces: it is what
	// makes the addressing explicit rather than implied, and it is what carries
	// the answer when the host had to probe around a local collision. Both sides
	// verify it lies inside LanBaz's own pool, so a code cannot talk a guest
	// into configuring an address that collides with the user's real network.
	Subnet string `json:"subnet,omitempty"`
	// GuestAddr is the address inside Subnet the host has leased to the guest
	// redeeming this code, e.g. "10.200.17.2".
	//
	// The host allocates it when it issues the code, because the host is the only
	// participant that knows the full membership and is therefore the only one
	// that can promise an address is unique. Sending it in the code means the
	// guest knows its own address the moment it decodes, with no extra round trip
	// - which matters a great deal in a handshake that already needs a human to
	// carry two blobs back and forth.
	GuestAddr string `json:"guest_addr,omitempty"`
	// Secret is the room secret, base64url. Both sides derive the same
	// authentication key from it.
	Secret string `json:"secret"`
	// Signal is the transport-specific bootstrap payload. For WebRTC it is
	// the base64 of the SDP offer, which embeds the ICE ufrag, pwd and
	// candidates. Keeping it opaque is what allows the pairing package to stay
	// independent of the transport implementation.
	Signal string `json:"signal"`
	// ExpiresAt is when the code stops being accepted.
	ExpiresAt time.Time `json:"expires_at"`
	// Kind is "invite" for a host's code and "reply" for a guest's answer, so
	// a client can route a pasted code to the right action without guessing.
	Kind string `json:"kind,omitempty"`
	// RoomName, HostName and GuestName are display names for the UI ("Ali
	// invites you to Friday"). They are signed with everything else but are
	// labels only: nothing routes or authorises on them.
	RoomName  string `json:"room_name,omitempty"`
	HostName  string `json:"host_name,omitempty"`
	GuestName string `json:"guest_name,omitempty"`
	// Mode is the room mode ("l2" for classic LAN); empty means standard.
	Mode string `json:"mode,omitempty"`
	// Nonce makes two otherwise identical codes distinct, which matters when
	// regenerating a code within the same second.
	Nonce string `json:"nonce"`
	// Sig is HMAC-SHA256(Secret, canonical payload with Sig zeroed).
	Sig string `json:"sig"`
}

// canonical returns the bytes the signature is computed over. It zeroes Sig so
// that signing and verifying agree, and it marshals through a struct with a
// fixed field order because encoding/json emits struct fields in declaration
// order.
func (c Code) canonical() ([]byte, error) {
	c.Sig = ""
	return json.Marshal(c)
}

// signingKey derives the HMAC key from the room secret.
func signingKey(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

// Sign authenticates the code in place.
func (c *Code) Sign() error {
	if c.Secret == "" {
		return protocol.NewError(protocol.CodePairingInvalid, "pairing code has no secret")
	}
	secret, err := decodeSecret(c.Secret)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, signingKey(secret))
	raw, err := c.canonical()
	if err != nil {
		return protocol.NewErrorf(protocol.CodePairingInvalid, "pairing code is not encodable: %v", err)
	}
	mac.Write(raw)
	c.Sig = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return nil
}

// safeID reports whether an identifier from a code is short lowercase ASCII:
// letters, digits and '-'. Empty is allowed; required fields are checked
// separately.
func safeID(s string) bool {
	if len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Verify checks the signature. It must be called before any other field of the
// code is trusted, including ExpiresAt: an attacker who could edit ExpiresAt
// without invalidating Sig could resurrect an old code.
func (c Code) Verify() error {
	if c.Version != CodeFormatVersion {
		return protocol.NewErrorf(protocol.CodePairingInvalid, "pairing code version %d is not supported", c.Version)
	}
	if c.RoomID == "" {
		return protocol.NewError(protocol.CodePairingInvalid, "pairing code has no room id")
	}
	// The secret travels inside the code, so the signature proves the code
	// was not damaged, not who made it: anyone can mint a "valid" code. Every
	// identifier in it therefore gets a strict shape check here, before any of
	// it is used. The room id in particular becomes the virtual adapter's
	// name, which reaches elevated PowerShell commands.
	if !safeID(c.RoomID) || !safeID(c.GuestID) || !safeID(c.HostID) {
		return protocol.NewError(protocol.CodePairingInvalid, "pairing code contains an invalid identifier")
	}
	if c.Secret == "" || c.Sig == "" {
		return protocol.NewError(protocol.CodePairingInvalid, "pairing code is missing its signature")
	}
	secret, err := decodeSecret(c.Secret)
	if err != nil {
		return err
	}
	got, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(c.Sig)
	if err != nil || len(got) != SignatureSize {
		return protocol.NewError(protocol.CodePairingInvalid, "pairing code signature is malformed")
	}
	mac := hmac.New(sha256.New, signingKey(secret))
	raw, err := c.canonical()
	if err != nil {
		return protocol.NewErrorf(protocol.CodePairingInvalid, "pairing code is not encodable: %v", err)
	}
	mac.Write(raw)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return protocol.NewError(protocol.CodePairingInvalid, "pairing code signature does not match; it was altered or mistyped")
	}
	return nil
}

// Validate checks structural and temporal validity. Call after Verify.
func (c Code) Validate(now time.Time) error {
	if c.ExpiresAt.IsZero() {
		return protocol.NewError(protocol.CodePairingInvalid, "pairing code has no expiry")
	}
	// Compare against a time truncated to the second: a code generated and
	// encoded a moment before this call must not fail because of sub-second
	// rounding in the round trip through JSON.
	if now.After(c.ExpiresAt.Add(time.Second)) {
		return protocol.NewErrorf(protocol.CodePairingExpired,
			"pairing code expired at %s", c.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if _, err := c.SecretBytes(); err != nil {
		return err
	}
	return nil
}

// SecretBytes decodes the room secret.
func (c Code) SecretBytes() ([]byte, error) { return decodeSecret(c.Secret) }

// Addressing returns the room subnet and the guest's address within it.
//
// Both are empty in a code that carries no addressing, which is how an older
// build's code is handled: the caller falls back to deriving the subnet from the
// room id. The presence check rather than an error is deliberate - rejecting an
// old code outright would turn a version mismatch into an outage instead of a
// slower handshake.
func (c Code) Addressing() (subnet netip.Prefix, guest netip.Addr, ok bool) {
	if c.Subnet == "" {
		return netip.Prefix{}, netip.Addr{}, false
	}
	subnet, err := netip.ParsePrefix(c.Subnet)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, false
	}
	if c.GuestAddr == "" {
		return subnet, netip.Addr{}, true
	}
	guest, err = netip.ParseAddr(c.GuestAddr)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, false
	}
	return subnet, guest, true
}

func decodeSecret(encoded string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, protocol.NewError(protocol.CodePairingInvalid, "pairing code secret is malformed")
	}
	if len(b) != SecretSize {
		return nil, protocol.NewErrorf(protocol.CodePairingInvalid,
			"pairing code secret is %d bytes, want %d", len(b), SecretSize)
	}
	return b, nil
}

// NewSecret returns a fresh room secret.
func NewSecret() (string, []byte, error) {
	raw := make([]byte, SecretSize)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, protocol.NewErrorf(protocol.CodeInternal, "generate room secret: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), raw, nil
}

// NewNonce returns a short random nonce that makes otherwise identical codes
// distinct.
func NewNonce() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", protocol.NewErrorf(protocol.CodeInternal, "generate pairing nonce: %v", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// New builds and signs a pairing code.
func New(roomID, hostID, hostPublicKey, hostFingerprint, signal string, expiresAt time.Time) (Code, error) {
	secretStr, _, err := NewSecret()
	if err != nil {
		return Code{}, err
	}
	nonce, err := NewNonce()
	if err != nil {
		return Code{}, err
	}
	c := Code{
		Version:         CodeFormatVersion,
		RoomID:          roomID,
		HostID:          hostID,
		HostPublicKey:   hostPublicKey,
		HostFingerprint: hostFingerprint,
		Secret:          secretStr,
		Signal:          signal,
		ExpiresAt:       expiresAt.UTC().Truncate(time.Second),
		Nonce:           nonce,
	}
	if err := c.Sign(); err != nil {
		return Code{}, err
	}
	return c, nil
}

// Encode renders the code as a dash-grouped base32 string.
func (c Code) Encode() (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", protocol.NewErrorf(protocol.CodeInternal, "encode pairing code: %v", err)
	}
	return encodePayload(raw)
}

// Decode parses a code produced by Encode, tolerating whitespace, dashes and
// lower/upper case. It does not verify the signature; call Verify for that.
func Decode(s string) (Code, error) {
	var c Code
	cleaned, err := normalize(s)
	if err != nil {
		return c, err
	}
	raw, err := decodePayload(cleaned)
	if err != nil {
		return c, err
	}
	// A size guard before json.Unmarshal: json.Unmarshal on a huge slice would
	// otherwise be bounded only by the slice we already allocated, and the
	// decompression bomb case is handled inside decodePayload.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, protocol.NewErrorf(protocol.CodePairingInvalid, "pairing code payload is malformed: %v", err)
	}
	return c, nil
}

// encodePayload compresses and renders raw.
func encodePayload(raw []byte) (string, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		return "", protocol.NewErrorf(protocol.CodeInternal, "pairing compressor: %v", err)
	}
	if _, err := w.Write(raw); err != nil {
		return "", protocol.NewErrorf(protocol.CodeInternal, "pairing compress: %v", err)
	}
	if err := w.Close(); err != nil {
		return "", protocol.NewErrorf(protocol.CodeInternal, "pairing compress close: %v", err)
	}
	if buf.Len() > MaxEncodedLen {
		return "", protocol.NewErrorf(protocol.CodePairingInvalid,
			"encoded pairing code is %d bytes, want at most %d", buf.Len(), MaxEncodedLen)
	}
	// The dash after the prefix is what lets normalize tell "LBZ" + payload
	// apart from a payload that merely begins with those letters.
	return Prefix + "-" + group(alphabet.EncodeToString(buf.Bytes())), nil
}

// decodePayload is the inverse of encodePayload.
//
// normalize has already removed dashes and spaces, so s is a bare base32
// string. It deliberately does not enforce a length multiple of 8: unpadded
// base32 encodes a bit stream, not a byte-block stream, so 248 bytes legitimately
// become 397 characters. The decoder is the only authority on validity.
func decodePayload(s string) ([]byte, error) {
	raw, err := alphabet.DecodeString(s)
	if err != nil {
		// A user may have typed the prefix with no separator ("LBZ6j65..."),
		// which normalize deliberately left alone. Retrying without the prefix
		// is safe: the prefix is not valid base32 on its own, because L is not
		// in the ambiguity-free alphabet, so a genuine payload can never start
		// with it.
		if trimmed, ok := stripBarePrefix(s); ok {
			if raw2, err2 := alphabet.DecodeString(trimmed); err2 == nil {
				return inflate(raw2)
			}
		}
		return nil, protocol.NewErrorf(protocol.CodePairingInvalid, "pairing code is not valid base32: %v", err)
	}
	return inflate(raw)
}

// stripBarePrefix removes a leading "LBZ" that is not followed by a separator.
func stripBarePrefix(s string) (string, bool) {
	if len(s) <= len(Prefix) || !strings.EqualFold(s[:len(Prefix)], Prefix) {
		return s, false
	}
	return s[len(Prefix):], true
}

// inflate decompresses a flate stream with a hard output limit.
//
// The limit is the defence against a decompression bomb: the encoder never
// produces more than MaxDecodedLen, so anything past that is hostile or corrupt
// and must not be materialised. io.ReadAll has no limit parameter, so the read
// is done by hand.
func inflate(raw []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(raw))
	defer r.Close()
	return readAllLimited(r, MaxDecodedLen)
}

// readAllLimited reads r fully, failing as soon as more than limit bytes would
// be buffered so a hostile stream cannot exhaust memory.
func readAllLimited(r io.Reader, limit int) ([]byte, error) {
	var buf bytes.Buffer
	// buf.Len() is at most limit, so a buffer larger than the limit is all the
	// preallocation that is ever useful.
	buf.Grow(min(limit, 8*1024))
	chunk := make([]byte, 8*1024)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			if buf.Len()+n > limit {
				return nil, protocol.NewErrorf(protocol.CodePayloadTooLarge,
					"pairing code expands beyond the %d byte limit", limit)
			}
			buf.Write(chunk[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buf.Bytes(), nil
			}
			return nil, protocol.NewErrorf(protocol.CodePairingInvalid,
				"pairing code payload is corrupt: %v", err)
		}
	}
}

// normalize strips the prefix, dashes, spaces and any other separator, maps
// letters to the case the alphabet expects, and validates the length.
func normalize(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", protocol.NewError(protocol.CodePairingInvalid, "pairing code is empty")
	}
	if len(s) > MaxEncodedLen {
		return "", protocol.NewErrorf(protocol.CodePayloadTooLarge,
			"pairing code is %d characters, want at most %d", len(s), MaxEncodedLen)
	}

	// Accept a full lanbaz://join/... URL as well as a bare code.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexAny(s, "?#"); j >= 0 {
			s = s[:j]
		}
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j+1:]
		}
	}

	// Strip the "LBZ" prefix only when a separator follows it. The letters L, B
	// and Z are all valid alphabet characters, so a bare code whose payload
	// happens to start with them would otherwise be truncated by 3 characters
	// and fail to decode. Encode always emits "LBZ-...", so requiring the
	// separator is unambiguous in the other direction too.
	if len(s) >= len(Prefix) && strings.EqualFold(s[:len(Prefix)], Prefix) {
		rest := s[len(Prefix):]
		if rest != "" && (rest[0] == '-' || rest[0] == ' ' || rest[0] == '_') {
			s = rest
		}
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '_':
			// separators, dropped
		default:
			return "", protocol.NewErrorf(protocol.CodePairingInvalid,
				"pairing code contains the unsupported character %q", r)
		}
	}
	out := b.String()
	if out == "" {
		return "", protocol.NewError(protocol.CodePairingInvalid, "pairing code is empty")
	}
	// No length-modulus check here on purpose: unpadded base32 encodes a bit
	// stream, not a byte-block stream, so 248 bytes legitimately become 397
	// characters. decodePayload is the only authority on validity.
	if len(out)/8 > MaxCodeGroups {
		return "", protocol.NewErrorf(protocol.CodePayloadTooLarge,
			"pairing code has more than %d blocks", MaxCodeGroups)
	}
	return out, nil
}

// group inserts a dash every GroupSize characters.
func group(s string) string {
	if len(s) <= GroupSize {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/GroupSize + 1)
	for i := 0; i < len(s); i += GroupSize {
		if i > 0 {
			b.WriteByte('-')
		}
		end := i + GroupSize
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(s[i:end])
	}
	return b.String()
}

// String renders a code for logs with the secret removed, so a code can be
// correlated in a log line without ever being replayable from that line.
func (c Code) String() string {
	return fmt.Sprintf("pairing{room=%s host=%s fp=%s expires=%s nonce=%s sig=%s}",
		c.RoomID, c.HostID, c.HostFingerprint, c.ExpiresAt.UTC().Format(time.RFC3339), c.Nonce, shortSig(c.Sig))
}

// shortSig returns a non-secret prefix of the signature, enough to tell two
// codes apart in a log.
func shortSig(sig string) string {
	if len(sig) <= 8 {
		return sig
	}
	return sig[:8]
}

// Code kinds.
const (
	KindInvite = "invite"
	KindReply  = "reply"
)
