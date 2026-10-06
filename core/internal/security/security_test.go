package security

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateTokenIsUniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		tok, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		if len(tok) < 40 {
			t.Errorf("token %q is shorter than 40 characters; want >= 256 bits of entropy", tok)
		}
		for _, r := range tok {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				t.Fatalf("token %q contains the non url-safe character %q", tok, r)
			}
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestGenerateIDShape(t *testing.T) {
	id, err := GenerateID(16)
	if err != nil {
		t.Fatalf("GenerateID: %v", err)
	}
	if len(id) != 32 {
		t.Errorf("id %q has length %d, want 32", id, len(id))
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("id %q contains the non hex character %q", id, r)
		}
	}
	if _, err := GenerateID(0); err == nil {
		t.Error("GenerateID(0) must fail")
	}
	if _, err := GenerateID(-1); err == nil {
		t.Error("GenerateID(-1) must fail")
	}
}

func TestConstantTimeEqual(t *testing.T) {
	const tok = "abcdefghijklmnop"
	if !ConstantTimeEqual(tok, tok) {
		t.Error("identical tokens must match")
	}
	if ConstantTimeEqual(tok, tok+"x") {
		t.Error("tokens of different length must not match")
	}
	if ConstantTimeEqual("", tok) || ConstantTimeEqual(tok, "") || ConstantTimeEqual("", "") {
		t.Error("an empty token must never match")
	}
}

// ------------------------------------------------------------- state file ---

func TestWriteAndReadStateFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	st := NewState(4321, "127.0.0.1:51234", 51234, "the-token-value", "1.2.3", `C:\lanbazd.exe`)

	path, err := WriteStateFile(dir, st)
	if err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
	if filepath.Base(path) != StateFileName {
		t.Errorf("path = %q, want it to end in %s", path, StateFileName)
	}

	back, err := ReadStateFile(dir)
	if err != nil {
		t.Fatalf("ReadStateFile: %v", err)
	}
	if back.APIToken != "the-token-value" || back.APIPort != 51234 || back.PID != 4321 {
		t.Errorf("state = %+v, want the written values back", back)
	}
	if back.StateFileVersion != StateFileVersion {
		t.Errorf("version = %d, want %d", back.StateFileVersion, StateFileVersion)
	}
	if back.APIPath != "/api" {
		t.Errorf("api path = %q, want /api", back.APIPath)
	}
}

func TestStateFileIsOwnerOnly(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("permission bits are not meaningful on windows; the ACL is asserted in the windows build")
	}
	dir := t.TempDir()
	if _, err := WriteStateFile(dir, NewState(1, "127.0.0.1:1", 1, "tok", "v", "")); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file mode = %o, want 600", perm)
	}
}

func TestStateFileIsValidJSON(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteStateFile(dir, NewState(7, "127.0.0.1:9999", 9999, "tok-abcdefgh", "1.0", "")); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}
	if generic["api_token"] != "tok-abcdefgh" {
		t.Errorf("api_token = %v, want the written token", generic["api_token"])
	}
}

func TestWriteStateFileIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteStateFile(dir, NewState(1, "127.0.0.1:1", 1, "first-token", "v", "")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteStateFile(dir, NewState(2, "127.0.0.1:2", 2, "second-token", "v", "")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only %s", names, StateFileName)
	}
	back, err := ReadStateFile(dir)
	if err != nil {
		t.Fatalf("ReadStateFile: %v", err)
	}
	if back.APIToken != "second-token" {
		t.Errorf("token = %q, want the second write", back.APIToken)
	}
}

func TestReadStateFileErrors(t *testing.T) {
	if _, err := ReadStateFile(t.TempDir()); err == nil {
		t.Error("reading a missing state file must fail")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadStateFile(dir); err == nil {
		t.Error("reading a corrupt state file must fail")
	}

	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(`{"state_file_version":99,"api_port":1}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadStateFile(dir); err == nil {
		t.Error("an unsupported state file version must fail")
	}

	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(`{"state_file_version":1,"api_port":0}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := ReadStateFile(dir); err == nil {
		t.Error("a state file without a valid api_port must fail")
	}
}

func TestRemoveStateFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteStateFile(dir, NewState(1, "127.0.0.1:1", 1, "tok", "v", "")); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}
	if err := RemoveStateFile(dir); err != nil {
		t.Fatalf("RemoveStateFile: %v", err)
	}
	// Removing again must be a no-op, not an error.
	if err := RemoveStateFile(dir); err != nil {
		t.Fatalf("RemoveStateFile on a missing file: %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Error("the current process must be reported as alive")
	}
	if ProcessAlive(0) || ProcessAlive(-1) {
		t.Error("pid 0 and -1 must never be alive")
	}
}

func TestDescribeStateMasksToken(t *testing.T) {
	out := describeState(NewState(1, "127.0.0.1:1", 1, "super-secret-token-value", "1.0", ""))
	if strings.Contains(out, "super-secret-token-value") {
		t.Errorf("describeState leaked the token: %s", out)
	}
	if !strings.Contains(out, Redacted) {
		t.Errorf("describeState = %s, want the %s placeholder", out, Redacted)
	}
}

// ---------------------------------------------------------------- redactor --

func TestRedactorMasksSecrets(t *testing.T) {
	r := NewRedactor()
	if !r.Register("super-secret-token") {
		t.Fatal("Register rejected a long enough secret")
	}
	got := r.String("connecting with token=super-secret-token now")
	if strings.Contains(got, "super-secret-token") {
		t.Errorf("String leaked the secret: %s", got)
	}
	if !strings.Contains(got, Redacted) {
		t.Errorf("String = %s, want the %s placeholder", got, Redacted)
	}
}

func TestRedactorIgnoresShortValues(t *testing.T) {
	r := NewRedactor()
	if r.Register("ab") {
		t.Error("a two character value must not be registered; masking it would shred unrelated logs")
	}
	if r.Has() {
		t.Error("Has must be false after only short registrations")
	}
	if got := r.String("ab cd"); got != "ab cd" {
		t.Errorf("String = %q, want it unchanged", got)
	}
}

func TestRedactorMasksMultipleSecrets(t *testing.T) {
	r := NewRedactor()
	r.Register("token-abcdefghij")
	r.Register("secret-9876543210")
	got := r.String("token-abcdefghij and secret-9876543210")
	if strings.Contains(got, "abcdefghij") || strings.Contains(got, "9876543210") {
		t.Errorf("String leaked a secret: %s", got)
	}
}

func TestRedactingHandlerMasksMessagesAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewRedactingHandler(base, func() *Redactor {
		r := NewRedactor()
		r.Register("room-secret-value")
		return r
	}()))

	logger.Info("connecting", "secret", "room-secret-value", "token", "room-secret-value")

	out := buf.String()
	if strings.Contains(out, "room-secret-value") {
		t.Errorf("log leaked the secret: %s", out)
	}
	if !strings.Contains(out, Redacted) {
		t.Errorf("log = %s, want the %s placeholder", out, Redacted)
	}
	// Attribute keys must survive so structured queries keep working.
	if !strings.Contains(out, "token=") {
		t.Errorf("log = %s, want the attribute keys preserved", out)
	}
}

func TestRedactingHandlerWithAttrsAndGroup(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	r := NewRedactor()
	r.Register("grouped-secret-value")
	logger := slog.New(NewRedactingHandler(base, r)).With("early", "grouped-secret-value")

	logger.WithGroup("room").Info("state", "id", "grouped-secret-value")

	if strings.Contains(buf.String(), "grouped-secret-value") {
		t.Errorf("log leaked the secret through WithAttrs/WithGroup: %s", buf.String())
	}
}

func TestRedactingHandlerWithNilRedactorIsPassthrough(t *testing.T) {
	var buf bytes.Buffer
	handler := NewRedactingHandler(slog.NewTextHandler(&buf, nil), nil)
	if handler == nil {
		t.Fatal("a nil redactor must return the base handler, not nil")
	}
	slog.New(handler).Info("plain")
	if !strings.Contains(buf.String(), "plain") {
		t.Errorf("log = %s, want the message to pass through", buf.String())
	}
}

func TestRedactorConcurrentUse(t *testing.T) {
	r := NewRedactor()
	r.Register("concurrent-secret-value")
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				if strings.Contains(r.String("concurrent-secret-value"), "concurrent-secret-value") {
					t.Error("a secret leaked during concurrent use")
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
