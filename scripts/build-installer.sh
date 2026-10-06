#!/usr/bin/env bash
# Builds the installable LanBaz bundle (NSIS) and the bare sidecar.
# On Windows without Git Bash, use scripts/build-installer.ps1 instead.
#
# What "done" means for this script:
#   1. a fresh lanbazd sidecar in app/src-tauri/binaries with the triple suffix
#   2. a production frontend build in app/dist
#   3. installers in app/src-tauri/target/release/bundle
#
# Tauri's bundler downloads NSIS on first run, so this needs network the very
# first time. Everything after that is offline and reproducible.
#
# Usage:
#   scripts/build-installer.sh              full pipeline
#   scripts/build-installer.sh --skip-ui    reuse the existing dist/ build
#   scripts/build-installer.sh --skip-sidecar
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP="$ROOT/app"
TAURI="$APP/src-tauri"
VENDORED="$ROOT/third_party/wintun/wintun.dll"
DEST="$TAURI/binaries/wintun.dll"
# Kept in step with third_party/wintun/README.md. A driver that fails this check
# still installs, still creates an adapter and still reports the network ready -
# it just never receives a packet - so the check happens before the build, not
# in the installer.
WINTUN_SHA256="e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce"

SKIP_UI=0
SKIP_SIDECAR=0
for arg in "$@"; do
  case "$arg" in
    --skip-ui) SKIP_UI=1 ;;
    --skip-sidecar) SKIP_SIDECAR=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

echo "== 0/3 staging the Wintun driver =="
# tauri-build replaces target\release\lanbazd.exe with remove_file() followed by
# a copy, and unwraps the remove: any running lanbazd.exe - including one the
# user started as administrator, which this script cannot stop - turns the whole
# bundle into a cargo panic reading "Access is denied." in a registry path. The
# failure is at least a minute of Rust work away from the cause, so it is worth
# checking here.
running="$(tasklist //FI 'IMAGENAME eq lanbazd.exe' //NH 2>/dev/null | grep -i 'lanbazd.exe' || true)"
if [ -n "$running" ]; then
  echo "error: lanbazd.exe is running, so the bundler cannot replace it." >&2
  echo "$running" | sed 's/^/  /' >&2
  echo >&2
  echo "Quit LanBaz from the tray menu (or run: taskkill /F /IM lanbazd.exe)" >&2
  echo "and run this script again." >&2
  exit 1
fi

if [ ! -f "$VENDORED" ]; then
  echo "error: $VENDORED is missing. See third_party/wintun/README.md." >&2
  exit 1
fi
actual="$(sha256sum "$VENDORED" | cut -d' ' -f1)"
if [ "$actual" != "$WINTUN_SHA256" ]; then
  echo "error: $VENDORED is not the expected Wintun build." >&2
  echo "  expected $WINTUN_SHA256" >&2
  echo "  actual   $actual" >&2
  echo "Refusing to ship an unverified virtual network driver. See" >&2
  echo "third_party/wintun/README.md for the upgrade procedure." >&2
  exit 1
fi
mkdir -p "$(dirname "$DEST")"
cp "$VENDORED" "$DEST"
cp "$ROOT/third_party/wintun/LICENSE.txt" "$TAURI/binaries/wintun-LICENSE.txt"
echo "driver staged: wintun.dll (${WINTUN_SHA256:0:12}...)"

# The Go toolchain is not on PATH in this project; honor the same override the
# other scripts use.
GO_BIN="${GO_BIN:-go}"
if ! command -v "$GO_BIN" >/dev/null 2>&1 && [ -x "$ROOT/../.tools/go/bin/go.exe" ]; then
  GO_BIN="$ROOT/../.tools/go/bin/go.exe"
fi

if [ "$SKIP_SIDECAR" -eq 0 ]; then
  echo "== 1/3 building the lanbazd sidecar =="
  GO_BIN="$GO_BIN" "$ROOT/scripts/build-sidecar.sh"
else
  echo "== 1/3 sidecar skipped =="
fi

if [ "$SKIP_UI" -eq 0 ]; then
  echo "== 2/3 building the frontend =="
  ( cd "$APP" && npx tsc --noEmit && npx vite build )
else
  echo "== 2/3 frontend skipped =="
fi

echo "== 3/3 bundling installers =="
cd "$APP"
# The npm CLI is used rather than `cargo tauri`: it is already a project
# dependency, so this works without installing a global toolchain crate.
# --bundles nsis keeps the output predictable; Tauri would otherwise build every
# target configured, and app-updater artifacts do not exist yet.
npx tauri build --bundles nsis

BUNDLE="$TAURI/target/release/bundle"
echo
echo "installers ready:"
find "$BUNDLE" -name "*.exe" -o -name "*.msi" 2>/dev/null | while read -r f; do
  echo "  $f"
done
echo
echo "next step: install one of them on a clean Windows machine and walk docs/install.md."
