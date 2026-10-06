<#
.SYNOPSIS
  Developer loop: build the sidecar, then run the desktop app with hot reload.

.DESCRIPTION
  Debug builds do not ask for administrator rights (see app\src-tauri\build.rs),
  so the virtual adapter is only created when this terminal is itself elevated.
  Set $env:LANBAZ_ELEVATE = '1' to embed the elevation manifest in dev builds.
#>
$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot

& (Join-Path $PSScriptRoot 'build.ps1') -Version '0.3.0-dev'

Push-Location (Join-Path $Root 'app')
try {
  if (-not (Test-Path 'node_modules')) { npm ci }
  npm run tauri dev
} finally {
  Pop-Location
}
