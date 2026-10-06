//! Daemon supervision: keeps `lanbazd` alive for as long as the shell runs.
//!
//! A user mid-match does not know or care that the daemon is a separate process;
//! they know "LanBaz stopped working". So the shell treats the daemon the way a
//! product treats its own engine: it watches it, and when it dies without the
//! user asking for that, it brings it back and says so.
//!
//! The loop is deliberately dumb and deliberately serialized: one thread, one
//! probe every few seconds, restart with backoff. Anything smarter — a state
//! machine, per-error policies — would be harder to trust than the thing it
//! guards.
//!
//! Two rules keep the supervisor from fighting the user:
//!
//!   * A graceful stop requested through the tray or the UI sets `suppressed`,
//!     and the supervisor stays out of the way until the next explicit start.
//!   * Restarts back off (2s, 4s, 8s… capped at 30s) so a daemon that dies on
//!     boot — a broken config, a missing DLL — produces a few lines in the log,
//!     not a fork bomb.

use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager};

use crate::daemon;

/// How often the running state is probed while everything is healthy.
const POLL_INTERVAL: Duration = Duration::from_secs(3);

/// Ceiling for the restart backoff.
const MAX_BACKOFF: Duration = Duration::from_secs(30);

/// Emitted to both webviews whenever supervision changes something.
pub const EVENT: &str = "lanbaz://daemon-supervision";

/// What the UI is told about the engine underneath it.
#[derive(Debug, Clone, Serialize)]
pub struct SupervisionEvent {
    /// `restarting` while the backoff counts down, `started` once healthy,
    /// `given_up` only if the user stopped the daemon on purpose.
    pub state: &'static str,
    /// Attempt number for `restarting`, so the UI can say "attempt 3".
    pub attempt: u32,
    /// Seconds until the next attempt, for `restarting`.
    pub next_in_seconds: u64,
}

/// Shared supervision state.
#[derive(Default)]
pub struct Supervisor {
    /// Set by a graceful stop, cleared by an explicit start.
    suppressed: AtomicBool,
    /// How many consecutive restarts have happened without a healthy probe.
    attempts: AtomicU32,
    /// Set once after a successful start so the first poll does not race the
    /// daemon's own boot.
    warm: AtomicBool,
    /// Serializes restarts so a slow `start_daemon` cannot stack two sidecars.
    restarting: Mutex<()>,
}

impl Supervisor {
    fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }

    /// The user asked for the daemon to be off. Remember that until they ask
    /// again for it to be on.
    pub fn suppress(&self) {
        self.suppressed.store(true, Ordering::SeqCst);
        self.attempts.store(0, Ordering::SeqCst);
    }

    /// The user asked for the daemon. Clear any standing suppression.
    pub fn unsuppress(&self) {
        self.suppressed.store(false, Ordering::SeqCst);
        self.attempts.store(0, Ordering::SeqCst);
        self.warm.store(true, Ordering::SeqCst);
    }
}

/// Spawns the supervision thread. One per process; the handle is dropped.
pub fn start(app: &AppHandle) {
    let handle = app.clone();
    let supervisor = Supervisor::new();
    app.manage(supervisor.clone());

    std::thread::Builder::new()
        .name("lanbaz-supervisor".into())
        .spawn(move || {
            // The shell brings its engine up itself rather than waiting for a
            // webview to ask: a window launched hidden into the tray must still
            // have a working LAN.
            if let Err(err) = daemon::start_blocking(&handle) {
                eprintln!("lanbaz: the daemon did not start: {err}");
            }
            supervisor.warm.store(true, Ordering::SeqCst);
            loop {
                std::thread::sleep(POLL_INTERVAL);
                tick(&handle, &supervisor);
            }
        })
        .expect("the supervisor thread must start");
}

/// One pass of the watch loop.
fn tick(app: &AppHandle, supervisor: &Supervisor) {
    // A graceful stop is the user's decision, not a crash to undo.
    if supervisor.suppressed.load(Ordering::SeqCst) {
        supervisor.warm.store(false, Ordering::SeqCst);
        return;
    }

    match daemon::is_running() {
        Ok(true) => {
            // Healthy again: forget the history, so the next crash restarts the
            // backoff from short rather than resuming where it left off.
            supervisor.attempts.store(0, Ordering::SeqCst);
            supervisor.warm.store(true, Ordering::SeqCst);
        }
        _ => {
            // The very first ticks of app boot race the sidecar's own startup;
            // a missing state file in that window is not a crash. Only a shell
            // that has seen the daemon healthy once may conclude otherwise.
            if !supervisor.warm.load(Ordering::SeqCst) {
                return;
            }
            restart(app, supervisor);
        }
    }
}

/// Brings the daemon back, with backoff, and says so.
fn restart(app: &AppHandle, supervisor: &Supervisor) {
    // take the lock or leave: a second tick while one restart is mid-flight
    // must not spawn a duplicate sidecar.
    let Ok(_guard) = supervisor.restarting.try_lock() else {
        return;
    };

    let attempt = supervisor.attempts.fetch_add(1, Ordering::SeqCst) + 1;
    // 2s, 4s, 8s, 16s, then the cap. attempt 1 waits the shortest time because
    // most crashes are transient and users want the room back *now*.
    let backoff = backoff_for(attempt);

    emit(
        app,
        SupervisionEvent {
            state: "restarting",
            attempt,
            next_in_seconds: backoff.as_secs(),
        },
    );

    std::thread::sleep(backoff);

    // Re-check after the sleep: the user may have stopped the daemon (or quit
    // the shell) during the backoff window.
    if supervisor.suppressed.load(Ordering::SeqCst) {
        return;
    }

    // Through the same start path the UI uses, so the sidecar is registered
    // with DaemonProcesses and the tray can still stop what we started.
    let started = daemon::start_blocking(app);

    match started {
        Ok(()) => {
            supervisor.attempts.store(0, Ordering::SeqCst);
            emit(
                app,
                SupervisionEvent {
                    state: "started",
                    attempt: 0,
                    next_in_seconds: 0,
                },
            );
        }
        Err(err) => {
            eprintln!("lanbaz: supervisor restart attempt {attempt} failed: {err}");
        }
    }
}

fn backoff_for(attempt: u32) -> Duration {
    // Attempt 1 waits 2s, then doubles: 2, 4, 8, 16, 32 → capped at 30. The cap
    // is the only limiter: clamping the exponent here instead would freeze the
    // sequence at 16s and quietly change the policy after four failures.
    // saturating_pow keeps a pathological attempt count from overflowing.
    let secs = 2u64.saturating_pow(attempt);
    Duration::from_secs(secs).min(MAX_BACKOFF)
}

fn emit(app: &AppHandle, event: SupervisionEvent) {
    let _ = app.emit(EVENT, event);
}

#[cfg(test)]
mod tests {
    use super::{backoff_for, MAX_BACKOFF};
    use std::time::Duration;

    #[test]
    fn backoff_grows_then_caps() {
        assert_eq!(backoff_for(1), Duration::from_secs(2));
        assert_eq!(backoff_for(2), Duration::from_secs(4));
        assert_eq!(backoff_for(3), Duration::from_secs(8));
        assert_eq!(backoff_for(4), Duration::from_secs(16));
        assert_eq!(backoff_for(5), Duration::from_secs(30));
        assert_eq!(backoff_for(50), MAX_BACKOFF);
    }
}
