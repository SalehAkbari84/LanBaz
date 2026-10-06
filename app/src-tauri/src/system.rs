//! Windows integration that needs administrator rights: knowing whether we have
//! them, and starting at logon with them.
//!
//! # Why autostart is a scheduled task
//!
//! LanBaz runs elevated (see lanbaz.exe.manifest). The usual autostart - a value
//! under HKCU\...\Run - cannot launch an elevated program: Windows silently skips
//! it at logon. A scheduled task with "run with highest privileges" is the
//! supported way to start an elevated app at logon without a UAC prompt, and it
//! is what the toggle in Settings creates and removes.

use serde::Serialize;

/// Name of the logon task in Task Scheduler.
#[cfg(target_os = "windows")]
const TASK_NAME: &str = "LanBaz";

/// Command-line flag the logon task passes, so the app starts in the tray.
pub const MINIMIZED_FLAG: &str = "--minimized";

#[derive(Debug, Clone, Serialize)]
pub struct Elevation {
    /// True when this process holds an elevated (administrator) token.
    pub elevated: bool,
}

/// Whether the app is running with administrator rights.
#[tauri::command]
pub fn is_elevated() -> Elevation {
    Elevation { elevated: elevated() }
}

#[cfg(target_os = "windows")]
pub fn elevated() -> bool {
    use std::ffi::c_void;

    #[link(name = "advapi32")]
    extern "system" {
        fn OpenProcessToken(process: *mut c_void, access: u32, token: *mut *mut c_void) -> i32;
        fn GetTokenInformation(
            token: *mut c_void,
            class: u32,
            info: *mut c_void,
            len: u32,
            ret: *mut u32,
        ) -> i32;
    }
    #[link(name = "kernel32")]
    extern "system" {
        fn GetCurrentProcess() -> *mut c_void;
        fn CloseHandle(h: *mut c_void) -> i32;
    }
    const TOKEN_QUERY: u32 = 0x0008;
    const TOKEN_ELEVATION: u32 = 20;

    unsafe {
        let mut token: *mut c_void = std::ptr::null_mut();
        if OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) == 0 {
            return false;
        }
        let mut value: u32 = 0;
        let mut ret: u32 = 0;
        let ok = GetTokenInformation(
            token,
            TOKEN_ELEVATION,
            &mut value as *mut u32 as *mut c_void,
            std::mem::size_of::<u32>() as u32,
            &mut ret,
        );
        CloseHandle(token);
        ok != 0 && value != 0
    }
}

#[cfg(not(target_os = "windows"))]
pub fn elevated() -> bool {
    false
}

/// Runs schtasks.exe without flashing a console window.
#[cfg(target_os = "windows")]
fn schtasks(args: &[&str]) -> Result<std::process::Output, String> {
    use std::os::windows::process::CommandExt;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    std::process::Command::new("schtasks.exe")
        .args(args)
        .creation_flags(CREATE_NO_WINDOW)
        .output()
        .map_err(|e| format!("could not run schtasks: {e}"))
}

/// Whether LanBaz starts at logon.
#[tauri::command]
pub async fn autostart_is_enabled() -> Result<bool, String> {
    tauri::async_runtime::spawn_blocking(autostart_query)
        .await
        .map_err(|e| e.to_string())?
}

/// Turns start-at-logon on or off.
#[tauri::command]
pub async fn autostart_set(enabled: bool) -> Result<bool, String> {
    tauri::async_runtime::spawn_blocking(move || {
        if enabled {
            autostart_enable()?;
        } else {
            autostart_disable()?;
        }
        autostart_query()
    })
    .await
    .map_err(|e| e.to_string())?
}

#[cfg(target_os = "windows")]
fn autostart_query() -> Result<bool, String> {
    let out = schtasks(&["/Query", "/TN", TASK_NAME])?;
    Ok(out.status.success())
}

#[cfg(target_os = "windows")]
fn autostart_enable() -> Result<(), String> {
    if !elevated() {
        return Err("starting with Windows needs LanBaz to run as administrator".to_string());
    }
    let exe = std::env::current_exe().map_err(|e| format!("cannot locate LanBaz: {e}"))?;
    let action = format!("\"{}\" {}", exe.display(), MINIMIZED_FLAG);
    let out = schtasks(&[
        "/Create", "/F", "/TN", TASK_NAME, "/TR", &action, "/SC", "ONLOGON", "/RL", "HIGHEST",
        "/IT",
    ])?;
    if out.status.success() {
        Ok(())
    } else {
        Err(format!(
            "could not create the logon task: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ))
    }
}

#[cfg(target_os = "windows")]
fn autostart_disable() -> Result<(), String> {
    if !autostart_query()? {
        return Ok(());
    }
    let out = schtasks(&["/Delete", "/F", "/TN", TASK_NAME])?;
    if out.status.success() {
        Ok(())
    } else {
        Err(format!(
            "could not remove the logon task: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ))
    }
}

#[cfg(not(target_os = "windows"))]
fn autostart_query() -> Result<bool, String> {
    Ok(false)
}

#[cfg(not(target_os = "windows"))]
fn autostart_enable() -> Result<(), String> {
    Err("start at logon is only available on Windows".to_string())
}

#[cfg(not(target_os = "windows"))]
fn autostart_disable() -> Result<(), String> {
    Ok(())
}

/// True when the app was launched by the logon task and should stay in the tray.
pub fn started_minimized() -> bool {
    std::env::args().any(|a| a == MINIMIZED_FLAG)
}

/// Opens a game's join link. Only known game URL schemes are allowed, and the
/// URI is restricted to a conservative character set, because it comes from a
/// remote player's presence message.
#[tauri::command]
pub fn open_game_uri(uri: String) -> Result<(), String> {
    const ALLOWED: [&str; 4] = ["steam://connect/", "steam://run/", "minecraft://", "samp://"];
    let ok_prefix = ALLOWED.iter().any(|p| uri.to_ascii_lowercase().starts_with(p));
    let ok_chars = uri.len() <= 200
        && uri
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, ':' | '/' | '.' | '-' | '_' | '?' | '=' | '&'));
    if !ok_prefix || !ok_chars {
        return Err("this join link is not allowed".to_string());
    }
    #[cfg(target_os = "windows")]
    {
        std::process::Command::new("explorer.exe")
            .arg(&uri)
            .spawn()
            .map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// Installs the bundled TAP-Windows driver for Classic LAN rooms. Runs the
/// Microsoft-signed devcon shipped with the driver; the app is elevated, so no
/// extra prompt appears. Windows may still ask the user to trust the driver
/// publisher the first time.
#[tauri::command]
pub async fn install_tap_driver(app: tauri::AppHandle) -> Result<String, String> {
    use tauri::Manager;
    let dir = app
        .path()
        .resource_dir()
        .map_err(|e| e.to_string())?
        .join("tap");
    tauri::async_runtime::spawn_blocking(move || install_tap_blocking(&dir))
        .await
        .map_err(|e| e.to_string())?
}

#[cfg(target_os = "windows")]
fn install_tap_blocking(dir: &std::path::Path) -> Result<String, String> {
    use std::os::windows::process::CommandExt;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    if !elevated() {
        return Err("installing the driver needs LanBaz to run as administrator".to_string());
    }
    let devcon = dir.join("devcon.exe");
    let inf = dir.join("OemVista.inf");
    if !devcon.exists() || !inf.exists() {
        return Err(format!("the driver files are missing from {}", dir.display()));
    }
    let out = std::process::Command::new(&devcon)
        .arg("install")
        .arg(&inf)
        .arg("tap0901")
        .current_dir(dir)
        .creation_flags(CREATE_NO_WINDOW)
        .output()
        .map_err(|e| format!("could not run devcon: {e}"))?;
    let text = format!(
        "{}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    if out.status.success() {
        Ok(text.trim().to_string())
    } else {
        Err(format!("driver installation failed: {}", text.trim()))
    }
}

#[cfg(not(target_os = "windows"))]
fn install_tap_blocking(_dir: &std::path::Path) -> Result<String, String> {
    Err("Classic LAN is only available on Windows".to_string())
}

/// Restarts LanBaz: the daemon stops cleanly (rooms close, players are told,
/// adapters are removed) and the app starts again, e.g. after a driver
/// install. Kept networks come back by themselves.
#[tauri::command]
pub async fn restart_app(app: tauri::AppHandle) {
    use tauri::Manager;
    if let Some(supervisor) = app.try_state::<std::sync::Arc<super::supervisor::Supervisor>>() {
        supervisor.suppress();
    }
    let _ = tauri::async_runtime::spawn_blocking(super::daemon::stop_blocking).await;
    super::kill_sidecars(&app);
    app.restart();
}
