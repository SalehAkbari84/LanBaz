# Installing and building LanBaz

How to turn this repository into the installer a user double-clicks, and what to
check on a clean machine afterwards.

## Building the installer

Prerequisites, once per build machine:

- Go 1.24+ (or `GO_BIN=/path/to/go`)
- Node/npm, Rust stable with the MSVC toolchain
- Network access on the **first** run only: Tauri's bundler downloads NSIS on
  first use and caches it under `%LOCALAPPDATA%/tauri`

Then:

```bash
scripts/build-installer.sh
```

The script runs the full pipeline and prints the artifacts at the end:

1. `lanbazd-x86_64-pc-windows-msvc.exe` — the daemon sidecar, built by
   [build-sidecar.sh](../scripts/build-sidecar.sh)
2. `app/dist/` — the production frontend (typechecked first)
3. `app/src-tauri/target/release/bundle/nsis/*.exe` — the installer

`--skip-ui` and `--skip-sidecar` reuse the previous outputs when only one side
changed. A full build is a few minutes; the Rust release profile is the bulk of
it (`lto = true`, `codegen-units = 1` in Cargo.toml).

## Why NSIS only, per-user

The bundle targets NSIS with `installMode: currentUser`:

- **Per-user** because the shell runs as the logged-in user and the daemon writes
  its state to `%LOCALAPPDATA%/LanBaz`. An admin install would create a
  permission split between the installing user and the running user.
- **No MSI** because two installers means two release artifacts to test, sign and
  reproduce, and NSIS already covers everything the product needs. Revisit when
  an enterprise deployment actually asks for MSI.
- **Wintun is bundled.** `wintun.dll` and its licence are shipped as bundle
  resources and land next to `lanbazd.exe`, which is where the daemon looks for
  them. The build verifies the driver's SHA-256 before bundling; see
  [../third_party/wintun/README.md](../third_party/wintun/README.md).

## Verifying on a clean machine

The install is not "done" until this list passes on a machine that has never
seen the repo:

1. Install; no admin prompt should appear (per-user).
2. LanBaz starts minimized to the tray. `daemon.json` exists under
   `%LOCALAPPDATA%/LanBaz`.
3. Start a second copy: nothing happens — the single-instance guard declines it.
4. Close the window: the app stays in the tray, the daemon keeps running.
   Quit only via the tray menu.
5. Create a room; join it from a second machine; both sides reach `active` in
   the Rooms tab.
6. Kill `lanbazd` in Task Manager: within ~5 seconds the UI shows the restart
   banner, and the daemon comes back on its own.
7. `Ctrl+Alt+L` in a borderless-fullscreen game shows the overlay panel;
   Escape hides it.
8. Reboot: with "Start with Windows" enabled, the tray icon is there after login
   and the daemon is reachable.

## Verified on this machine

Everything below was exercised against the release build, not asserted:

- `npx tauri build --bundles nsis` completes. NSIS is fetched once and cached in
  `%LOCALAPPDATA%/tauri`, so later builds need no network.
- The produced `LanBaz_0.1.0_x64-setup.exe` (5,432,751 bytes) installs silently
  and lays down `lanbaz.exe`, `lanbazd.exe`, `wintun.dll`, `wintun-LICENSE.txt`
  and `uninstall.exe` in `%LOCALAPPDATA%\LanBaz`.
- **The installed copy carries the LAN.** Running the installed `lanbazd.exe`,
  creating a room and pinging its own LAN address returns 4/4 replies, with the
  adapter reported `Up` and both routes present in `Get-NetRoute`. The installed
  `wintun.dll` hashes to the expected SHA-256.
- The built `lanbaz.exe` starts without panic, spawns the sidecar, and writes a
  `daemon.json` carrying a valid `api_port`, `api_token` and `managed_by_shell`.
- `lanbazctl room create` against that live daemon returns a real room with a
  `10.200.x.0/24` subnet, a pairing code and a pairing URI.
- **Single instance:** a second `lanbaz.exe` exits silently; one shell, one
  daemon afterwards.
- **Supervisor:** killing the daemon brings it back on its own within ~5s, with a
  new PID written to the state file.
- **Forced-exit cleanup:** `taskkill /F` on the shell runs no cleanup code, and
  still leaves zero orphaned daemons — the daemon notices its parent is gone and
  shuts itself down, logging `the LanBaz shell that started this daemon is gone`.
- Toolchain gates: `cargo check --offline`, `cargo test --lib`, `tsc --noEmit`,
  `vite build`, `go build ./core/...`, `go vet ./core/...` and the full Go test
  suite all pass.

## Verified: the virtual LAN

Run against a real Wintun driver with the daemon started as administrator:

- `virtual adapter created` → `virtual network started ... backend=wintun`, and
  `network status` reports `ready` with the room's `10.200.x.0/24` subnet.
- Windows reports the adapter as **Up**, with the on-link route present:
  `Get-NetRoute` shows `10.200.x.0/24 → 10.200.x.1` on the `LanBaz-…` interface.
- **Packets move.** `ping 10.200.x.1` — the adapter's own LAN address, so the
  reply has to leave through `WintunSendPacket` and come back through the
  session's ring — returns 4/4 replies, and the interface counters show 73
  unicast packets sent.

Four bugs were found this way and fixed:

- `WintunStartSession` takes **two** arguments (adapter, ring capacity), not one.
  With the capacity omitted the driver rejects the call with
  `ERROR_INVALID_PARAMETER` — but only after the adapter exists, has an address
  and a route, and the network has reported itself ready. Every packet function
  was also being given the **adapter** handle where the header declares a
  **session** handle.
- `createError` matched `0x80070005` / `0x80070002`, which are the HRESULT
  spellings of access-denied and file-not-found. A driver call reports the plain
  Win32 codes `5` and `2`, so neither branch ever fired and the one failure with
  an obvious fix arrived as a bare "Access is denied."
- The GUID was handed over from a local variable without keeping it alive for
  the duration of the call.
- `windows.NewLazySystemDLL` searches System32 and the process search path but
  never the directory the executable lives in, so a correctly installed driver
  was still reported missing.

`core/internal/network/wintun/signature_test.go` now reads the binding's own
call sites and fails the build if any packet call is handed the adapter handle,
if any call's arity stops matching `wintun.h`, or if a new export is bound
without a recorded signature. That last bug class is invisible to the compiler
and to `go vet`; the only reason it is now guarded is that it cost a day.

Two more bugs were found only by running the built binary, and both are fixed:

- A stale `plugins.shell.sidecar` block in `tauri.conf.json` panicked the shell
  at startup. `externalBin` already registers the sidecar; the block was invalid
  for this Tauri version.
- `start_daemon` existed twice — once in `lib.rs`, once in `daemon.rs` — and the
  copy the UI called silently dropped `--managed-by-shell`. There is now exactly
  one `spawn_sidecar` path, in the crate root.

## Not verified

- The overlay inside a real borderless-fullscreen game: transparency,
  always-on-top, hotkey reachability, toast timing.
- A genuinely clean machine (no prior WebView2, no prior state directory).
- SmartScreen/Defender reactions to an unsigned installer.
- **Two machines.** Everything above was exercised on one machine. Two peers on
  two real networks have not been put side by side; the pairing and WebRTC
  layers are covered by tests and by single-machine round trips, but that is
  not the same thing.
- Game profiles and LAN discovery. A game that advertises itself over
  broadcast/multicast will not appear in the other player's list automatically;
  the address has to be typed in. See
  [play-with-a-friend.md](play-with-a-friend.md).

## Known limits on a fresh install

- **Administrator rights are required for the LAN.** Creating a virtual adapter
  is a system-privileged operation, and the shell spawns the daemon with the
  token it was started with. Rooms, pairing and the overlay all work without
  them — the network simply does not come up, and `network status` says why.
  Right-click → Run as administrator is the whole workaround. Until the shell
  can relaunch itself with an elevated token this stays a manual step, and it
  is the single most likely reason a first attempt fails.
- **Defender/SmartScreen** — unsigned installers warn on first run. Code signing
  is a release-blocking step for distribution, not for testing.
- **WebView2** — the NSIS bootstrapper downloads it if the machine lacks it,
  which needs network on the target machine.
- **MTU is 1200, not 1500.** A tunnel over arbitrary Internet paths cannot
  promise a full-size frame, so the adapter advertises the lowest MTU every
  Internet path must support. Games do not care.
- **The install directory and the daemon state directory are the same folder.**
  A per-user NSIS install lands in `%LOCALAPPDATA%\LanBaz`, and `config.AppDirName`
  is also `LanBaz`, so `lanbaz.exe`, `lanbazd.exe`, `wintun.dll`,
  `uninstall.exe` and `daemon.json` / `daemon.log` / `identity.key` all sit
  together. Verified working — the daemon finds its driver either way — but it
  means **uninstalling leaves `identity.key` on disk**, so a reinstall silently
  reuses the same peer identity. Set `installDir` under
  `bundle.windows.nsis` to move the app out (for example `Programs\LanBaz`) and
  give the state directory its own name before treating uninstall as a clean
  removal.

## Build gotchas

- **Quit LanBaz before bundling.** `tauri-build` replaces
  `target/release/lanbazd.exe` with a `remove_file` it immediately unwraps, so a
  running daemon turns the bundle step into a cargo panic about `Access is
  denied.` raised from inside the registry — a minute of Rust work away from the
  real cause. `build-installer.sh` now checks for a running `lanbazd.exe` first.
- **Tauri only re-copies the sidecar when it rebuilds it.** Replacing
  `binaries/lanbazd-<triple>.exe` by hand also needs a manual copy to
  `target/release/lanbazd.exe`, or the previous binary runs.
