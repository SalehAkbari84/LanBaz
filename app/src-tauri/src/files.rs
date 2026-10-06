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
    let mut cmd = std::process::Command::new("explorer.exe");
    // Explorer wants exactly /select,"C:\path with spaces\file.mp4". Rust's
    // normal quoting wraps the whole argument ("/select,C:\...") instead, and
    // Explorer then opens a default folder - which is what happened with
    // video names (they usually contain spaces) but not short photo names.
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.raw_arg(select_arg(&shown));
    }
    #[cfg(not(windows))]
    cmd.arg(&shown);
    cmd.spawn().map(|_| ()).map_err(|e| e.to_string())
}

/// The Explorer argument that selects a file. A '"' cannot occur in a
/// Windows path, so quoting it is enough.
fn select_arg(path: &str) -> String {
    format!("/select,\"{path}\"")
}

/// Opens the Windows "Open" dialog (several files allowed) and returns the
/// chosen paths; empty when cancelled. Raw comdlg32, like hotkey.rs, so the
/// shell needs no dialog plugin.
#[tauri::command]
pub async fn pick_files(window: tauri::WebviewWindow, title: String) -> Result<Vec<String>, String> {
    #[cfg(target_os = "windows")]
    {
        let owner = window.hwnd().map(|h| h.0 as isize).unwrap_or(0);
        tauri::async_runtime::spawn_blocking(move || open_dialog(owner, &title))
            .await
            .map_err(|e| e.to_string())
    }
    #[cfg(not(target_os = "windows"))]
    {
        let _ = (window, title);
        Ok(Vec::new())
    }
}

/// OPENFILENAMEW, as comdlg32 lays it out.
#[cfg(target_os = "windows")]
#[repr(C)]
struct OpenFileNameW {
    struct_size: u32,
    owner: *mut std::ffi::c_void,
    instance: *mut std::ffi::c_void,
    filter: *const u16,
    custom_filter: *mut u16,
    max_cust_filter: u32,
    filter_index: u32,
    file: *mut u16,
    max_file: u32,
    file_title: *mut u16,
    max_file_title: u32,
    initial_dir: *const u16,
    title: *const u16,
    flags: u32,
    file_offset: u16,
    file_extension: u16,
    def_ext: *const u16,
    cust_data: isize,
    hook: *mut std::ffi::c_void,
    template_name: *const u16,
    reserved_ptr: *mut std::ffi::c_void,
    reserved: u32,
    flags_ex: u32,
}

#[cfg(target_os = "windows")]
fn open_dialog(owner: isize, title: &str) -> Vec<String> {

    #[link(name = "comdlg32")]
    extern "system" {
        fn GetOpenFileNameW(ofn: *mut OpenFileNameW) -> i32;
    }
    const OFN_EXPLORER: u32 = 0x0008_0000;
    const OFN_ALLOWMULTISELECT: u32 = 0x0000_0200;
    const OFN_FILEMUSTEXIST: u32 = 0x0000_1000;
    const OFN_PATHMUSTEXIST: u32 = 0x0000_0800;
    const OFN_NOCHANGEDIR: u32 = 0x0000_0008;

    let mut buf = vec![0u16; 1 << 16];
    let filter: Vec<u16> = "All files\0*.*\0\0".encode_utf16().collect();
    let title: Vec<u16> = title.encode_utf16().chain(std::iter::once(0)).collect();
    let mut ofn = OpenFileNameW {
        struct_size: std::mem::size_of::<OpenFileNameW>() as u32,
        owner: owner as *mut std::ffi::c_void,
        instance: std::ptr::null_mut(),
        filter: filter.as_ptr(),
        custom_filter: std::ptr::null_mut(),
        max_cust_filter: 0,
        filter_index: 1,
        file: buf.as_mut_ptr(),
        max_file: buf.len() as u32,
        file_title: std::ptr::null_mut(),
        max_file_title: 0,
        initial_dir: std::ptr::null(),
        title: title.as_ptr(),
        flags: OFN_EXPLORER | OFN_ALLOWMULTISELECT | OFN_FILEMUSTEXIST | OFN_PATHMUSTEXIST | OFN_NOCHANGEDIR,
        file_offset: 0,
        file_extension: 0,
        def_ext: std::ptr::null(),
        cust_data: 0,
        hook: std::ptr::null_mut(),
        template_name: std::ptr::null(),
        reserved_ptr: std::ptr::null_mut(),
        reserved: 0,
        flags_ex: 0,
    };
    if unsafe { GetOpenFileNameW(&mut ofn) } == 0 {
        return Vec::new();
    }
    parse_selection(&buf)
}

/// One file: "C:\dir\a.zip\0\0". Several: "C:\dir\0a.zip\0b.zip\0\0".
#[cfg(target_os = "windows")]
fn parse_selection(buf: &[u16]) -> Vec<String> {
    use std::os::windows::ffi::OsStringExt;
    let parts: Vec<String> = buf
        .split(|&c| c == 0)
        .take_while(|s| !s.is_empty())
        .map(|s| std::ffi::OsString::from_wide(s).to_string_lossy().into_owned())
        .collect();
    match parts.as_slice() {
        [] => Vec::new(),
        [one] => vec![one.clone()],
        [dir, names @ ..] => names
            .iter()
            .map(|n| Path::new(dir).join(n).to_string_lossy().into_owned())
            .collect(),
    }
}

#[cfg(all(test, target_os = "windows"))]
mod tests {
    use super::parse_selection;

    fn wide(s: &str) -> Vec<u16> {
        s.encode_utf16().collect()
    }

    #[test]
    fn selections() {
        assert_eq!(parse_selection(&wide(r"C:\x\a.zip\0\0".replace(r"\0", "\0").as_str())), vec![r"C:\x\a.zip"]);
        assert_eq!(
            parse_selection(&wide(&format!("{}\0a.zip\0b c.txt\0\0", r"C:\x"))),
            vec![r"C:\x\a.zip", r"C:\x\b c.txt"]
        );
        assert!(parse_selection(&wide("\0\0")).is_empty());
    }

    #[test]
    fn select_argument_quotes_the_path() {
        assert_eq!(
            super::select_arg(r"C:\Users\a\Downloads\LanBaz\my clip, final (2).mp4"),
            r#"/select,"C:\Users\a\Downloads\LanBaz\my clip, final (2).mp4""#
        );
    }

    #[test]
    #[cfg(target_pointer_width = "64")]
    fn dialog_struct_matches_windows() {
        assert_eq!(std::mem::size_of::<super::OpenFileNameW>(), 152);
    }
}
