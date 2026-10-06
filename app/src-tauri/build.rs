fn main() {
    println!("cargo:rerun-if-changed=lanbaz.exe.manifest");
    println!("cargo:rerun-if-env-changed=LANBAZ_ELEVATE");

    // Release builds - the installer - ask for administrator rights at launch,
    // because the virtual adapter, its firewall rule and its network profile
    // cannot be configured without them.
    //
    // Debug builds do not, unless LANBAZ_ELEVATE=1: `tauri dev` launches the
    // binary from a normal terminal, and a requireAdministrator executable
    // started that way fails with "the requested operation requires elevation"
    // instead of running. A non-elevated dev build still pairs and connects;
    // only the adapter is missing, and the app says so.
    let profile = std::env::var("PROFILE").unwrap_or_default();
    let elevate = profile == "release" || std::env::var("LANBAZ_ELEVATE").as_deref() == Ok("1");

    let mut windows = tauri_build::WindowsAttributes::new();
    if elevate {
        windows = windows.app_manifest(include_str!("lanbaz.exe.manifest"));
    }
    tauri_build::try_build(tauri_build::Attributes::new().windows_attributes(windows))
        .expect("failed to run the Tauri build script");
}
