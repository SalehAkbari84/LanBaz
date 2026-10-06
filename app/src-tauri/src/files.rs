//! Shows a received file in Explorer.
//!
//! Only files inside Downloads\LanBaz, where the daemon saves what friends
//! send: the webview must not be able to open Explorer anywhere else, and it
//! never opens or runs the file itself.

use std::path::{Path, PathBuf};

fn received_dir() -> Option<PathBuf> {
    std::env::var_os("USERPROFILE").map(|home| Path::new(&home).join("Downloads").join("LanBaz"))
}

#[tauri::command]
pub fn reveal_received(path: String) -> Result<(), String> {
    let dir = received_dir()
        .and_then(|d| d.canonicalize().ok())
        .ok_or("the LanBaz downloads folder does not exist yet")?;
    let file = Path::new(&path)
        .canonicalize()
        .map_err(|_| "that file is gone".to_string())?;
    if !file.starts_with(&dir) || !file.is_file() {
        return Err("only files LanBaz received can be shown".into());
    }
    // canonicalize() yields a \\?\ path, which Explorer does not accept.
    let shown = file.to_string_lossy().trim_start_matches(r"\\?\").to_string();
    std::process::Command::new("explorer.exe")
        .arg(format!("/select,{shown}"))
        .spawn()
        .map(|_| ())
        .map_err(|e| e.to_string())
}
