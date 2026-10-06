// Automatic updates from GitHub Releases.
//
// The release feed is `latest.json` on the newest GitHub release of the repo
// named at build time (LANBAZ_UPDATE_REPO, "owner/repo") or, without a rebuild,
// in <state-dir>/update-repo.txt. Every installer is signed with the private key
// that never leaves the maintainer's PC; the public half is in tauri.conf.json,
// so a tampered or foreign installer is refused before it runs.
//
// Install order matters: the installer is downloaded and verified first, then
// the daemon is stopped gracefully (it removes its adapter, routes and firewall
// rule and says goodbye to peers), and only then is the installer started -
// it has to replace lanbazd.exe, which a running daemon would keep locked.

use std::sync::{Arc, Mutex};

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager};
use tauri_plugin_updater::{Update, UpdaterExt};

/// The update found by the last check, kept for `update_install`.
#[derive(Default)]
pub struct Pending(Mutex<Option<Update>>);

#[derive(Serialize)]
pub struct UpdateStatus {
    configured: bool,
    repo: String,
    current: String,
}

#[derive(Serialize)]
pub struct UpdateInfo {
    version: String,
    notes: String,
    date: String,
}

#[derive(Clone, Serialize)]
struct Progress {
    downloaded: usize,
    total: Option<u64>,
}

fn repo() -> Option<String> {
    let file = super::daemon::state_dir().join("update-repo.txt");
    if let Ok(text) = std::fs::read_to_string(file) {
        let r = text.trim().to_string();
        if valid_repo(&r) {
            return Some(r);
        }
    }
    option_env!("LANBAZ_UPDATE_REPO")
        .map(|r| r.trim().to_string())
        .filter(|r| valid_repo(r))
}

/// owner/repo, GitHub's allowed characters only.
fn valid_repo(r: &str) -> bool {
    let mut parts = r.split('/');
    let ok = |s: Option<&str>| {
        s.is_some_and(|s| {
            !s.is_empty()
                && s != "."
                && s != ".."
                && s.chars()
                    .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_' || c == '.')
        })
    };
    ok(parts.next()) && ok(parts.next()) && parts.next().is_none()
}

fn feed(repo: &str) -> Result<tauri::Url, String> {
    format!("https://github.com/{repo}/releases/latest/download/latest.json")
        .parse()
        .map_err(|e| format!("{e}"))
}

#[tauri::command]
pub fn update_status(app: AppHandle) -> UpdateStatus {
    let repo = repo().unwrap_or_default();
    UpdateStatus {
        configured: !repo.is_empty(),
        repo,
        current: app.package_info().version.to_string(),
    }
}

/// Saves the release repo ("owner/repo"); an empty string clears it.
#[tauri::command]
pub fn update_set_repo(repo: String) -> Result<(), String> {
    let repo = repo.trim();
    let path = super::daemon::state_dir().join("update-repo.txt");
    if repo.is_empty() {
        let _ = std::fs::remove_file(path);
        return Ok(());
    }
    if !valid_repo(repo) {
        return Err("expected owner/repo, for example someone/lanbaz".into());
    }
    std::fs::write(path, repo).map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn update_check(app: AppHandle) -> Result<Option<UpdateInfo>, String> {
    let Some(repo) = repo() else {
        return Ok(None);
    };
    let updater = app
        .updater_builder()
        .endpoints(vec![feed(&repo)?])
        .map_err(|e| e.to_string())?
        .build()
        .map_err(|e| e.to_string())?;
    let found = updater.check().await.map_err(|e| e.to_string())?;
    let info = found.as_ref().map(|u| UpdateInfo {
        version: u.version.clone(),
        notes: u.body.clone().unwrap_or_default(),
        date: u.date.map(|d| d.to_string()).unwrap_or_default(),
    });
    *app.state::<Pending>().0.lock().unwrap_or_else(|e| e.into_inner()) = found;
    Ok(info)
}

#[tauri::command]
pub async fn update_install(app: AppHandle) -> Result<(), String> {
    let update = app
        .state::<Pending>()
        .0
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .take()
        .ok_or("no update was found; check again")?;
    let emitter = app.clone();
    let bytes = update
        .download(
            move |chunk, total| {
                let _ = emitter.emit("update-progress", Progress { downloaded: chunk, total });
            },
            || {},
        )
        .await
        .map_err(|e| e.to_string())?;

    // Verified and on disk: now stop the daemon so its files can be replaced.
    if let Some(supervisor) = app.try_state::<Arc<super::supervisor::Supervisor>>() {
        supervisor.suppress();
    }
    let _ = tauri::async_runtime::spawn_blocking(super::daemon::stop_blocking).await;
    super::kill_sidecars(&app);

    // On Windows this starts the installer and exits LanBaz.
    update.install(bytes).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::valid_repo;

    #[test]
    fn repo_names() {
        assert!(valid_repo("someone/lanbaz"));
        assert!(valid_repo("a-b_c.d/x.y"));
        for bad in ["", "x", "a/b/c", "a/", "/b", "a b/c", "a/b?x=1", "../x"] {
            assert!(!valid_repo(bad), "{bad}");
        }
    }
}
