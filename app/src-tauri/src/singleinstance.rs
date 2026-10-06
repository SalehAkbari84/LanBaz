//! One shell per machine.
//!
//! A second LanBaz would not just open a second window: it would race the first
//! for the daemon (two sidecars fighting over one state file and one API port)
//! and register the same tray icon and the same hotkey. So the guard runs before
//! the Tauri builder, and a second launch hands over to the first instead: it
//! signals a named event the first instance waits on, which brings the running
//! window to the front, then exits. Double-clicking the shortcut while LanBaz
//! sits in the tray therefore opens it, rather than appearing to do nothing.
//!
//! Implemented with a Win32 named mutex and event rather than a plugin, for the
//! same reason the hotkey is raw FFI: the whole of what a plugin does here is a
//! few syscalls.
//!
//! The mutex handle is deliberately leaked for the life of the process. Closing
//! it would release the guard while the shell is still running.

/// Session-local names keep different Windows users out of each other's way.
#[cfg(target_os = "windows")]
const MUTEX_NAME: &str = "Local\\LanBaz.Shell.SingleInstance";
#[cfg(target_os = "windows")]
const EVENT_NAME: &str = "Local\\LanBaz.Shell.Show";

#[cfg(target_os = "windows")]
const ERROR_ALREADY_EXISTS: u32 = 183;

#[cfg(target_os = "windows")]
mod ffi {
    use std::ffi::c_void;

    #[link(name = "kernel32")]
    extern "system" {
        pub fn CreateMutexW(attrs: *const c_void, initial_owner: i32, name: *const u16) -> *mut c_void;
        pub fn CreateEventW(
            attrs: *const c_void,
            manual_reset: i32,
            initial_state: i32,
            name: *const u16,
        ) -> *mut c_void;
        pub fn OpenEventW(access: u32, inherit: i32, name: *const u16) -> *mut c_void;
        pub fn SetEvent(h: *mut c_void) -> i32;
        pub fn WaitForSingleObject(h: *mut c_void, ms: u32) -> u32;
        pub fn CloseHandle(h: *mut c_void) -> i32;
        pub fn GetLastError() -> u32;
    }
}

#[cfg(target_os = "windows")]
fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(std::iter::once(0)).collect()
}

/// Returns true when this process owns the right to run.
#[cfg(target_os = "windows")]
pub fn acquire() -> bool {
    let name = wide(MUTEX_NAME);
    unsafe {
        let handle = ffi::CreateMutexW(std::ptr::null(), 0, name.as_ptr());
        if handle.is_null() {
            // Fail closed: refusing to start is the safe direction.
            return false;
        }
        // GetLastError is only meaningful right after the call.
        if ffi::GetLastError() == ERROR_ALREADY_EXISTS {
            return false;
        }
        let _leak = handle;
        true
    }
}

/// Asks the running instance to show its window.
#[cfg(target_os = "windows")]
pub fn signal_existing() {
    const EVENT_MODIFY_STATE: u32 = 0x0002;
    let name = wide(EVENT_NAME);
    unsafe {
        let h = ffi::OpenEventW(EVENT_MODIFY_STATE, 0, name.as_ptr());
        if !h.is_null() {
            ffi::SetEvent(h);
            ffi::CloseHandle(h);
        }
    }
}

/// Waits for later launches and calls `on_show` for each one.
#[cfg(target_os = "windows")]
pub fn listen<F>(on_show: F)
where
    F: Fn() + Send + 'static,
{
    const INFINITE: u32 = 0xFFFF_FFFF;
    const WAIT_OBJECT_0: u32 = 0;
    let name = wide(EVENT_NAME);
    // Auto-reset: one signal, one show.
    let handle = unsafe { ffi::CreateEventW(std::ptr::null(), 0, 0, name.as_ptr()) };
    if handle.is_null() {
        return;
    }
    let handle = handle as usize;
    let _ = std::thread::Builder::new()
        .name("lanbaz-single-instance".into())
        .spawn(move || loop {
            let rc = unsafe { ffi::WaitForSingleObject(handle as *mut std::ffi::c_void, INFINITE) };
            if rc != WAIT_OBJECT_0 {
                return;
            }
            on_show();
        });
}

#[cfg(not(target_os = "windows"))]
pub fn acquire() -> bool {
    true
}

#[cfg(not(target_os = "windows"))]
pub fn signal_existing() {}

#[cfg(not(target_os = "windows"))]
pub fn listen<F>(_on_show: F)
where
    F: Fn() + Send + 'static,
{
}

#[cfg(test)]
mod tests {
    #[test]
    #[cfg(target_os = "windows")]
    fn names_are_session_local() {
        assert!(super::MUTEX_NAME.starts_with("Local\\"));
        assert!(super::EVENT_NAME.starts_with("Local\\"));
    }
}
