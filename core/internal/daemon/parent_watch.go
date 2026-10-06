package daemon

import (
	"log/slog"
	"os"
	"time"
)

// How often the parent is checked. Short enough that a user who kills the shell
// does not spend a full match with a dead LAN, long enough to be free: this is
// one cheap syscall every couple of seconds for the life of the daemon.
const parentPollInterval = 2 * time.Second

// exitWithParent shuts the daemon down when the process that started it is gone.
//
// Why the daemon watches its own parent instead of the shell watching the child:
// a forced kill of the shell (Task Manager, a panic, a power event) runs no
// cleanup code in the shell, so anything the shell was going to do on the way out
// simply does not happen. The orphaned process is the only one still able to
// notice, and it is the one holding the state file and the API port.
//
// Windows has no portable "wait for a parent pid" primitive, so this polls. The
// pid is read once, at start: re-reading `os.Getppid` semantics across platforms
// is a portability trap, and the parent cannot change for a process that was
// spawned as a child.
//
// The check is deliberately coarse. A transient failure to open the parent — a
// permission blip, a race while the parent is being torn down — must not be read
// as "the parent died", so any error is treated as alive and the next tick
// re-decides. That biases towards leaving a daemon running, which the next launch
// can clean up, rather than killing one that was merely slow to answer.
func (d *Daemon) exitWithParent(log *slog.Logger) {
	parent := os.Getppid()
	if parent <= 1 {
		// Started by init or a service manager: there is no parent to outlive.
		return
	}

	ticker := time.NewTicker(parentPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		if processAlive(parent) {
			continue
		}
		log.Info("the LanBaz shell that started this daemon is gone; shutting down",
			"parent_pid", parent)
		d.Shutdown(time.Duration(d.cfg.ShutdownGraceMillis) * time.Millisecond)
		return
	}
}
