//! The screen-share viewer window.
//!
//! One window, label "screen": opening it again for another sharer navigates
//! it there instead of stacking windows. It loads the same UI bundle with
//! ?view=screen and runs the receiving end of the WebRTC connection itself,
//! because a media stream cannot cross from one webview to another.

use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder};

const LABEL: &str = "screen";

/// Characters a room id, peer id or display name may carry into the URL are
/// percent-encoded; anything else is dropped.
fn enc(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes().take(256) {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

fn viewer_path(room: &str, peer: &str, name: &str) -> String {
    format!("index.html?view=screen&room={}&peer={}&name={}", enc(room), enc(peer), enc(name))
}

#[tauri::command]
pub async fn open_screen_viewer(app: AppHandle, room: String, peer: String, name: String) -> Result<(), String> {
    let path = viewer_path(&room, &peer, &name);
    let title = format!("{name} — LanBaz");
    if let Some(w) = app.get_webview_window(LABEL) {
        let url = w.url().map_err(|e| e.to_string())?.join(&format!("/{path}")).map_err(|e| e.to_string())?;
        w.navigate(url).map_err(|e| e.to_string())?;
        let _ = w.set_title(&title);
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
        return Ok(());
    }
    WebviewWindowBuilder::new(&app, LABEL, WebviewUrl::App(path.into()))
        .title(title)
        .inner_size(1280.0, 760.0)
        .min_inner_size(480.0, 300.0)
        .resizable(true)
        .center()
        .build()
        .map(|_| ())
        .map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::viewer_path;

    #[test]
    fn viewer_url_is_encoded() {
        assert_eq!(
            viewer_path("lbzroom-abc", "5232", "Ali & Co?"),
            "index.html?view=screen&room=lbzroom-abc&peer=5232&name=Ali%20%26%20Co%3F"
        );
        // Persian names survive as UTF-8 percent escapes.
        assert!(viewer_path("r", "p", "علی").ends_with("name=%D8%B9%D9%84%DB%8C"));
    }
}
