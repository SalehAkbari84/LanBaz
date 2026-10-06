package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// StateFileVersion is bumped when the state file layout changes.
const StateFileVersion = 1

// State is the daemon handshake record written to StateDir/daemon.json. The UI
// and lanbazctl read it to discover the dynamically chosen API port and to
// authenticate. It is created with owner-only permissions and is never logged.
type State struct {
	StateFileVersion int    `json:"state_file_version"`
	PID              int    `json:"pid"`
	APIListen        string `json:"api_listen"`
	APIPort          int    `json:"api_port"`
	APIToken         string `json:"api_token"`
	APIPath          string `json:"api_path"`
	Version          string `json:"version"`
	ProtocolVersion  int    `json:"protocol_version"`
	StartedAt        string `json:"started_at"`
	// ExePath lets the UI and the CLI start the same daemon binary when a
	// previous instance is not running.
	ExePath string `json:"exe_path,omitempty"`
	// ManagedByShell is set by the flag the desktop shell passes when it starts
	// the daemon as a sidecar. It tells the shell that this particular process
	// is its child, so a forced exit (Task Manager, a crash) can clean it up
	// without touching a daemon the user started by hand.
	ManagedByShell bool `json:"managed_by_shell,omitempty"`
}

// StateFileName is the file name used inside the state directory.
const StateFileName = "daemon.json"

// WriteStateFile persists the state atomically with owner-only permissions.
// A restrictive temporary file is written first and then renamed, so a crash
// can never leave a world readable token behind.
func WriteStateFile(stateDir string, st State) (string, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "create state dir: %v", err)
	}
	st.StateFileVersion = StateFileVersion
	st.APIPath = protocol.APIPath

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "encode state: %v", err)
	}
	data = append(data, '\n')

	path := filepath.Join(stateDir, StateFileName)
	tmp, err := os.CreateTemp(stateDir, StateFileName+".*.tmp")
	if err != nil {
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "create temp state file: %v", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best effort cleanup; a successful rename makes this a no-op.
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "secure temp state file: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "write temp state file: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "sync temp state file: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "close temp state file: %v", err)
	}
	// CreateTemp already used 0600, but a pre-existing target keeps its old
	// mode after a rename on some platforms, so re-assert it.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "secure state file: %v", err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "replace state file: %v", err)
	}
	if err := RestrictToOwner(path); err != nil {
		return "", protocol.NewErrorf(protocol.CodeStateFileUnwritable, "restrict state file permissions: %v", err)
	}
	return path, nil
}

// ReadStateFile loads the daemon state. A missing file is reported with
// os.ErrNotExist wrapped in a NOT_FOUND API error so callers can distinguish
// "no daemon" from "broken state file".
func ReadStateFile(stateDir string) (State, error) {
	path := filepath.Join(stateDir, StateFileName)
	data, err := os.ReadFile(path)
	// The daemon replaces the file atomically, and on Windows a reader that
	// lands in that instant gets a sharing violation. Retry briefly.
	for i := 0; err != nil && !errors.Is(err, os.ErrNotExist) && i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		data, err = os.ReadFile(path)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, protocol.NewError(protocol.CodeNotFound, "no daemon state file; is lanbazd running?")
		}
		return State{}, protocol.NewErrorf(protocol.CodeInternal, "read state file: %v", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, protocol.NewErrorf(protocol.CodeInternal, "parse state file: %v", err)
	}
	if st.StateFileVersion != StateFileVersion {
		return State{}, protocol.NewErrorf(protocol.CodeInternal,
			"state file version %d is not supported (expected %d)", st.StateFileVersion, StateFileVersion)
	}
	if st.APIPort <= 0 {
		return State{}, protocol.NewError(protocol.CodeInternal, "state file has no valid api_port")
	}
	return st, nil
}

// RemoveStateFile deletes the state file, ignoring a missing file.
func RemoveStateFile(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, StateFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return protocol.NewErrorf(protocol.CodeStateFileUnwritable, "remove state file: %v", err)
	}
	return nil
}

// ProcessAlive reports whether a pid currently exists. Used by lanbazctl to
// distinguish a stale state file from a live daemon.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc != nil
}

// NewState builds the state record for a freshly started daemon.
func NewState(pid int, listen string, port int, token, version, exePath string) State {
	return State{
		PID:             pid,
		APIListen:       listen,
		APIPort:         port,
		APIToken:        token,
		APIPath:         protocol.APIPath,
		Version:         version,
		ProtocolVersion: protocol.Version,
		StartedAt:       time.Now().UTC().Format(time.RFC3339Nano),
		ExePath:         exePath,
	}
}

// describeState renders a state record for debug logs with the token masked.
func describeState(st State) string {
	return fmt.Sprintf("state{pid:%d listen:%s port:%d token:%s version:%s}",
		st.PID, st.APIListen, st.APIPort, Redacted, st.Version)
}
