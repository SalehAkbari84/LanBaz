//! The global hotkey that toggles the in-game overlay.
//!
//! Why a raw Win32 call instead of a plugin: the shell must build offline, and
//! this is the whole of what a plugin would do — register one key combination,
//! pump one message loop. Anything else is dependency weight for four lines of
//! FFI.
//!
//! Why a dedicated thread instead of the overlay window's own handle: `WM_HOTKEY`
//! is posted to whatever window handle was passed in, and tao does not surface
//! messages it does not recognise, so a hotkey aimed at the overlay window would
//! be delivered into a queue nobody reads. Registering with a null handle posts
//! the message to the *calling thread's* queue instead, which this thread owns
//! end to end. That also means the hotkey still fires while the overlay is
//! hidden, which is the case that matters most.
//!
//! Registering can legitimately fail: another application may already own
//! Ctrl+Alt+L. That is reported rather than hidden, because a hotkey that silently
//! does nothing is indistinguishable from a broken feature.

/// What the user presses. Kept in one place so the UI, the tray menu and the docs
/// cannot drift apart.
pub const HOTKEY_LABEL: &str = "Ctrl+Alt+L";

/// Opens the overlay with the chat box focused.
pub const CHAT_HOTKEY_LABEL: &str = "Ctrl+Alt+C";

/// Which key fired.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Hotkey {
    Overlay,
    Chat,
}

/// Whether the hotkey is registered, and why not if it is not. Written by the
/// hotkey thread, read by the overlay_status command.
static STATUS: std::sync::Mutex<Option<(bool, Option<String>)>> = std::sync::Mutex::new(None);

fn set_status(registered: bool, error: Option<String>) {
    *STATUS.lock().unwrap_or_else(|e| e.into_inner()) = Some((registered, error));
}

/// The registration result, or None while the thread is still starting.
pub fn status() -> Option<(bool, Option<String>)> {
    STATUS.lock().unwrap_or_else(|e| e.into_inner()).clone()
}

/// Starts the hotkey thread and runs until the process exits.
///
/// Returns the join handle so the caller can surface a registration failure; the
/// thread itself only returns when the message loop ends.
#[cfg(target_os = "windows")]
pub fn spawn<F>(
    on_trigger: F,
) -> Result<std::thread::JoinHandle<Result<(), String>>, String>
where
    F: Fn(Hotkey) + Send + 'static,
{
    std::thread::Builder::new()
        .name("lanbaz-hotkey".into())
        .spawn(move || run(on_trigger))
        .map_err(|e| e.to_string())
}

#[cfg(target_os = "windows")]
fn run<F>(on_trigger: F) -> Result<(), String>
where
    F: Fn(Hotkey) + Send + 'static,
{
    use std::ffi::c_void;

    #[repr(C)]
    struct Msg {
        hwnd: *mut c_void,
        message: u32,
        wparam: usize,
        lparam: isize,
        time: u32,
        pt_x: i32,
        pt_y: i32,
    }

    // u32::MAX means "any message", which is what we want: the loop stays
    // responsive to thread messages as well as the hotkey.
    const WM_HOTKEY: u32 = 0x0312;
    const HOTKEY_ID: i32 = 0xBA2;
    const CHAT_ID: i32 = 0xBA3;
    const MOD_ALT: u32 = 0x0001;
    const MOD_CONTROL: u32 = 0x0002;
    const MOD_NOREPEAT: u32 = 0x4000;
    const VK_L: u32 = 0x4C;
    const VK_C: u32 = 0x43;

    #[link(name = "user32")]
    extern "system" {
        fn RegisterHotKey(hwnd: *mut c_void, id: i32, fs_modifiers: u32, vk: u32) -> i32;
        fn UnregisterHotKey(hwnd: *mut c_void, id: i32) -> i32;
        fn GetMessageW(lp_msg: *mut Msg, hwnd: *mut c_void, min: u32, max: u32) -> i32;
    }

    unsafe {
        let registered = RegisterHotKey(
            std::ptr::null_mut(),
            HOTKEY_ID,
            MOD_CONTROL | MOD_ALT | MOD_NOREPEAT,
            VK_L,
        );
        // RegisterHotKey returns nonzero on success. Reading it the other way
        // round once made a working key look taken and a taken key look
        // working, and the dead thread kept the combination from everyone.
        if registered == 0 {
            set_status(false, Some(format!(
                "{HOTKEY_LABEL} is already taken by another application"
            )));
            return Err(format!(
                "{HOTKEY_LABEL} is already taken by another application"
            ));
        }
        set_status(true, None);
        // The chat key is a convenience: if another app owns it, the overlay
        // key and the chat box inside the overlay still work.
        let _ = RegisterHotKey(std::ptr::null_mut(), CHAT_ID, MOD_CONTROL | MOD_ALT | MOD_NOREPEAT, VK_C);

        // Unregistering on the way out is courtesy rather than necessity: the OS
        // drops the binding when the process exits. It matters only if this
        // thread ever stops before the process does.
        let mut msg: Msg = std::mem::zeroed();
        loop {
            match GetMessageW(&mut msg, std::ptr::null_mut(), 0, u32::MAX) {
                -1 => {
                    UnregisterHotKey(std::ptr::null_mut(), HOTKEY_ID);
                    return Err("the hotkey message loop failed".to_string());
                }
                0 => break,
                _ => {
                    if msg.message == WM_HOTKEY {
                        on_trigger(if msg.wparam as i32 == CHAT_ID { Hotkey::Chat } else { Hotkey::Overlay });
                    }
                }
            }
        }
    }
    Ok(())
}

/// The overlay is a Windows feature: without injection there is nowhere else to
/// put the hotkey, so the caller gets an error instead of a dead key.
#[cfg(not(target_os = "windows"))]
pub fn spawn<F>(
    _on_trigger: F,
) -> Result<std::thread::JoinHandle<Result<(), String>>, String>
where
    F: Fn(Hotkey) + Send + 'static,
{
    set_status(false, Some("the overlay hotkey is only available on Windows".to_string()));
    Ok(std::thread::spawn(|| {
        Err("the overlay hotkey is only available on Windows".to_string())
    }))
}

#[cfg(test)]
mod tests {
    use super::HOTKEY_LABEL;

    #[test]
    fn label_is_stable() {
        // The tray menu, the overlay UI and the docs all print this string. A
        // silent change here would leave users pressing a key that is documented
        // differently, so it is pinned.
        assert_eq!(HOTKEY_LABEL, "Ctrl+Alt+L");
    }
}