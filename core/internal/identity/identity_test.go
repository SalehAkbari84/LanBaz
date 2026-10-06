package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateProducesValidIdentity(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !id.ID().IsValid() {
		t.Errorf("peer id %q is not a valid shape", id.ID())
	}
	if got := len(id.PublicKey()); got != PublicKeySize {
		t.Errorf("public key is %d bytes, want %d", got, PublicKeySize)
	}
	if got := len(id.PrivateKey()); got != KeySize {
		t.Errorf("private key is %d bytes, want %d", got, KeySize)
	}
}

func TestGenerateProducesDistinctIdentities(t *testing.T) {
	seen := make(map[PeerID]bool)
	for i := 0; i < 64; i++ {
		id, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if seen[id.ID()] {
			t.Fatalf("two identities share the peer id %s", id.ID())
		}
		seen[id.ID()] = true
	}
}

// The private key must be handed out as a copy. A caller that mutated the
// returned slice would otherwise corrupt the daemon's own identity.
func TestPrivateKeyIsACopy(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	first := id.PrivateKey()
	for i := range first {
		first[i] ^= 0xFF
	}
	if bytes.Equal(first, id.PrivateKey()) {
		t.Fatal("mutating the returned private key changed the identity")
	}
}

func TestPublicKeyIsACopy(t *testing.T) {
	id, _ := Generate()
	first := id.PublicKey()
	first[0] ^= 0xFF
	if bytes.Equal(first, id.PublicKey()) {
		t.Fatal("mutating the returned public key changed the identity")
	}
}

// The peer id is derived from the public key, which is what binds a name to a
// key: a peer cannot claim an id it cannot sign for.
func TestPeerIDIsStableAndKeyBound(t *testing.T) {
	id, _ := Generate()
	want := id.ID()
	if again := deriveID(id.priv.Public().(ed25519.PublicKey)); again != want {
		t.Errorf("deriveID is not stable: %s != %s", again, want)
	}
	other, _ := Generate()
	if other.ID() == want {
		t.Error("two identities share a peer id")
	}
}

func TestSignAndVerify(t *testing.T) {
	id, _ := Generate()
	msg := []byte("lanbaz room bootstrap")
	sig := id.Sign(msg)
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	if err := Verify(id.PublicKey(), msg, sig); err != nil {
		t.Errorf("Verify rejected a valid signature: %v", err)
	}
	// A signature must not transfer to a different message.
	if err := Verify(id.PublicKey(), []byte("tampered"), sig); err == nil {
		t.Error("Verify accepted a signature over a different message")
	}
	// ...nor to a different key.
	other, _ := Generate()
	if err := Verify(other.PublicKey(), msg, sig); err == nil {
		t.Error("Verify accepted a signature under the wrong key")
	}
}

func TestVerifyRejectsWrongSizedKey(t *testing.T) {
	id, _ := Generate()
	sig := id.Sign([]byte("m"))
	if err := Verify([]byte{1, 2, 3}, []byte("m"), sig); err == nil {
		t.Fatal("Verify accepted a 3-byte public key")
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	if a.Fingerprint() != a.Fingerprint() {
		t.Error("fingerprint is not stable")
	}
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("two identities share a fingerprint")
	}
	if len(a.Fingerprint()) != FingerprintSize {
		t.Errorf("fingerprint is %d characters, want %d", len(a.Fingerprint()), FingerprintSize)
	}
	// A fingerprint is displayed, so it must be plain hex.
	if strings.ContainsAny(a.Fingerprint(), "-: ") {
		t.Errorf("fingerprint %q contains separators", a.Fingerprint())
	}
}

func TestLoadOrCreatePersistsIdentity(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate (second): %v", err)
	}
	if first.ID() != second.ID() {
		t.Errorf("identity changed across reload: %s != %s", first.ID(), second.ID())
	}
	if _, err := os.Stat(Path(dir)); err != nil {
		t.Errorf("identity file was not persisted: %v", err)
	}
}

// The key file is the one place the long-lived secret lives; it must not be
// readable by other accounts.
func TestIdentityFileIsOwnerOnly(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("permission bits are not meaningful on windows; TestIdentityFileACLIsRestricted asserts the DACL there")
	}
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("identity file mode is %v, want no group or other access", perm)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("wrong size", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("too short"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Error("Load accepted a truncated key")
		}
	})

	t.Run("inconsistent public half", func(t *testing.T) {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		corrupt := append([]byte(nil), priv...)
		// Flip a bit in the public half only. A naive loader would accept this
		// and then fail to authenticate anything at all.
		corrupt[ed25519.SeedSize] ^= 0xFF
		if err := os.WriteFile(path, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Error("Load accepted a key with a mismatched public half")
		}
	})

	t.Run("missing", func(t *testing.T) {
		if _, err := Load(filepath.Join(dir, "absent.key")); err == nil {
			t.Error("Load accepted a missing file")
		}
	})
}

func TestWriteFileRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	first, _ := Generate()
	path := Path(dir)
	if err := WriteFile(path, first); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A second exclusive create must fail rather than replace the key, so two
	// daemons starting together cannot silently diverge.
	if err := WriteFile(path, first); err == nil {
		t.Fatal("WriteFile overwrote an existing identity")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID() != first.ID() {
		t.Error("the stored identity changed")
	}
}

func TestLoadOrCreateRejectsEmptyDir(t *testing.T) {
	if _, err := LoadOrCreate("  "); err == nil {
		t.Error("LoadOrCreate accepted a blank state directory")
	}
}
