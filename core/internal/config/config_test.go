package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
	if cfg.ListenHost != "127.0.0.1" {
		t.Errorf("listen host = %q, want 127.0.0.1", cfg.ListenHost)
	}
	if cfg.ListenPort != 0 {
		t.Errorf("listen port = %d, want 0 (dynamic)", cfg.ListenPort)
	}
	if cfg.LogLevel != LogLevelInfo {
		t.Errorf("log level = %q, want %q", cfg.LogLevel, LogLevelInfo)
	}
}

func TestLoadWithoutFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("loaded config is invalid: %v", err)
	}
}

func TestLoadReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{"listen_host":"127.0.0.1","log_level":"debug","listen_port":51234,"max_connections":3}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != LogLevelDebug {
		t.Errorf("log level = %q, want debug", cfg.LogLevel)
	}
	if cfg.ListenPort != 51234 {
		t.Errorf("listen port = %d, want 51234", cfg.ListenPort)
	}
	if cfg.MaxConnections != 3 {
		t.Errorf("max connections = %d, want 3", cfg.MaxConnections)
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted invalid JSON")
	}
}

func TestEnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"log_level":"debug"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("LANBAZ_LOG_LEVEL", "warn")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != LogLevelWarn {
		t.Errorf("log level = %q, want warn (env must beat the file)", cfg.LogLevel)
	}
}

func TestOverrideBeatsEnv(t *testing.T) {
	t.Setenv("LANBAZ_LOG_LEVEL", "warn")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Override("log-level", "error")
	if cfg.LogLevel != LogLevelError {
		t.Errorf("log level = %q, want error", cfg.LogLevel)
	}
}

func TestOverrideWithEmptyValueKeepsLowerPrecedence(t *testing.T) {
	t.Setenv("LANBAZ_LOG_LEVEL", "warn")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Override("log-level", "")
	if cfg.LogLevel != LogLevelWarn {
		t.Errorf("log level = %q, want warn preserved from env", cfg.LogLevel)
	}
}

func TestOverridePort(t *testing.T) {
	cfg := Default()
	cfg.Override("listen-port", "9999")
	if cfg.ListenPort != 9999 {
		t.Errorf("listen port = %d, want 9999", cfg.ListenPort)
	}
	cfg.Override("listen-port", "")
	if cfg.ListenPort != 9999 {
		t.Errorf("listen port = %d, want 9999 preserved", cfg.ListenPort)
	}
}

func TestValidateRejectsNonLoopback(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "192.168.1.10", "::", "example.com"} {
		cfg := Default()
		cfg.ListenHost = host
		err := cfg.Validate()
		if err == nil {
			t.Errorf("listen host %q was accepted; the control api must be loopback only", host)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Errorf("error for %q = %v, want a loopback explanation", host, err)
		}
	}
}

func TestValidateAcceptsLoopbackVariants(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.0.0.2", "::1"} {
		cfg := Default()
		cfg.ListenHost = host
		if err := cfg.Validate(); err != nil {
			t.Errorf("listen host %q was rejected: %v", host, err)
		}
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty host", func(c *Config) { c.ListenHost = "  " }},
		{"negative port", func(c *Config) { c.ListenPort = -1 }},
		{"port too large", func(c *Config) { c.ListenPort = 70000 }},
		{"bad log level", func(c *Config) { c.LogLevel = "verbose" }},
		{"empty state dir", func(c *Config) { c.StateDir = "" }},
		{"zero max connections", func(c *Config) { c.MaxConnections = 0 }},
		{"zero rate", func(c *Config) { c.RequestsPerSecond = 0 }},
		{"zero burst", func(c *Config) { c.RequestsBurst = 0 }},
		{"negative grace", func(c *Config) { c.ShutdownGraceMillis = -1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
}

func TestValidLogLevel(t *testing.T) {
	for _, level := range []string{"trace", "debug", "info", "warn", "error", "INFO", " warn "} {
		if !ValidLogLevel(level) {
			t.Errorf("level %q must be valid", level)
		}
	}
	for _, level := range []string{"", "verbose", "fatal"} {
		if ValidLogLevel(level) {
			t.Errorf("level %q must be invalid", level)
		}
	}
}

func TestListenAddressAndPaths(t *testing.T) {
	cfg := Default()
	cfg.ListenPort = 4321
	if got := cfg.ListenAddress(); got != "127.0.0.1:4321" {
		t.Errorf("listen address = %q, want 127.0.0.1:4321", got)
	}
	if got := cfg.StateFile(); !strings.HasSuffix(got, filepath.Join("daemon.json")) {
		t.Errorf("state file = %q, want it to end in daemon.json", got)
	}
	if got := cfg.LogFile(); !strings.HasSuffix(got, filepath.Join("daemon.log")) {
		t.Errorf("log file = %q, want it to end in daemon.log", got)
	}
}

func TestDefaultStateDirIsPerUser(t *testing.T) {
	dir := DefaultStateDir()
	if dir == "" {
		t.Fatal("default state dir must not be empty")
	}
	if !strings.Contains(dir, AppDirName) {
		t.Errorf("state dir = %q, want it to contain %q", dir, AppDirName)
	}
}
