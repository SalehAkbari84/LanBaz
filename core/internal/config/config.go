// Package config loads and validates the LanBaz daemon configuration.
//
// Precedence, lowest to highest: built-in defaults, config file, environment
// variables, command line flags. The control API is only ever bound to a
// loopback address; a non-loopback listen address is rejected by Validate
// because the API is a local, token authenticated interface.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// AppDirName is the per-user directory used for the state file and logs.
const AppDirName = "LanBaz"

// Log levels accepted by the daemon.
const (
	LogLevelTrace = "trace"
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// Virtual network backends.
//
// The default is auto: Wintun where the driver is present, and an in-memory
// backend everywhere else. The fallback is deliberate rather than a concession.
// A daemon that refused to start without administrator rights would leave a user
// with a program that cannot pair, cannot see their friends and cannot say why;
// one that starts, connects and reports "no virtual network" is strictly more
// useful and strictly more honest.
const (
	// NetworkAuto picks Wintun when the driver loads and the in-memory backend
	// otherwise.
	NetworkAuto = "auto"
	// NetworkWintun requires Wintun, and fails a room's network if it is absent.
	NetworkWintun = "wintun"
	// NetworkMemory always uses the in-memory backend, which moves no packets.
	// It exists for development and for a container with no driver.
	NetworkMemory = "memory"
	// NetworkOff disables the virtual network entirely.
	NetworkOff = "off"
)

// Config is the daemon configuration.
type Config struct {
	// ListenHost is the loopback address the control API binds to. The port is
	// always chosen dynamically unless ListenPort is set.
	ListenHost string `json:"listen_host"`
	// ListenPort is 0 (dynamic) unless explicitly pinned. A fixed port is only
	// useful for debugging; the daemon never advertises a default port.
	ListenPort int `json:"listen_port"`
	// LogLevel is one of trace|debug|info|warn|error.
	LogLevel string `json:"log_level"`
	// StateDir holds daemon.json (API port + token) and daemon.log.
	StateDir string `json:"state_dir"`
	// APIToken, when set, is used verbatim instead of generating a token. It is
	// never written to logs.
	APIToken string `json:"api_token,omitempty"`
	// MaxConnections bounds concurrent local control connections.
	MaxConnections int `json:"max_connections"`
	// RequestsPerSecond is the per-connection control request rate limit.
	RequestsPerSecond float64 `json:"requests_per_second"`
	// RequestsBurst is the per-connection burst allowance.
	RequestsBurst int `json:"requests_burst"`
	// ShutdownGraceMillis is the default graceful shutdown budget.
	ShutdownGraceMillis int `json:"shutdown_grace_ms"`
	// NetworkBackend selects the virtual adapter implementation: auto|wintun|
	// memory|off.
	NetworkBackend string `json:"network_backend"`
	// NetworkMTU is the virtual interface's packet size bound. It must not exceed
	// the transport's MTU, or the router would drop every packet a game sent at
	// full size. 0 selects the built-in default.
	NetworkMTU int `json:"network_mtu"`
}

// Default returns the built-in defaults with the platform state directory
// resolved.
func Default() Config {
	return Config{
		ListenHost:          "127.0.0.1",
		ListenPort:          0,
		LogLevel:            LogLevelInfo,
		StateDir:            DefaultStateDir(),
		MaxConnections:      8,
		RequestsPerSecond:   200,
		RequestsBurst:       400,
		ShutdownGraceMillis: 5000,
		NetworkBackend:      NetworkAuto,
		NetworkMTU:          0,
	}
}

// DefaultStateDir returns %LOCALAPPDATA%\LanBaz on Windows and
// $XDG_STATE_HOME (or ~/.local/state)/LanBaz elsewhere.
func DefaultStateDir() string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" && os.PathSeparator == '\\' {
		return filepath.Join(dir, AppDirName)
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, AppDirName)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", AppDirName)
	}
	return AppDirName
}

// Load reads the configuration from defaults, then path (when it exists), then
// the LANBAZ_* environment variables. Flags are applied by the caller with
// Override so that the precedence stays explicit.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := json.Unmarshal(data, &cfg); err != nil {
				return Config{}, protocol.NewErrorf(protocol.CodeConfigInvalid, "invalid config file %s: %v", path, err)
			}
		case errors.Is(err, os.ErrNotExist):
			// A missing explicit config file is not an error: defaults apply.
		default:
			return Config{}, protocol.NewErrorf(protocol.CodeConfigInvalid, "cannot read config file %s: %v", path, err)
		}
	}

	applyEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v, ok := os.LookupEnv("LANBAZ_LISTEN_HOST"); ok {
		cfg.ListenHost = v
	}
	if v, ok := os.LookupEnv("LANBAZ_LISTEN_PORT"); ok {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err == nil {
			cfg.ListenPort = p
		}
	}
	if v, ok := os.LookupEnv("LANBAZ_LOG_LEVEL"); ok {
		cfg.LogLevel = v
	}
	if v, ok := os.LookupEnv("LANBAZ_STATE_DIR"); ok {
		cfg.StateDir = v
	}
	if v, ok := os.LookupEnv("LANBAZ_API_TOKEN"); ok {
		cfg.APIToken = v
	}
	if v, ok := os.LookupEnv("LANBAZ_MAX_CONNECTIONS"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			cfg.MaxConnections = n
		}
	}
	if v, ok := os.LookupEnv("LANBAZ_NETWORK_BACKEND"); ok {
		cfg.NetworkBackend = v
	}
	if v, ok := os.LookupEnv("LANBAZ_NETWORK_MTU"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			cfg.NetworkMTU = n
		}
	}
}

// Override applies an explicit flag value, ignoring empty strings so that an
// unset flag never clobbers a lower precedence source.
func (c *Config) Override(flag, value string) {
	switch flag {
	case "listen-host":
		if value != "" {
			c.ListenHost = value
		}
	case "listen-port":
		if value != "" {
			var p int
			if _, err := fmt.Sscanf(value, "%d", &p); err == nil {
				c.ListenPort = p
			}
		}
	case "log-level":
		if value != "" {
			c.LogLevel = value
		}
	case "state-dir":
		if value != "" {
			c.StateDir = value
		}
	case "api-token":
		if value != "" {
			c.APIToken = value
		}
	case "network-backend":
		if value != "" {
			c.NetworkBackend = value
		}
	case "network-mtu":
		if value != "" {
			var n int
			if _, err := fmt.Sscanf(value, "%d", &n); err == nil {
				c.NetworkMTU = n
			}
		}
	}
}

// Validate enforces the safety invariants of the configuration.
func (c Config) Validate() error {
	host := strings.TrimSpace(c.ListenHost)
	if host == "" {
		return protocol.NewError(protocol.CodeConfigInvalid, "listen_host must not be empty")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		// The control API is token authenticated but not multi-user safe and
		// intentionally has no transport encryption beyond loopback. Binding it
		// to a routable address would expose room controls to the network.
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"listen_host %q is not a loopback address; the control API is loopback only", c.ListenHost)
	}
	if c.ListenPort < 0 || c.ListenPort > 65535 {
		return protocol.NewErrorf(protocol.CodeConfigInvalid, "listen_port %d is out of range", c.ListenPort)
	}
	if !ValidLogLevel(c.LogLevel) {
		return protocol.NewErrorf(protocol.CodeConfigInvalid, "log_level %q is not one of trace|debug|info|warn|error", c.LogLevel)
	}
	if strings.TrimSpace(c.StateDir) == "" {
		return protocol.NewError(protocol.CodeConfigInvalid, "state_dir must not be empty")
	}
	if c.MaxConnections <= 0 {
		return protocol.NewError(protocol.CodeConfigInvalid, "max_connections must be positive")
	}
	if c.RequestsPerSecond <= 0 {
		return protocol.NewError(protocol.CodeConfigInvalid, "requests_per_second must be positive")
	}
	if c.RequestsBurst <= 0 {
		return protocol.NewError(protocol.CodeConfigInvalid, "requests_burst must be positive")
	}
	if c.ShutdownGraceMillis < 0 {
		return protocol.NewError(protocol.CodeConfigInvalid, "shutdown_grace_ms must not be negative")
	}
	if !ValidNetworkBackend(c.NetworkBackend) {
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"network_backend %q is not one of auto|wintun|memory|off", c.NetworkBackend)
	}
	// A tiny MTU would make the LAN unusable and a huge one would overflow the
	// transport's frame, so both ends are bounded here rather than being left for
	// the router to discover at the first packet.
	if c.NetworkMTU < 0 || c.NetworkMTU > 9000 {
		return protocol.NewErrorf(protocol.CodeConfigInvalid,
			"network_mtu %d is out of range; use 576 to 9000, or 0 for the default", c.NetworkMTU)
	}
	return nil
}

// ValidNetworkBackend reports whether the backend name is one this build knows.
func ValidNetworkBackend(b string) bool {
	switch strings.ToLower(strings.TrimSpace(b)) {
	case NetworkAuto, NetworkWintun, NetworkMemory, NetworkOff:
		return true
	default:
		return false
	}
}

// ValidLogLevel reports whether level is accepted by the daemon logger.
func ValidLogLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LogLevelTrace, LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
		return true
	default:
		return false
	}
}

// ListenAddress renders the address passed to net.Listen.
func (c Config) ListenAddress() string {
	return net.JoinHostPort(c.ListenHost, fmt.Sprintf("%d", c.ListenPort))
}

// StateFile is the path of the daemon state file inside StateDir.
func (c Config) StateFile() string {
	return filepath.Join(c.StateDir, "daemon.json")
}

// LogFile is the path of the human readable daemon log inside StateDir.
func (c Config) LogFile() string {
	return filepath.Join(c.StateDir, "daemon.log")
}
