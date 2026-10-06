#!/usr/bin/env bash
# Builds the lanbazd sidecar into app/src-tauri/binaries with the target triple
# suffix that Tauri expects, so `npm run tauri dev` can start the daemon.
#
# On Windows the binary must be named lanbazd-x86_64-pc-windows-msvc.exe.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$ROOT/app/src-tauri/binaries"
GO_BIN="${GO_BIN:-go}"
# Fall back to the toolchain bundled next to the repo.
if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  for cand in "$ROOT/../.tools/go/bin/go.exe" "$ROOT/../.tools/go/bin/go"; do
    if [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
  done
fi

if ! command -v "$GO_BIN" >/dev/null 2>&1 && [ ! -x "$GO_BIN" ]; then
  echo "error: '$GO_BIN' not found in PATH. Install Go 1.24+ or set GO_BIN." >&2
  exit 1
fi

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) TRIPLE="x86_64-pc-windows-msvc"; SUFFIX=".exe" ;;
  Darwin)                 TRIPLE="aarch64-apple-darwin";   SUFFIX="" ;;
  *)                      TRIPLE="x86_64-unknown-linux-gnu"; SUFFIX="" ;;
esac

mkdir -p "$OUT_DIR"
TARGET="$OUT_DIR/lanbazd-$TRIPLE$SUFFIX"

echo "building lanbazd for $TRIPLE"
( cd "$ROOT" && "$GO_BIN" build -trimpath -ldflags "-s -w" -o "$TARGET" ./core/cmd/lanbazd )

echo "sidecar ready: $TARGET"

# Tauri bundles the driver as a resource, so dev builds need it staged too.
if [ "$SUFFIX" = ".exe" ] && [ -f "$ROOT/third_party/wintun/wintun.dll" ]; then
  cp "$ROOT/third_party/wintun/wintun.dll" "$OUT_DIR/wintun.dll"
  cp "$ROOT/third_party/wintun/LICENSE.txt" "$OUT_DIR/wintun-LICENSE.txt"
  echo "wintun.dll staged"
fi