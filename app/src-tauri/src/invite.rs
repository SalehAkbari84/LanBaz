//! Getting a pairing code into LanBaz without copy-and-paste gymnastics.
//!
//! LanBaz has no server, so an invite and a reply still have to travel between
//! friends - but they can travel as a click instead of a 2000-character paste:
//!
//!   * `lanbaz://join/<code>` links, registered as a URL protocol, and
//!   * `.lanbaz` files, registered as a file type - an invite saved as a small
//!     file sends fine on Telegram or Discord, which cap message length.
//!
//! Either one launches LanBaz with the code as an argument. When LanBaz is
//! already running, the new process writes the code to a handoff file and
//! signals the running instance (see singleinstance.rs), which then shows it.
//! The clipboard is a third route: the UI asks for its text when the window
//! gains focus and offers to use a code it finds there.

use std::path::{Path, PathBuf};
use std::sync::Mutex;

use serde::Serialize;
use tauri::{AppHandle, Emitter};

use crate::daemon::state_dir;

/// Emitted to the webviews when a code arrives from a link or a file.
pub const EVENT: &str = "lanbaz://invite";

/// Codes are a few kilobytes; anything far larger is not one.
const MAX_CODE_LEN: usize = 64 * 1024;

/// A code received before the webview was ready to listen.
static PENDING: Mutex<Option<String>> = Mutex::new(None);

#[derive(Debug, Clone, Serialize)]
pub struct Incoming {
    pub code: String,
}

/// Extracts a code from a command line argument: a lanbaz:// URL, a path to a
/// .lanbaz file, or a bare code.
pub fn code_from_arg(arg: &str) -> Option<String> {
    let arg = arg.trim().trim_matches('"');
    let lower = arg.to_ascii_lowercase();
    if let Some(rest) = lower.strip_prefix("lanbaz://") {
        // Keep the original casing of the code part; the prefix is ASCII.
        let tail = &arg[arg.len() - rest.len()..];
        let tail = tail
            .trim_start_matches("join/")
            .trim_start_matches("reply/")
            .trim_end_matches('/');
        return clean(&percent_decode(tail));
    }
    if lower.ends_with(".lanbaz") {
        let text = std::fs::read_to_string(Path::new(arg)).ok()?;
        return clean(&text);
    }
    if lower.starts_with("lbz-") {
        return clean(arg);
    }
    None
}

fn clean(s: &str) -> Option<String> {
    let s = s.trim();
    // A saved file may hold the link form; accept both.
    let s = s
        .strip_prefix("lanbaz://join/")
        .or_else(|| s.strip_prefix("lanbaz://reply/"))
        .unwrap_or(s)
        .trim();
    if s.is_empty() || s.len() > MAX_CODE_LEN || !s.to_ascii_lowercase().starts_with("lbz-") {
        return None;
    }
    Some(s.to_string())
}

fn percent_decode(s: &str) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let Ok(v) = u8::from_str_radix(&s[i + 1..i + 3], 16) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The code passed to this process, if any.
pub fn code_from_args() -> Option<String> {
    std::env::args().skip(1).find_map(|a| code_from_arg(&a))
}

fn handoff_path() -> PathBuf {
    state_dir().join("handoff.txt")
}

/// Second instance: leave the code for the running one.
pub fn write_handoff(code: &str) {
    let _ = std::fs::create_dir_all(state_dir());
    let _ = std::fs::write(handoff_path(), code);
}

/// First instance: pick up a code a later launch left, and tell the UI.
pub fn deliver_handoff(app: &AppHandle) {
    let path = handoff_path();
    if let Ok(text) = std::fs::read_to_string(&path) {
        let _ = std::fs::remove_file(&path);
        if let Some(code) = clean(&text) {
            deliver(app, code);
        }
    }
}

/// Hands a code to the UI now, and keeps it for a webview that has not
/// subscribed yet.
pub fn deliver(app: &AppHandle, code: String) {
    *PENDING.lock().unwrap_or_else(|e| e.into_inner()) = Some(code.clone());
    let _ = app.emit(EVENT, Incoming { code });
}

/// The UI asks for a code that arrived before it was listening.
#[tauri::command]
pub fn take_pending_invite() -> Option<String> {
    PENDING.lock().unwrap_or_else(|e| e.into_inner()).take()
}

/// Reads plain text from the Windows clipboard, for the "use the code you just
/// copied?" prompt. Returns None when the clipboard holds no text.
#[tauri::command]
pub fn read_clipboard_text() -> Option<String> {
    clipboard::read()
}

/// Saves a code as a .lanbaz file in the user's Downloads folder and shows it
/// in Explorer, ready to drag into a chat.
#[tauri::command]
pub fn save_invite_file(code: String, label: String) -> Result<String, String> {
    if clean(&code).is_none() {
        return Err("that is not a LanBaz code".to_string());
    }
    let dir = downloads_dir();
    std::fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
    let safe: String = label
        .chars()
        .map(|c| if c.is_alphanumeric() || c == '-' || c == '_' { c } else { '-' })
        .take(40)
        .collect();
    let name = if safe.trim_matches('-').is_empty() {
        "LanBaz-invite".to_string()
    } else {
        format!("LanBaz-{}", safe.trim_matches('-'))
    };
    let mut path = dir.join(format!("{name}.lanbaz"));
    let mut n = 2;
    while path.exists() {
        path = dir.join(format!("{name}-{n}.lanbaz"));
        n += 1;
    }
    std::fs::write(&path, code.trim()).map_err(|e| e.to_string())?;
    reveal(&path);
    Ok(path.to_string_lossy().into_owned())
}

fn downloads_dir() -> PathBuf {
    std::env::var("USERPROFILE")
        .map(|home| PathBuf::from(home).join("Downloads"))
        .unwrap_or_else(|_| state_dir())
}

#[cfg(target_os = "windows")]
fn reveal(path: &Path) {
    let _ = std::process::Command::new("explorer.exe")
        .arg(format!("/select,{}", path.display()))
        .spawn();
}

#[cfg(not(target_os = "windows"))]
fn reveal(_path: &Path) {}

/// Registers lanbaz:// and .lanbaz for the current user. Idempotent; runs at
/// every start so a moved install keeps working.
#[cfg(target_os = "windows")]
pub fn register_handlers() {
    use std::os::windows::process::CommandExt;
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    let Ok(exe) = std::env::current_exe() else {
        return;
    };
    let command = format!("\"{}\" \"%1\"", exe.display());
    let icon = format!("\"{}\",0", exe.display());
    let entries: [(&str, &str, &str); 7] = [
        (r"HKCU\Software\Classes\lanbaz", "", "URL:LanBaz invite"),
        (r"HKCU\Software\Classes\lanbaz", "URL Protocol", ""),
        (r"HKCU\Software\Classes\lanbaz\DefaultIcon", "", &icon),
        (r"HKCU\Software\Classes\lanbaz\shell\open\command", "", &command),
        (r"HKCU\Software\Classes\.lanbaz", "", "LanBaz.Invite"),
        (r"HKCU\Software\Classes\LanBaz.Invite", "", "LanBaz invite"),
        (r"HKCU\Software\Classes\LanBaz.Invite\shell\open\command", "", &command),
    ];
    for (key, name, value) in entries {
        let mut cmd = std::process::Command::new("reg.exe");
        cmd.arg("add").arg(key);
        if name.is_empty() {
            cmd.arg("/ve");
        } else {
            cmd.args(["/v", name]);
        }
        cmd.args(["/t", "REG_SZ", "/d", value, "/f"]).creation_flags(CREATE_NO_WINDOW);
        let _ = cmd.output();
    }
}

#[cfg(not(target_os = "windows"))]
pub fn register_handlers() {}

#[cfg(target_os = "windows")]
mod clipboard {
    use std::ffi::c_void;

    #[link(name = "user32")]
    extern "system" {
        fn OpenClipboard(hwnd: *mut c_void) -> i32;
        fn CloseClipboard() -> i32;
        fn GetClipboardData(format: u32) -> *mut c_void;
        fn IsClipboardFormatAvailable(format: u32) -> i32;
    }
    #[link(name = "kernel32")]
    extern "system" {
        fn GlobalLock(h: *mut c_void) -> *mut c_void;
        fn GlobalUnlock(h: *mut c_void) -> i32;
        fn GlobalSize(h: *mut c_void) -> usize;
    }
    const CF_UNICODETEXT: u32 = 13;

    pub fn read() -> Option<String> {
        unsafe {
            if IsClipboardFormatAvailable(CF_UNICODETEXT) == 0 || OpenClipboard(std::ptr::null_mut()) == 0 {
                return None;
            }
            let mut out = None;
            let h = GetClipboardData(CF_UNICODETEXT);
            if !h.is_null() {
                let p = GlobalLock(h) as *const u16;
                if !p.is_null() {
                    let max = GlobalSize(h) / 2;
                    let mut len = 0;
                    while len < max && *p.add(len) != 0 {
                        len += 1;
                    }
                    // Only short-ish text is interesting; a code is a few KB.
                    if len <= 128 * 1024 {
                        out = Some(String::from_utf16_lossy(std::slice::from_raw_parts(p, len)));
                    }
                    GlobalUnlock(h);
                }
            }
            CloseClipboard();
            out
        }
    }
}

#[cfg(not(target_os = "windows"))]
mod clipboard {
    pub fn read() -> Option<String> {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::code_from_arg;

    #[test]
    fn parses_links_and_bare_codes() {
        assert_eq!(code_from_arg("lanbaz://join/LBZ-abcd-efgh").as_deref(), Some("LBZ-abcd-efgh"));
        assert_eq!(code_from_arg("\"lanbaz://join/LBZ-abcd/\"").as_deref(), Some("LBZ-abcd"));
        assert_eq!(code_from_arg("LBZ-abcd").as_deref(), Some("LBZ-abcd"));
        assert_eq!(code_from_arg("--minimized"), None);
        assert_eq!(code_from_arg("lanbaz://join/"), None);
    }
}
