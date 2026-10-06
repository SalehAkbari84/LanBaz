//! Bridge between the Tauri shell and a running lanbazd.
//!
//! This module reads the daemon state file and, for the graceful stop path,
//! speaks a minimal WebSocket exchange with the control API. That last part is
//! the one place where Rust touches the protocol, and it is deliberately tiny:
//! hello plus daemon.shutdown. Everything else goes through the TypeScript
//! DaemonClient so the protocol has a single implementation.

use std::io::{Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager};

/// The subset of <state-dir>/daemon.json the shell needs.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct DaemonState {
    pub pid: i32,
    pub api_listen: String,
    pub api_port: u16,
    pub api_token: String,
    pub api_path: String,
    pub version: String,
    pub started_at: String,
    /// True when the running daemon was started by the desktop shell, so a
    /// forced shell exit may clean it up. Absent in older daemons, which serde
    /// reports as false.
    #[serde(default)]
    pub managed_by_shell: bool,
}

/// What the webview receives to open its WebSocket.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DaemonEndpoint {
    pub url: String,
    pub token: String,
    pub pid: i32,
    pub version: String,
    pub state_dir: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct DaemonStateDir {
    pub path: String,
}

/// Windows state directory, matching config.DefaultStateDir on the Go side.
pub fn state_dir() -> PathBuf {
    if let Ok(local) = std::env::var("LOCALAPPDATA") {
        if !local.is_empty() {
            return PathBuf::from(local).join("LanBaz");
        }
    }
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".to_string());
    PathBuf::from(home).join(".local").join("state").join("LanBaz")
}

fn state_file() -> PathBuf {
    state_dir().join("daemon.json")
}

/// Reads daemon.json, returning None when no daemon has ever started or it has
/// already removed the file on shutdown.
pub fn read_state() -> Option<DaemonState> {
    let raw = std::fs::read_to_string(state_file()).ok()?;
    serde_json::from_str(&raw).ok()
}

/// True when a daemon state file exists and its API port accepts a connection.
pub fn is_running() -> Result<bool, String> {
    let Some(state) = read_state() else {
        return Ok(false);
    };
    if state.api_port == 0 {
        return Ok(false);
    }
    // The address the daemon actually bound, which is what api_listen records;
    // the port alone would assume 127.0.0.1 and miss a daemon on ::1.
    let addr: SocketAddr = state
        .api_listen
        .parse()
        .or_else(|_| format!("127.0.0.1:{}", state.api_port).parse())
        .map_err(|e| format!("invalid api address: {e}"))?;
    // A TCP connect is enough: the port is only bound while the API serves.
    match TcpStream::connect_timeout(&addr, Duration::from_millis(400)) {
        Ok(stream) => {
            drop(stream);
            Ok(true)
        }
        Err(_) => Ok(false),
    }
}

/// Returns the control API endpoint, or an error when no daemon is running.
///
/// Async and off the main thread: it does a blocking TCP probe, and a
/// synchronous command runs on the UI thread in Tauri v2.
#[tauri::command]
pub async fn daemon_endpoint() -> Result<DaemonEndpoint, String> {
    tauri::async_runtime::spawn_blocking(endpoint_blocking)
        .await
        .map_err(|e| e.to_string())?
}

fn endpoint_blocking() -> Result<DaemonEndpoint, String> {
    let state = read_state().ok_or_else(|| {
        "no daemon state file found; the lanbazd daemon does not appear to be running".to_string()
    })?;

    if !is_running().unwrap_or(false) {
        return Err(format!(
            "a state file exists but nothing is listening on port {}; the daemon may have crashed",
            state.api_port
        ));
    }

    let url = format!("ws://{}{}", state.api_listen, state.api_path);
    Ok(DaemonEndpoint {
        url,
        token: state.api_token,
        pid: state.pid,
        version: state.version,
        state_dir: state_dir().to_string_lossy().to_string(),
    })
}

/// Serializes every start. Both webviews, the tray and the supervisor can ask
/// for a daemon at the same moment; without this lock each saw "not running"
/// and spawned its own, and two daemons fought over one state file.
static START_LOCK: Mutex<()> = Mutex::new(());

/// Starts the daemon sidecar if it is not already running.
#[tauri::command]
pub async fn start_daemon(app: AppHandle) -> Result<(), String> {
    // The supervisor clears its suppression here too: whatever asked for a
    // daemon wants one running from now on.
    if let Some(supervisor) = app.try_state::<Arc<super::supervisor::Supervisor>>() {
        supervisor.unsuppress();
    }
    tauri::async_runtime::spawn_blocking(move || start_blocking(&app))
        .await
        .map_err(|e| e.to_string())?
}

/// The single start path, for the UI command, the tray and the supervisor.
pub fn start_blocking(app: &AppHandle) -> Result<(), String> {
    let _guard = START_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    if is_running().unwrap_or(false) {
        return Ok(());
    }
    // The spawn itself lives in the crate root, because the --managed-by-shell
    // flag must be passed on every path and one copy is how that stays true.
    super::spawn_sidecar(app).map_err(|e| e.to_string())?;

    // Wait for the state file to appear so the UI can connect immediately.
    // Creating the adapter can take a few seconds on first run.
    let deadline = Instant::now() + Duration::from_secs(15);
    while Instant::now() < deadline {
        if is_running().unwrap_or(false) {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    Err("the daemon did not become ready within 15 seconds".to_string())
}

/// Asks the daemon to stop gracefully through the control API.
#[tauri::command]
pub async fn stop_daemon(app: AppHandle) -> Result<(), String> {
    // A stop through the UI is a decision, not a crash; keep the supervisor
    // from undoing it.
    if let Some(supervisor) = app.try_state::<Arc<super::supervisor::Supervisor>>() {
        supervisor.suppress();
    }
    let result = tauri::async_runtime::spawn_blocking(stop_blocking)
        .await
        .map_err(|e| e.to_string())?;
    // Whatever the graceful path managed, no sidecar of ours outlives a stop.
    super::kill_sidecars(&app);
    result
}

/// Graceful stop, then waits for the API to go away. The caller kills the
/// sidecar afterwards if it is still there.
pub fn stop_blocking() -> Result<(), String> {
    let state = read_state().ok_or_else(|| "no daemon is running".to_string())?;
    graceful_shutdown(&state)?;
    // Give the daemon its grace period to say goodbye to peers and remove the
    // adapter, routes and firewall rule; a kill would skip all of that.
    let deadline = Instant::now() + Duration::from_secs(8);
    while Instant::now() < deadline {
        if !is_running().unwrap_or(false) {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    Err("the daemon did not stop within 8 seconds".to_string())
}

/// Sends hello + daemon.shutdown over a minimal WebSocket frame exchange.
///
/// Only the two methods the shell needs are implemented here. The framing is
/// written by hand to keep the Rust dependency list at zero beyond serde and
/// tauri itself.
fn graceful_shutdown(state: &DaemonState) -> Result<(), String> {
    let url = format!("ws://{}{}", state.api_listen, state.api_path);

    let Some((_, rest)) = url.split_once("://") else {
        return Err(format!("malformed daemon url {url}"));
    };
    let (authority, path) = match rest.find('/') {
        Some(i) => (&rest[..i], &rest[i..]),
        None => (rest, "/api"),
    };

    let mut stream = TcpStream::connect(authority).map_err(|e| format!("connect {authority}: {e}"))?;
    stream.set_read_timeout(Some(Duration::from_secs(3))).ok();
    stream.set_write_timeout(Some(Duration::from_secs(3))).ok();

    // RFC 6455 requires the key to be the base64 of 16 bytes; the daemon's
    // WebSocket library rejects anything else with 400. This is the RFC's own
    // sample nonce - the key is a handshake check, not a secret.
    let key = "dGhlIHNhbXBsZSBub25jZQ==";
    let handshake = format!(
        "GET {path} HTTP/1.1\r\nHost: {authority}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n"
    );
    write_all(&mut stream, handshake.as_bytes())?;

    // Drain the HTTP response headers; the daemon answers 101.
    let mut header = Vec::new();
    let mut byte = [0u8; 1];
    while !header.ends_with(b"\r\n\r\n") {
        match stream.read_exact(&mut byte) {
            Ok(_) => header.push(byte[0]),
            Err(e) => return Err(format!("handshake failed: {e}")),
        }
    }
    let head = String::from_utf8_lossy(&header);
    if !head.starts_with("HTTP/1.1 101") {
        return Err(format!(
            "upgrade refused: {}",
            head.lines().next().unwrap_or("")
        ));
    }

    // The token is escaped with {:?}, which produces a valid JSON string for the
    // characters a generated token can contain (base64url alphabet).
    let hello = format!(
        "{{\"id\":\"req-hello\",\"type\":\"hello\",\"version\":1,\"payload\":{{\"token\":{:?},\"client\":\"tauri-shell\"}}}}",
        state.api_token
    );
    write_frame(&mut stream, hello.as_bytes())?;
    // Consume the hello response before sending shutdown.
    let _ = read_frame(&mut stream)?;

    let shutdown = "{\"id\":\"req-shutdown\",\"type\":\"daemon.shutdown\",\"version\":1,\"payload\":{\"reason\":\"tauri shell\"}}";
    write_frame(&mut stream, shutdown.as_bytes())?;
    // The acknowledgement (or the close that follows it) confirms the request
    // was read before this socket goes away.
    let _ = read_frame(&mut stream);

    Ok(())
}

fn write_all(w: &mut impl Write, mut buf: &[u8]) -> Result<(), String> {
    while !buf.is_empty() {
        match w.write(buf) {
            Ok(0) => return Err("connection closed while writing".to_string()),
            Ok(n) => buf = &buf[n..],
            Err(e) => return Err(format!("write: {e}")),
        }
    }
    Ok(())
}

/// Writes one client-to-server WebSocket text frame with a masked payload.
fn write_frame(w: &mut impl Write, payload: &[u8]) -> Result<(), String> {
    let mut frame: Vec<u8> = Vec::with_capacity(payload.len() + 14);
    frame.push(0x81); // FIN + text
    let n = payload.len();
    if n < 126 {
        frame.push(0x80 | n as u8);
    } else if n < 65536 {
        frame.push(0x80 | 126);
        frame.extend_from_slice(&(n as u16).to_be_bytes());
    } else {
        frame.push(0x80 | 127);
        frame.extend_from_slice(&(n as u64).to_be_bytes());
    }
    // A fixed mask keeps this function pure. The payloads here are control
    // messages, never bulk game traffic; masking exists to stop proxies from
    // optimising a frame out of a loop, not as a security control.
    let mask = [0x37u8, 0xfa, 0x21, 0x3d];
    frame.extend_from_slice(&mask);
    for (i, b) in payload.iter().enumerate() {
        frame.push(b ^ mask[i % 4]);
    }
    w.write_all(&frame).map_err(|e| format!("write frame: {e}"))
}

/// Reads one server-to-client frame.
fn read_frame(r: &mut impl Read) -> Result<Vec<u8>, String> {
    let mut head = [0u8; 2];
    r.read_exact(&mut head).map_err(|e| format!("read frame: {e}"))?;
    let opcode = head[0] & 0x0F;
    let mut length = (head[1] & 0x7F) as usize;
    if length == 126 {
        let mut ext = [0u8; 2];
        r.read_exact(&mut ext).map_err(|e| format!("read length: {e}"))?;
        length = u16::from_be_bytes(ext) as usize;
    } else if length == 127 {
        let mut ext = [0u8; 8];
        r.read_exact(&mut ext).map_err(|e| format!("read length: {e}"))?;
        length = u64::from_be_bytes(ext) as usize;
    }
    let mut payload = vec![0u8; length];
    r.read_exact(&mut payload)
        .map_err(|e| format!("read payload: {e}"))?;
    if opcode == 0x8 {
        return Err("daemon closed the connection".to_string());
    }
    Ok(payload)
}

/// Returns the state directory so the UI can show it in Settings.
#[tauri::command]
pub fn daemon_state_dir() -> DaemonStateDir {
    DaemonStateDir {
        path: state_dir().to_string_lossy().to_string(),
    }
}