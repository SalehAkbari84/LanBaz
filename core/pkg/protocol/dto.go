package protocol

import "time"

// HelloRequest is the mandatory first message on every local WebSocket
// connection. It carries the token written by the daemon into its state file.
type HelloRequest struct {
	Token   string `json:"token"`
	Client  string `json:"client,omitempty"`
	Version string `json:"version,omitempty"`
}

// HelloResponse confirms an authenticated connection.
type HelloResponse struct {
	Daemon          string `json:"daemon"`
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	ServerTime      string `json:"server_time"`
}

// DaemonState enumerates the daemon lifecycle states reported over the API.
type DaemonState string

const (
	StateStarting DaemonState = "starting"
	StateRunning  DaemonState = "running"
	StateStopping DaemonState = "stopping"
	StateStopped  DaemonState = "stopped"
)

// DaemonStatus is the payload of daemon.status.
type DaemonStatus struct {
	State           DaemonState `json:"state"`
	Version         string      `json:"version"`
	Commit          string      `json:"commit"`
	BuildTime       string      `json:"build_time"`
	GoVersion       string      `json:"go_version"`
	ProtocolVersion int         `json:"protocol_version"`
	PID             int         `json:"pid"`
	UptimeSeconds   float64     `json:"uptime_seconds"`
	StartedAt       string      `json:"started_at"`
	APIListen       string      `json:"api_listen"`
	APIPort         int         `json:"api_port"`
	StateDir        string      `json:"state_dir"`
	LogLevel        string      `json:"log_level"`
	ActiveClients   int         `json:"active_clients"`
	RoomCount       int         `json:"room_count"`
	PeerCount       int         `json:"peer_count"`
}

// DaemonVersion is the payload of daemon.version. It contains no build secrets
// beyond the public version metadata.
type DaemonVersion struct {
	Daemon          string `json:"daemon"`
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	BuildTime       string `json:"build_time"`
	GoVersion       string `json:"go_version"`
	ProtocolVersion int    `json:"protocol_version"`
	Platform        string `json:"platform"`
	Phase           string `json:"phase"`
}

// ShutdownRequest is the payload of daemon.shutdown.
type ShutdownRequest struct {
	// GracePeriod is an optional client requested grace period in
	// milliseconds. Zero means "use the daemon default".
	GracePeriod int    `json:"grace_period,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// ShutdownResponse acknowledges a shutdown request. The daemon stops accepting
// new work immediately and finishes in-flight shutdown asynchronously.
type ShutdownResponse struct {
	Accepted     bool   `json:"accepted"`
	GracePeriod  int    `json:"grace_period_ms"`
	StopDeadline string `json:"stop_deadline"`
}

// StateEvent is pushed on EventDaemonState whenever the lifecycle state changes.
type StateEvent struct {
	State     DaemonState `json:"state"`
	Previous  DaemonState `json:"previous"`
	Timestamp time.Time   `json:"timestamp"`
}

// LogEvent is pushed on EventDaemonLog for daemon log lines above the
// configured client log threshold. Secrets are redacted before they reach here.
type LogEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// PeerID identifies a peer inside a room. It is a 128-bit random value encoded
// as lowercase hex.
type PeerID string

// String implements fmt.Stringer.
func (p PeerID) String() string { return string(p) }

// IsValid reports whether the peer id has the expected shape.
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
