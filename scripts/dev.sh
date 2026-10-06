#!/usr/bin/env bash
# Developer loop: build the Go sidecar, then start the Tauri shell against it.
#
# Usage: ./scripts/dev.sh
#
# The shell spawns lanbazd as a sidecar, so the binary has to exist under
# app/src-tauri/binaries with the target triple Tauri expects.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "==> building the lanbazd sidecar"
GO_BIN="${GO_BIN:-go}" "$ROOT/scripts/build-sidecar.sh"

echo "==> starting the Tauri shell"
cd "$ROOT/app"
exec npm run tauri dev "$@"