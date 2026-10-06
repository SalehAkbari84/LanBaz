//! Push-to-talk: a key held anywhere, including inside a full-screen game.
//!
//! `RegisterHotKey` (see hotkey.rs) reports presses only, never releases, so it
//! cannot drive a talk button. This thread reads the key's state with
//! `GetAsyncKeyState` every 20 ms instead - the same read every voice app
//! uses; it observes no other key and injects nothing - and emits `ptt` with
//! true/false to the webviews whenever the state changes. Key 0 means off, and
//! then the thread only sleeps.

use std::sync::atomic::{AtomicU32, Ordering};

use tauri::{AppHandle, Emitter};

static KEY: AtomicU32 = AtomicU32::new(0);

/// Sets the push-to-talk key (a Windows virtual-key code); 0 turns it off.
#[tauri::command]
pub fn ptt_set_key(vk: u32) -> Result<(), String> {
    if vk > 0xFE {
        return Err(format!("not a key code: {vk}"));
    }
    KEY.store(vk, Ordering::SeqCst);
    Ok(())
}

#[cfg(target_os = "windows")]
fn key_down(vk: u32) -> bool {
    #[link(name = "user32")]
    extern "system" {
        fn GetAsyncKeyState(vk: i32) -> i16;
    }
    // The high bit is "down now".
    unsafe { GetAsyncKeyState(vk as i32) as u16 & 0x8000 != 0 }
}

#[cfg(not(target_os = "windows"))]
fn key_down(_: u32) -> bool {
    false
}

pub fn start(app: &AppHandle) {
    let app = app.clone();
    let _ = std::thread::Builder::new()
        .name("lanbaz-ptt".into())
        .spawn(move || {
            let mut down = false;
            loop {
                let vk = KEY.load(Ordering::Relaxed);
                let now = vk != 0 && key_down(vk);
                if now != down {
                    down = now;
                    let _ = app.emit("ptt", down);
                }
                let idle = if vk == 0 { 250 } else { 20 };
                std::thread::sleep(std::time::Duration::from_millis(idle));
            }
        });
}
