package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/security"
)

func newTestLogger(t *testing.T, level string, buf *bytes.Buffer, redactor *security.Redactor) *Logger {
	t.Helper()
	l, err := New(Options{
		Level:     level,
		StateDir:  t.TempDir(),
		Redactor:  redactor,
		LogToFile: false,
		Stderr:    buf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestLevelsFilterOutput(t *testing.T) {
	tests := []struct {
		level        string
		wantDebugged bool
		wantInfo     bool
		wantError    bool
	}{
		{"error", false, false, true},
		{"warn", false, false, true},
		{"info", false, true, true},
		{"debug", true, true, true},
		{"trace", true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			var buf bytes.Buffer
			l := newTestLogger(t, tc.level, &buf, nil)
			l.Debug("a debug line")
			l.Info("an info line")
			l.Error("an error line")

			out := buf.String()
			if got := strings.Contains(out, "a debug line"); got != tc.wantDebugged {
				t.Errorf("level %s: debug line present = %v, want %v (output: %s)",
					tc.level, got, tc.wantDebugged, out)
			}
			if got := strings.Contains(out, "an info line"); got != tc.wantInfo {
				t.Errorf("level %s: info line present = %v, want %v (output: %s)",
					tc.level, got, tc.wantInfo, out)
			}
			if got := strings.Contains(out, "an error line"); got != tc.wantError {
				t.Errorf("level %s: error line present = %v, want %v (output: %s)",
					tc.level, got, tc.wantError, out)
			}
		})
	}
}

func TestUnknownLevelIsRejected(t *testing.T) {
	if _, err := New(Options{Level: "verbose", Stderr: &bytes.Buffer{}}); err == nil {
		t.Fatal("New accepted an unknown log level")
	}
}

func TestTokenIsNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	r := security.NewRedactor()
	const token = "api-token-abcdefghijklmnop"
	r.Register(token)

	l := newTestLogger(t, "debug", &buf, r)
	l.Info("state file written", "api_token", token, "path", "daemon.json")

	if strings.Contains(buf.String(), token) {
		t.Errorf("the api token leaked into the log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), security.Redacted) {
		t.Errorf("log = %s, want the %s placeholder", buf.String(), security.Redacted)
	}
	if !strings.Contains(buf.String(), "daemon.json") {
		t.Errorf("log = %s, want unrelated attributes preserved", buf.String())
	}
}

func TestMaskTokensBackstop(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"equals form", "token=supersecretvalue", "token=" + security.Redacted},
		{"colon form", "password:hunter2hunter2", "password:" + security.Redacted},
		{"secret equals", "secret=abcdef123456", "secret=" + security.Redacted},
		{"private key", "private_key=deadbeefdead", "private_key=" + security.Redacted},
		{"no secret", "no secrets here", "no secrets here"},
		{"empty", "", ""},
		{"key with no value", "token=", "token="},
		{"two secrets", "token=aaaaaaa1 and secret=bbbbbbb2",
			"token=" + security.Redacted + " and secret=" + security.Redacted},
		{"quoted value keeps quotes", `token="abcdefghijkl",`,
			`token="` + security.Redacted + `",`},
		{"password colon", "password:hunter2hunter2", "password:" + security.Redacted},
		{"password equals", "password=hunter2hunter2", "password=" + security.Redacted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskTokens(tc.in)
			if got != tc.want {
				t.Errorf("maskTokens(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMaskTokensTerminatesOnSecretOnlyInput(t *testing.T) {
	// Regression guard: an earlier implementation restarted the scan after each
	// replacement and looped forever on exactly this input.
	done := make(chan string, 1)
	go func() { done <- maskTokens("token=supersecretvalue") }()
	select {
	case got := <-done:
		if strings.Contains(got, "supersecretvalue") {
			t.Errorf("maskTokens leaked the value: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("maskTokens did not terminate")
	}
}

func TestFileSinkIsCreatedAndRestricted(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("permission bits are not meaningful on windows; the ACL is asserted by security.RestrictToOwner")
	}
	dir := t.TempDir()
	l, err := New(Options{Level: "info", StateDir: dir, LogToFile: true, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.Info("hello from the daemon")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := filepath.Join(dir, "daemon.log")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(raw), "hello from the daemon") {
		t.Errorf("log file = %s, want the line to be persisted", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("log file mode = %o, want 600", perm)
	}
}

func TestConsoleAndFileSinksBothReceive(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	l, err := New(Options{Level: "info", StateDir: dir, LogToFile: true, Stderr: &console})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.Info("dual sink line")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if !strings.Contains(console.String(), "dual sink line") {
		t.Errorf("console = %s, want the line", console.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(raw), "dual sink line") {
		t.Errorf("file = %s, want the line", raw)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	l, err := New(Options{Level: "info", StateDir: t.TempDir(), LogToFile: true, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestLevelVarCanChangeAtRuntime(t *testing.T) {
	var buf bytes.Buffer
	lv, err := NewLevelVar("error")
	if err != nil {
		t.Fatalf("NewLevelVar: %v", err)
	}
	l, err := New(Options{Level: "info", StateDir: t.TempDir(), LogToFile: false, Stderr: &buf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	// The LevelVar is a separate handle in production; here we only verify its
	// parse and name mapping.
	if lv.String() != "ERROR" {
		t.Errorf("String() = %q, want ERROR", lv.String())
	}
	if err := lv.Set("trace"); err != nil {
		t.Fatalf("Set(trace): %v", err)
	}
	if lv.String() != "DEBUG-4" {
		t.Errorf("String() = %q, want DEBUG-4 for trace", lv.String())
	}
	if err := lv.Set("nonsense"); err == nil {
		t.Error("Set must reject an unknown level")
	}
}
