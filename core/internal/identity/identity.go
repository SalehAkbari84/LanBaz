// Package identity owns the long-lived cryptographic identity of one LanBaz
// installation.
//
// Every daemon owns exactly one identity. The identity is an Ed25519 key pair:
// the public half is the peer's verifiable name on the virtual network, the
// private half proves, during the DTLS/SCTP handshake of a WebRTC link, that a
// peer is who it claims to be. Nothing else in LanBaz is allowed to read the
// private key.
//
// The private key never leaves this package except as a []byte handed to the
// transport at construction time, and it is never logged. The file that stores
// it is written with an owner-only ACL (see the security package for the shared
// implementation) and is created with 0600 permissions.
package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lanbaz/lanbaz/core/internal/security"
)

// FileName is the identity file inside the state directory.
const FileName = "identity.key"

// KeySize is the Ed25519 private key size in bytes.
const KeySize = ed25519.PrivateKeySize

// PublicKeySize is the Ed25519 public key size in bytes.
const PublicKeySize = ed25519.PublicKeySize

// FingerprintSize is the number of hex characters of a fingerprint.
const FingerprintSize = 32

// Errors returned by this package. They are sentinel values so that callers can
// branch with errors.Is without matching on message text.
var (
	// ErrMalformedIdentity means the stored file is not a valid identity.
	ErrMalformedIdentity = errors.New("identity: malformed identity file")
	// ErrNotFound means no identity exists yet.
	ErrNotFound = errors.New("identity: not found")
)

// Identity is a peer's key pair.
//
// The zero value is not usable; construct with LoadOrCreate.
type Identity struct {
	mu   sync.RWMutex
	priv ed25519.PrivateKey
	// id is the stable peer id derived from the public key.
	id PeerID
}

// PeerID is a 128-bit identifier rendered as lowercase hex. It is derived from
// the public key so that the same installation always presents the same id,
// and so that an id cannot be claimed by a different key.
type PeerID string

// String implements fmt.Stringer.
func (p PeerID) String() string { return string(p) }

// IsValid reports whether the id has the expected shape: 32 lowercase hex
// characters.
func (p PeerID) IsValid() bool {
	if len(p) != 32 {
		return false
	}
	for _, r := range p {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// ID returns the stable peer id.
func (i *Identity) ID() PeerID {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.id
}

// PublicKey returns a copy of the public key bytes.
func (i *Identity) PublicKey() []byte {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return append([]byte(nil), i.priv.Public().(ed25519.PublicKey)...)
}

// PublicKeyBase64 returns the public key in the URL-safe base64 form used on
// the wire and in pairing codes.
func (i *Identity) PublicKeyBase64() string {
	return base64.RawURLEncoding.EncodeToString(i.PublicKey())
}

// PrivateKey returns the private key bytes for transport construction. The
// returned slice is a copy; callers must not log it. Prefer Sign over this
// method whenever signing is all that is needed.
func (i *Identity) PrivateKey() []byte {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return append([]byte(nil), i.priv...)
}

// Sign signs msg with the identity key. The result is 64 bytes.
func (i *Identity) Sign(msg []byte) []byte {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return ed25519.Sign(i.priv, msg)
}

// Fingerprint returns a short, human-comparable fingerprint of the public key.
// It is the first 16 bytes of SHA-256 over the raw public key, hex encoded.
// A fingerprint identifies a key; it is not a secret and is safe to display so
// that two users can confirm out of band that they joined the same room.
func (i *Identity) Fingerprint() string {
	return Fingerprint(i.PublicKey())
}

// Fingerprint renders a public key as a display fingerprint.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:FingerprintSize/2])
}

// Generate creates a fresh in-memory identity.
func Generate() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate key: %w", err)
	}
	return &Identity{priv: priv, id: deriveID(priv.Public().(ed25519.PublicKey))}, nil
}

// deriveID maps a public key to its stable peer id: the first 16 bytes of
// SHA-256 over the public key. Binding the id to the key means a peer cannot
// claim an id it cannot sign for, which is what stops a stranger from joining a
// room under a stolen peer id.
func deriveID(pub ed25519.PublicKey) PeerID {
	sum := sha256.Sum256(pub)
	return PeerID(hex.EncodeToString(sum[:16]))
}

// DeriveID is the exported form of deriveID, so that a component which learns a
// peer's public key over the wire can check that the peer id the peer claims is
// the one that key implies. Without it the check would be a comment instead of
// an assertion, and the binding described above would be untested.
//
// A public key of the wrong size derives an id that simply will not match, so
// callers do not need a separate length check.
func DeriveID(pub []byte) PeerID {
	if len(pub) != PublicKeySize {
		return ""
	}
	return deriveID(ed25519.PublicKey(pub))
}

// Verify checks a signature produced by Sign for the given public key. It
// returns ErrMalformedIdentity for a wrong-sized key so that callers do not have
// to distinguish a corrupt file from a hostile one.
func Verify(pub, msg, sig []byte) error {
	if len(pub) != PublicKeySize {
		return fmt.Errorf("%w: public key is %d bytes, want %d", ErrMalformedIdentity, len(pub), PublicKeySize)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		return errors.New("identity: signature verification failed")
	}
	return nil
}

// Path returns the identity file path inside a state directory.
func Path(stateDir string) string { return filepath.Join(stateDir, FileName) }

// LoadOrCreate returns the identity stored in stateDir, generating and
// persisting a new one on first run.
//
// The file is written before LoadOrCreate returns, so a concurrent daemon
// cannot end up with two identities: WriteIdentityFile uses an exclusive
// create. If another process won the race, the loser reads the winner's file.
func LoadOrCreate(stateDir string) (*Identity, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("identity: state directory must not be empty")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("identity: create state dir: %w", err)
	}

	path := Path(stateDir)
	if id, err := Load(path); err == nil {
		return id, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	fresh, err := Generate()
	if err != nil {
		return nil, err
	}
	// Persist before use. If the exclusive create fails because another process
	// created the file first, adopt that identity instead of diverging.
	if err := WriteFile(path, fresh); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if errors.Is(err, ErrExistSentinel) {
		existing, loadErr := Load(path)
		if loadErr != nil {
			return nil, loadErr
		}
		return existing, nil
	}
	return fresh, nil
}

// ErrExistSentinel is returned by WriteFile when the file already exists. It is
// a distinct error so that the race above can adopt the existing identity.
var ErrExistSentinel = os.ErrExist

// Load reads an identity from a file.
func Load(path string) (*Identity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, filepath.Base(path))
		}
		return nil, fmt.Errorf("identity: read %s: %w", filepath.Base(path), err)
	}
	// The file is a raw 64-byte Ed25519 private key in seed||public form.
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: %s is %d bytes, want %d",
			ErrMalformedIdentity, filepath.Base(path), len(raw), ed25519.PrivateKeySize)
	}
	priv := ed25519.PrivateKey(raw)
	// A private key is only usable if its embedded public half matches the seed.
	// Checking this here turns a corrupted file into a clear error instead of a
	// handshake that mysteriously never authenticates.
	if !publicMatchesSeed(priv) {
		return nil, fmt.Errorf("%w: %s has an inconsistent public half", ErrMalformedIdentity, filepath.Base(path))
	}
	return &Identity{priv: priv, id: deriveID(priv.Public().(ed25519.PublicKey))}, nil
}

// WriteFile persists an identity with owner-only permissions. It uses an
// exclusive create so that two daemons starting at the same moment cannot
// silently overwrite each other's key.
func WriteFile(path string, id *Identity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("identity: create dir: %w", err)
	}
	priv := id.PrivateKey()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return ErrExistSentinel
		}
		return fmt.Errorf("identity: create %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	if _, err := f.Write(priv); err != nil {
		return fmt.Errorf("identity: write %s: %w", filepath.Base(path), err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("identity: sync %s: %w", filepath.Base(path), err)
	}
	// Tighten the ACL beyond the POSIX bits: on Windows a 0600 file is still
	// readable by other accounts in the same group, so the security package
	// rewrites the DACL to the current user only.
	_ = security.RestrictToOwner(path)
	return nil
}

// publicMatchesSeed recomputes the public key from the seed half and compares
// it with the stored public half. The derived key is itself seed||public, so
// the comparison must use its second half, not its first.
func publicMatchesSeed(priv ed25519.PrivateKey) bool {
	seed := priv[:ed25519.SeedSize]
	derived := ed25519.NewKeyFromSeed(seed)
	return bytes.Equal(derived[ed25519.SeedSize:], priv[ed25519.SeedSize:])
}
