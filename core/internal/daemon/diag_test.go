package daemon

import (
	"archive/zip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lanbaz/lanbaz/core/internal/config"
	"github.com/lanbaz/lanbaz/core/internal/logging"
	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// The diagnostics zip has every part, and no secret from the log or the
// settings ends up in it.
func TestDiagnosticsBundleHasEverythingAndNoSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the connection check")
	}
	const token = "tok3n-SHOULD-NOT-LEAK-1234567890"
	const meteredKey = "meteredKEY0123456789abcdef"

	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	cfg.LogLevel = config.LogLevelDebug
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ring := logging.NewRing(500)
	lg, err := logging.New(logging.Options{Level: "debug", StateDir: cfg.StateDir, Stderr: io.Discard, Ring: ring})
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()
	d, err := New(Options{Config: cfg, Token: testToken, Logger: lg.Logger, BuildInfo: BuildInfo{Version: "9.9.9"}, SocialBus: &social.MemBus{}, LogRing: ring})
	if err != nil {
		t.Fatal(err)
	}
	c := startDaemon(t, d, &cfg)
	d.diagDir = t.TempDir()

	lg.Info("pretend a token leaked into a log line", "token", token)
	var saved protocol.Settings
	if err := call(t, c, protocol.MethodSettingsGet, nil, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Metered = &protocol.MeteredRelay{App: "myapp", Key: meteredKey}
	if err := call(t, c, protocol.MethodSettingsSet, saved, &saved); err != nil {
		t.Fatal(err)
	}

	path, err := d.diagBundle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	seen := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		seen[f.Name] = string(b)
	}
	for _, name := range []string{"log.txt", "diagnose.json", "status.json", "windows.txt"} {
		if seen[name] == "" {
			t.Errorf("%s is missing or empty", name)
		}
	}
	for name, body := range seen {
		if strings.Contains(body, token) || strings.Contains(body, meteredKey) || strings.Contains(body, testToken) {
			t.Errorf("%s contains a secret", name)
		}
	}
	if !strings.Contains(seen["status.json"], `"version": "9.9.9"`) || !strings.Contains(seen["status.json"], redacted) {
		t.Errorf("status.json lacks the version or the redaction marker:\n%s", seen["status.json"])
	}
	if !strings.Contains(seen["log.txt"], "pretend a token leaked") {
		t.Error("the developer log is not in the bundle")
	}
}
