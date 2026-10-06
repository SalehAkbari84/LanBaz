<#
.SYNOPSIS
  Builds the LanBaz Windows installer (NSIS).

.DESCRIPTION
  1. builds and stages the daemon sidecar and the Wintun driver (build.ps1)
  2. installs the frontend dependencies if needed and builds the UI
  3. bundles the installer with Tauri

  The installer lands in app\src-tauri\target\release\bundle\nsis\.
  Tauri downloads NSIS the first time, so the first run needs internet.

  Automatic updates: when the signing key exists (default
  %USERPROFILE%\.lanbaz\updater.key, made with `npx tauri signer generate`),
  the installer is signed and, with -UpdateRepo owner/repo, latest.json is
  written next to it. -UpdateRepo is also built in as the default release
  feed (users can change it in Settings -> About). Publish with
  scripts\publish-release.ps1.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\build-installer.ps1
#>
[CmdletBinding()]
param(
  [string]$Version = "0.5.2",
  [switch]$SkipUI,
  [string]$UpdateRepo = $env:LANBAZ_UPDATE_REPO,
  [string]$SigningKey = (Join-Path $env:USERPROFILE '.lanbaz\updater.key'),
  [string]$Notes = ""
)

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
$App = Join-Path $Root 'app'

& (Join-Path $PSScriptRoot 'build.ps1') -Version $Version
if ($LASTEXITCODE -and $LASTEXITCODE -ne 0) { throw "build.ps1 failed" }

Push-Location $App
try {
  if (-not (Test-Path (Join-Path $App 'node_modules'))) {
    Write-Host "== installing frontend dependencies"
    npm ci
    if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
  }
  if (-not $SkipUI) {
    Write-Host "== building the frontend"
    npx tsc --noEmit
    if ($LASTEXITCODE -ne 0) { throw "typecheck failed" }
  }
  Write-Host "== bundling the installer"
  $env:LANBAZ_UPDATE_REPO = $UpdateRepo
  $tauriArgs = @('tauri', 'build', '--bundles', 'nsis')
  $signed = Test-Path $SigningKey
  if ($signed) {
    # The key file stays outside the repo; only its path is handed to Tauri.
    $env:TAURI_SIGNING_PRIVATE_KEY = $SigningKey
    if (-not $env:TAURI_SIGNING_PRIVATE_KEY_PASSWORD) { $env:TAURI_SIGNING_PRIVATE_KEY_PASSWORD = '' }
    $override = Join-Path $env:TEMP 'lanbaz-updater.conf.json'
    '{"bundle":{"createUpdaterArtifacts":true}}' | Set-Content -Encoding ascii $override
    $tauriArgs += @('--config', $override)
  } else {
    Write-Host "   (no signing key at $SigningKey - the installer is not signed and cannot be offered as an automatic update)"
  }
  # The tauri.conf.json beforeBuildCommand runs the vite build.
  npx @tauriArgs
  if ($LASTEXITCODE -ne 0) { throw "tauri build failed" }
} finally {
  Pop-Location
}

$bundle = Join-Path $App 'src-tauri\target\release\bundle'
$setup = Get-ChildItem (Join-Path $bundle 'nsis') -Filter "LanBaz_$($Version)_x64-setup.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
if ($signed -and $setup -and (Test-Path "$($setup.FullName).sig")) {
  if ($UpdateRepo) {
    # The feed the app reads: releases/latest/download/latest.json.
    $feed = [ordered]@{
      version   = $Version
      notes     = $Notes
      pub_date  = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
      platforms = [ordered]@{
        'windows-x86_64' = [ordered]@{
          signature = (Get-Content -Raw "$($setup.FullName).sig").Trim()
          url       = "https://github.com/$UpdateRepo/releases/download/v$Version/$($setup.Name)"
        }
      }
    }
    $latest = Join-Path $setup.DirectoryName 'latest.json'
    [IO.File]::WriteAllText($latest, ($feed | ConvertTo-Json -Depth 5))
    Write-Host "Update feed: $latest"
  } else {
    Write-Host "Signed. Pass -UpdateRepo owner/repo to also write latest.json."
  }
}
Write-Host ""
Write-Host "Installer(s):"
Get-ChildItem -Recurse -Filter *.exe $bundle | ForEach-Object { Write-Host "  $($_.FullName)" }
Write-Host ""
Write-Host "Give the setup .exe to your friend. Both of you install it and start LanBaz (it asks for administrator rights)."
