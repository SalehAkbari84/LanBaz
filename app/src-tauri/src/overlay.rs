//! The in-game overlay window.
//!
//! A second webview that floats above the game: a compact room list, the virtual
//! LAN state, and the peer addresses a user types into a game's server box. It
//! renders the same React app as the main window, from the same daemon
//! connection, so nothing about the protocol is duplicated here — this module
//! only decides when the window is visible and whether it eats mouse clicks.
//!
//! Deliberately *not* an injected overlay. Injecting a DLL into another process
//! is how overlay software gets flagged by anti-cheat, and a gaming VPN that
//! kicks you out of the game is worse than no overlay at all. This window is an
//! ordinary top-level window:
//!
//!   * `always_on_top` keeps it above a borderless-fullscreen game.
//!   * `skip_taskbar` keeps it out of the taskbar and Alt+Tab.
//!   * `ignore_cursor_events` makes it click-through while it is up, so it never
//!     steals a click meant for the game. The webview turns this off for as long
//!     as the pointer is over the panel.
//!
//! The limit worth knowing: Windows hands the display to a DirectX exclusive
//! fullscreen game, and no ordinary window can stay above it. Borderless
//! fullscreen and windowed modes, which is what nearly every current title uses,
//! are unaffected. That trade is the price of not injecting.

use std::sync::atomic::{AtomicU8, Ordering};

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager, PhysicalPosition};

use crate::hotkey;

/// Label of the overlay window, matching tauri.conf.json.
pub const OVERLAY_LABEL: &str = "overlay";

/// Emitted to both webviews when the hotkey could not be claimed, so the UI can
/// say so instead of leaving a key that does nothing.
pub const HOTKEY_EVENT: &str = "lanbaz://hotkey";

/// Emitted to the overlay window whenever its visibility or mode changes, so the
/// webview can render the right thing without polling.
pub const MODE_EVENT: &str = "lanbaz://overlay-mode";

/// How the overlay is presenting itself.
#[derive(Debug, Clone, Copy, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum OverlayMode {
    /// Hidden entirely.
    Hidden,
    /// On screen but click-through and unfocused: a notification that cannot
    /// interrupt the game. Auto-dismisses in the webview.
    Toast,
    /// Interactive: the user asked for it, via the hotkey or the tray.
    Panel,
}

/// What the webview needs to describe the hotkey honestly.
#[derive(Debug, Clone, Serialize)]
pub struct OverlayStatus {
    /// Whether the shortcut is actually registered.
    pub registered: bool,
    /// The combination, for display.
    pub keys: String,
    /// The chat combination, for display.
    pub chat_keys: String,
    /// Why it is not registered, if it is not.
    pub error: Option<String>,
}

/// The mode the overlay is in right now, so the hotkey can tell a toast (which
/// should turn into the panel) from the panel (which should close).
static MODE: AtomicU8 = AtomicU8::new(0);

fn mode_code(mode: OverlayMode) -> u8 {
    match mode {
        OverlayMode::Hidden => 0,
        OverlayMode::Toast => 1,
        OverlayMode::Panel => 2,
    }
}

/// Puts the overlay in the top-right corner of the primary monitor, clear of
/// the minimap and chat most games keep bottom-left.
fn place(app: &AppHandle) {
    let Some(window) = app.get_webview_window(OVERLAY_LABEL) else {
        return;
    };
    let Ok(Some(monitor)) = window.primary_monitor() else {
        return;
    };
    let Ok(size) = window.outer_size() else {
        return;
    };
    let origin = monitor.position();
    let area = monitor.size();
    let margin = (24.0 * monitor.scale_factor()) as i32;
    let x = origin.x + area.width as i32 - size.width as i32 - margin;
    let y = origin.y + margin;
    let _ = window.set_position(PhysicalPosition::new(x.max(origin.x), y));
}

/// Wires the hotkey thread. Errors are reported, never fatal: the tray item and
/// the main window still work without a shortcut.
pub fn start(app: &AppHandle) {
    place(app);
    let handle = app.clone();
    let reporter = app.clone();
    let joined = hotkey::spawn(move |key| match key {
        hotkey::Hotkey::Overlay => toggle(&handle),
        hotkey::Hotkey::Chat => open_chat(&handle),
    });

    let Ok(thread) = joined else {
        report(
            &reporter,
            "could not start the hotkey thread; use the tray menu instead".to_string(),
        );
        return;
    };

    tauri::async_runtime::spawn(async move {
        if let Ok(Err(error)) = thread.join() {
            report(&reporter, error);
        }
    });
}

/// Shows the overlay if hidden, hides it if shown. This is the hotkey and tray
/// path, so it always means "the user wants the panel".
///
/// Focus is taken only when showing, and only for the panel: a toast appears
/// behind the game without pulling focus away from it, because stealing focus
/// mid-match is the single most disruptive thing an overlay can do.
pub fn toggle(app: &AppHandle) {
    let visible = app
        .get_webview_window(OVERLAY_LABEL)
        .and_then(|w| w.is_visible().ok())
        .unwrap_or(false);
    // A toast on screen is not "the overlay is open": the user pressing the
    // key while a notification shows wants the panel, not for it to vanish.
    if visible && MODE.load(Ordering::SeqCst) == mode_code(OverlayMode::Toast) {
        apply(app, true, OverlayMode::Panel);
        return;
    }
    apply(app, !visible, if visible { OverlayMode::Hidden } else { OverlayMode::Panel });
}

/// Emitted to the overlay when the chat hotkey asks for the chat box.
pub const CHAT_EVENT: &str = "lanbaz://overlay-chat";

/// Shows the panel and focuses its chat input.
pub fn open_chat(app: &AppHandle) {
    apply(app, true, OverlayMode::Panel);
    if let Some(window) = app.get_webview_window(OVERLAY_LABEL) {
        let _ = window.emit_to(OVERLAY_LABEL, CHAT_EVENT, ());
    }
}

/// Base overlay size in logical pixels, before the user's scale.
const BASE_W: f64 = 360.0;
const BASE_H: f64 = 560.0;

/// Moves and resizes the overlay: corner of the primary monitor, and size from
/// the user's scale preference. Called by the webview when the preference
/// changes.
#[tauri::command]
pub fn set_overlay_layout(app: AppHandle, corner: String, scale: f64) {
    let Some(window) = app.get_webview_window(OVERLAY_LABEL) else {
        return;
    };
    let Ok(Some(monitor)) = window.primary_monitor() else {
        return;
    };
    let scale = scale.clamp(0.8, 1.3);
    let sf = monitor.scale_factor();
    let w = (BASE_W * scale * sf) as i32;
    let h = (BASE_H * scale * sf) as i32;
    let _ = window.set_size(tauri::PhysicalSize::new(w as u32, h as u32));
    let origin = monitor.position();
    let area = monitor.size();
    let margin = (20.0 * sf) as i32;
    let right = origin.x + area.width as i32 - w - margin;
    let left = origin.x + margin;
    let top = origin.y + margin;
    // Leave room for the taskbar at the bottom.
    let bottom = origin.y + area.height as i32 - h - margin - (48.0 * sf) as i32;
    let (x, y) = match corner.as_str() {
        "top-left" => (left, top),
        "bottom-left" => (left, bottom),
        "bottom-right" => (right, bottom),
        _ => (right, top),
    };
    let _ = window.set_position(PhysicalPosition::new(x, y.max(origin.y)));
}

/// The single place that changes the overlay's state.
///
/// Both the tray and the webviews go through here so the window flags and the
/// mode event can never disagree: a window that says "panel" but still passes
/// clicks through would swallow the user's shots.
fn apply(app: &AppHandle, visible: bool, mode: OverlayMode) {
    let Some(window) = app.get_webview_window(OVERLAY_LABEL) else {
        return;
    };

    if visible {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_always_on_top(true);
        match mode {
            OverlayMode::Panel => {
                let _ = window.set_ignore_cursor_events(false);
                let _ = window.set_focus();
            }
            OverlayMode::Toast => {
                // A toast must never eat a click. Mouse input passes straight
                // through to the game underneath, which is the whole point of
                // announcing something without interrupting play.
                let _ = window.set_ignore_cursor_events(true);
            }
            OverlayMode::Hidden => {}
        }
    } else {
        let _ = window.hide();
    }

    MODE.store(mode_code(if visible { mode } else { OverlayMode::Hidden }), Ordering::SeqCst);
    let _ = window.emit_to(OVERLAY_LABEL, MODE_EVENT, mode);
}

fn report(app: &AppHandle, error: String) {
    eprintln!("lanbaz: {error}");
    let _ = app.emit(HOTKEY_EVENT, status_with_error(error));
}

fn status_with_error(error: String) -> OverlayStatus {
    OverlayStatus {
        registered: false,
        keys: hotkey::HOTKEY_LABEL.to_string(),
        chat_keys: hotkey::CHAT_HOTKEY_LABEL.to_string(),
        error: Some(error),
    }
}

/// The real registration result. Before the thread has finished registering -
/// a few milliseconds after startup - it reports registered, so the UI does not
/// flash an error that resolves on its own.
fn status() -> OverlayStatus {
    let (registered, error) = hotkey::status().unwrap_or((true, None));
    OverlayStatus {
        registered,
        keys: hotkey::HOTKEY_LABEL.to_string(),
        chat_keys: hotkey::CHAT_HOTKEY_LABEL.to_string(),
        error,
    }
}

#[tauri::command]
pub fn overlay_status() -> OverlayStatus {
    status()
}

/// Whether the overlay is on screen.
///
/// Asked by the *other* window: the main window wants to know whether to raise
/// it, and the overlay itself wants to know whether it is already showing. So it
/// resolves the overlay by label rather than by who called, otherwise the main
/// window would always be told "hidden" and would raise a toast on top of a
/// panel the user is in the middle of clicking.
#[tauri::command]
pub fn overlay_is_visible(app: AppHandle) -> bool {
    app.get_webview_window(OVERLAY_LABEL)
        .and_then(|w| w.is_visible().ok())
        .unwrap_or(false)
}

/// Shows or hides the overlay on request from the webview.
///
/// `mode` decides whether the overlay is a full interactive panel or a
/// click-through toast. A missing mode means panel, because every caller that
/// shows the overlay wants to use it; the toast is opt-in.
#[tauri::command]
pub fn set_overlay_visible(app: AppHandle, visible: bool, mode: Option<String>) {
    let mode = match (visible, mode.as_deref()) {
        (false, _) => OverlayMode::Hidden,
        (true, Some("toast")) => OverlayMode::Toast,
        (true, _) => OverlayMode::Panel,
    };
    apply(&app, visible, mode);
}