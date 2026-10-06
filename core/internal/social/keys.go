// Package social is LanBaz's friend system: friend codes, friend requests,
// presence and join requests, carried as end-to-end encrypted Nostr messages
// over public relays. No LanBaz server exists; relays only see opaque
// gift-wrapped blobs addressed to a public key.
//
// Game traffic never goes through here. This package replaces the part of
// pairing that used to be copied by hand: the invite and reply codes travel
// as encrypted messages between friends instead.
package social

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbd-wtf/go-nostr"

	"github.com/lanbaz/lanbaz/core/internal/security"
)

// KeyFile is the Nostr key's file name in the state directory.
const KeyFile = "social.json"

// Keys is this installation's Nostr identity.
type Keys struct {
	Secret string `json:"secret"` // hex
	Public string `json:"public"` // hex, x-only
}

// LoadOrCreateKeys reads the key from the state directory or creates one.
func LoadOrCreateKeys(stateDir string) (Keys, error) {
	path := filepath.Join(stateDir, KeyFile)
	if raw, err := os.ReadFile(path); err == nil {
		var k Keys
		if err := json.Unmarshal(raw, &k); err != nil {
			return Keys{}, fmt.Errorf("social: %s is damaged: %w", KeyFile, err)
		}
		pub, err := nostr.GetPublicKey(k.Secret)
		if err != nil || pub != k.Public {
			return Keys{}, fmt.Errorf("social: %s does not hold a matching key pair", KeyFile)
		}
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Keys{}, err
	}
	sec := nostr.GeneratePrivateKey()
	pub, err := nostr.GetPublicKey(sec)
	if err != nil {
		return Keys{}, err
	}
	k := Keys{Secret: sec, Public: pub}
	raw, _ := json.Marshal(k)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return Keys{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Keys{}, err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return Keys{}, err
	}
	if err := f.Close(); err != nil {
		return Keys{}, err
	}
	_ = security.RestrictToOwner(path)
	return k, nil
}

var codeEnc = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// FriendCode is the short, shareable form of a public key: "LBZ-XXXXX-XXXXX",
// the first 50 bits of SHA-256(pubkey). A relay can be asked who claims a
// code, and the answer is only believed if it hashes back to the code, so
// squatting someone's code means finding a 50-bit hash preimage.
func FriendCode(pubHex string) string {
	raw, _ := hex.DecodeString(pubHex)
	sum := sha256.Sum256(raw)
	s := codeEnc.EncodeToString(sum[:7])[:10]
	return "LBZ-" + s[:5] + "-" + s[5:]
}

// NormalizeCode accepts what a person types - any case, with or without the
// prefix and dashes, with the usual look-alike letters - and returns the
// canonical form, or "" when it is not a friend code.
func NormalizeCode(in string) string {
	s := strings.ToUpper(strings.TrimSpace(in))
	s = strings.TrimPrefix(s, "LBZ")
	s = strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1", "U", "V").Replace(s)
	if len(s) != 10 {
		return ""
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			return ""
		}
	}
	return "LBZ-" + s[:5] + "-" + s[5:]
}

// validPub reports whether s is a 32-byte x-only public key in hex.
func validPub(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}
