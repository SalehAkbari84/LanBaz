// LanBaz desktop shell.
//
// Responsibilities, and nothing else:
//   * window, tray, autostart
//   * start/stop the lanbazd sidecar and keep it alive
//   * hand the webview the *location* of the control API
//
// The shell never speaks the control protocol itself. It reads
// <state-dir>/daemon.json, which the daemon owns, and passes the address plus
// the token to the React layer, which does the WebSocket work. That keeps every
// protocol concern in one place (the TypeScript DaemonClient) and keeps Rust out
// of the networking business, as required by the architecture rules.

mod daemon;
mod files;
mod hotkey;
mod invite;
mod overlay;
mod ptt;
mod singleinstance;
mod supervisor;
mod system;
mod updater;

use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

use tauri::{
    menu::{Menu, MenuItem, PredefinedMenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    Emitter, Manager,
};
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;

/// Sidecar children keyed by binary name, so the tray can stop the daemon.
#[derive(Clone, Default)]
struct DaemonProcesses(Arc<Mutex<HashMap<String, CommandChild>>>);

impl DaemonProcesses {
    /// Records a child. A previous child under the same name is killed rather
    /// than dropped: losing its handle would leave a daemon nobody can stop.
    fn insert(&self, name: &str, child: CommandChild) {
        let mut guard = self.0.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(old) = guard.insert(name.to_string(), child) {
            let _ = old.kill();
        }
    }

    /// Takes every child out of the map and kills it.
    fn kill_all(&self) {
        let taken = {
            let mut guard = self.0.lock().unwrap_or_else(|e| e.into_inner());
            std::mem::take(&mut *guard)
        };
        for (_, child) in taken {
            let _ = child.kill();
        }
    }
}

/// Kills every sidecar this shell started.
pub(crate) fn kill_sidecars(app: &tauri::AppHandle) {
    app.state::<DaemonProcesses>().kill_all();
}

/// Start-time arguments for the daemon sidecar.
#[derive(Clone)]
struct DaemonArgs {
    log_level: String,
}

/// Notes on why there is no orphan cleanup here.
///
/// A guard on the shell's own exit was tried first and removed: `Drop` only runs
/// on an unwind, so a forced kill (Task Manager, a panic, a power event) skips it
/// entirely. No code inside a killed process can act after it dies.
///
/// The guarantee therefore lives in the daemon, which polls its parent and
/// stops itself when that parent is gone (`--managed-by-shell`,
/// `core/internal/daemon/parent_watch.go`). A daemon started by hand leaves the
/// flag unset and is left alone.
impl Default for DaemonArgs {
    fn default() -> Self {
        Self {
            log_level: "info".to_string(),
        }
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    // Before anything else: a second shell would race the first for the daemon,
    // the tray icon and the hotkey. It asks the running one to show its window
    // instead - a double-click on the shortcut must never look like nothing
    // happened.
    let incoming = invite::code_from_args();
    if !singleinstance::acquire() {
        // A lanbaz:// link or .lanbaz file opened while LanBaz runs: hand the
        // code to the running instance rather than dropping it.
        if let Some(code) = &incoming {
            invite::write_handoff(code);
        }
        singleinstance::signal_existing();
        return;
    }

    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .manage(updater::Pending::default())
        .manage(updater::Downloaded::default())
        .manage(DaemonProcesses::default())
        .manage(DaemonArgs::default())
        .invoke_handler(tauri::generate_handler![
            daemon::daemon_endpoint,
            daemon::start_daemon,
            daemon::stop_daemon,
            daemon::daemon_state_dir,
            overlay::overlay_status,
            overlay::set_overlay_visible,
            overlay::overlay_is_visible,
            overlay::set_overlay_layout,
            system::is_elevated,
            system::autostart_is_enabled,
            system::autostart_set,
            invite::take_pending_invite,
            invite::read_clipboard_text,
            invite::save_invite_file,
            system::open_game_uri,
            system::install_tap_driver,
            system::restart_app,
            updater::update_status,
            updater::update_set_repo,
            updater::update_check,
            updater::update_install,
            updater::update_download,
            updater::update_apply,
            ptt::ptt_set_key,
            files::reveal_received,
            files::pick_files,
            tray_set_networks,
        ])
        .setup(move |app| {
            build_tray(app.handle())?;
            overlay::start(app.handle());
            ptt::start(app.handle());
            supervisor::start(app.handle());
            let handle = app.handle().clone();
            singleinstance::listen(move || {
                show_main_window(&handle);
                invite::deliver_handoff(&handle);
            });
            std::thread::spawn(invite::register_handlers);
            if let Some(code) = incoming.clone() {
                invite::deliver(app.handle(), code);
            } else if system::started_minimized() {
                // Launched by the logon task: the daemon starts, the window
                // stays in the tray until the user asks for it.
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.hide();
                }
            }
            Ok(())
        })
        .on_window_event(|window, event| {
            // Closing the window is not quitting the app: a game is running,
            // the daemon is serving it, and the user asked for neither to stop.
            // The window goes to the tray; Quit stays explicit, in the menu.
            if window.label() == "main" {
                if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                    let _ = window.hide();
                    api.prevent_close();
                }
            }
        })
        .run(tauri::generate_context!())
        .expect("error while running the LanBaz shell");
}

/// One line of the tray's network list, sent by the UI (`tray_set_networks`).
#[derive(Clone, serde::Deserialize)]
pub struct TrayNet {
    /// The open room it stands for, if any; clicking it opens that room.
    room: Option<String>,
    label: String,
}

/// The tray menu: the kept networks (and open rooms) first, then the actions.
fn tray_menu(app: &tauri::AppHandle, nets: &[TrayNet]) -> tauri::Result<Menu<tauri::Wry>> {
    let show = MenuItem::with_id(app, "show", "Show LanBaz", true, None::<&str>)?;
    let overlay_label = format!("Overlay ({})", hotkey::HOTKEY_LABEL);
    let overlay = MenuItem::with_id(app, "overlay", &overlay_label, true, None::<&str>)?;
    let start = MenuItem::with_id(app, "start", "Start daemon", true, None::<&str>)?;
    let stop = MenuItem::with_id(app, "stop", "Stop daemon", true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", "Quit LanBaz", true, None::<&str>)?;

    let menu = Menu::new(app)?;
    if !nets.is_empty() {
        let title = MenuItem::with_id(app, "nets", "Networks", false, None::<&str>)?;
        menu.append(&title)?;
        for (i, n) in nets.iter().take(12).enumerate() {
            let id = match &n.room {
                Some(room) => format!("net:{room}"),
                None => format!("netinfo:{i}"),
            };
            menu.append(&MenuItem::with_id(app, id, &n.label, true, None::<&str>)?)?;
        }
        menu.append(&PredefinedMenuItem::separator(app)?)?;
    }
    menu.append(&show)?;
    menu.append(&PredefinedMenuItem::separator(app)?)?;
    menu.append(&overlay)?;
    menu.append(&PredefinedMenuItem::separator(app)?)?;
    menu.append(&start)?;
    menu.append(&stop)?;
    menu.append(&PredefinedMenuItem::separator(app)?)?;
    menu.append(&quit)?;
    Ok(menu)
}

/// Replaces the tray's network list.
#[tauri::command]
fn tray_set_networks(app: tauri::AppHandle, nets: Vec<TrayNet>) -> Result<(), String> {
    let tray = app.tray_by_id("lanbaz-tray").ok_or("no tray icon")?;
    let menu = tray_menu(&app, &nets).map_err(|e| e.to_string())?;
    tray.set_menu(Some(menu)).map_err(|e| e.to_string())?;
    let tip = if nets.is_empty() {
        "LanBaz".to_string()
    } else {
        let lines: Vec<&str> = nets.iter().take(4).map(|n| n.label.as_str()).collect();
        format!("LanBaz\n{}", lines.join("\n"))
    };
    tray.set_tooltip(Some(tip)).map_err(|e| e.to_string())
}

fn build_tray(app: &tauri::AppHandle) -> Result<(), Box<dyn std::error::Error>> {
    let menu = tray_menu(app, &[])?;

    let icon = app
        .default_window_icon()
        .cloned()
        .ok_or("no default window icon is configured; the bundle icons are missing")?;

    TrayIconBuilder::with_id("lanbaz-tray")
        .icon(icon)
        .icon_as_template(false)
        .tooltip("LanBaz")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id.as_ref() {
            "show" => show_main_window(app),
            "overlay" => overlay::toggle(app),
            "start" => {
                let handle = app.clone();
                tauri::async_runtime::spawn(async move {
                    if let Err(err) = daemon::start_daemon(handle).await {
                        eprintln!("lanbaz: could not start the daemon: {err}");
                    }
                });
            }
            "stop" => {
                let handle = app.clone();
                tauri::async_runtime::spawn(async move {
                    if let Err(err) = daemon::stop_daemon(handle).await {
                        eprintln!("lanbaz: could not stop the daemon cleanly: {err}");
                    }
                });
            }
            "quit" => {
                // Quitting the app stops the daemon: it is a sidecar of this
                // process and must not outlive it. The graceful path runs
                // first so the adapter, routes and firewall rule are removed.
                let handle = app.clone();
                tauri::async_runtime::spawn(async move {
                    let _ = daemon::stop_daemon(handle.clone()).await;
                    handle.exit(0);
                });
            }
            id if id.starts_with("net:") || id.starts_with("netinfo:") => {
                show_main_window(app);
                let room = id.strip_prefix("net:").unwrap_or("").to_string();
                let _ = app.emit("tray-open-network", room);
            }
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main_window(tray.app_handle());
            }
        })
        .build(app)?;

    Ok(())
}

fn show_main_window(app: &tauri::AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

pub(crate) fn spawn_sidecar(app: &tauri::AppHandle) -> Result<(), Box<dyn std::error::Error>> {
    let level = app.state::<DaemonArgs>().log_level.clone();
    let command = app
        .shell()
        .sidecar("lanbazd")?
        // managed-by-shell tells the daemon to record itself as our child, which
        // is what lets the orphan guard clean up after a forced exit without
        // touching a daemon the user started by hand.
        .args(["--log-level", level.as_str(), "--managed-by-shell"]);

    let (mut rx, child) = command.spawn()?;
    app.state::<DaemonProcesses>().insert("lanbazd", child);

    // Forward the daemon output so a developer sees the same lines a terminal
    // user would. Secrets are already redacted on the daemon side.
    tauri::async_runtime::spawn(async move {
        while let Some(event) = rx.recv().await {
            match event {
                CommandEvent::Stdout(bytes) => print!("{}", String::from_utf8_lossy(&bytes)),
                CommandEvent::Stderr(bytes) => eprint!("{}", String::from_utf8_lossy(&bytes)),
                CommandEvent::Terminated(payload) => {
                    eprintln!("lanbaz: daemon exited with code {:?}", payload.code);
                    break;
                }
                CommandEvent::Error(err) => {
                    eprintln!("lanbaz: daemon error: {err}");
                    break;
                }
                _ => {}
            }
        }
    });

    Ok(())
}
