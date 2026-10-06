<#
.SYNOPSIS
  Builds the LanBaz daemon and CLI, and stages everything the desktop app needs.

.DESCRIPTION
  Outputs:
    bin\lanbazd.exe, bin\lanbazctl.exe
    app\src-tauri\binaries\lanbazd-x86_64-pc-windows-msvc.exe   (Tauri sidecar)
    app\src-tauri\binaries\wintun.dll + wintun-LICENSE.txt      (verified driver)

  Go is taken from PATH, or from ..\.tools\go\bin\go.exe next to the repo, or
  from $env:GO_BIN.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\build.ps1
#>
[CmdletBinding()]
param(
  [string]$Version = "0.6.2"
)

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'common.ps1')

$go = Find-Go -Root $Root
Write-Host "== Go: $go ($(& $go version))"

$commit = 'unknown'
try { $commit = (git -C $Root rev-parse --short HEAD 2>$null) } catch { }
if (-not $commit) { $commit = 'unknown' }
$buildTime = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$ldflags = "-s -w -X main.version=$Version -X main.commit=$commit -X main.buildTime=$buildTime"

$bin = Join-Path $Root 'bin'
New-Item -ItemType Directory -Force $bin | Out-Null

Push-Location $Root
try {
  Write-Host "== building lanbazd and lanbazctl ($Version)"
  & $go build -trimpath -ldflags $ldflags -o (Join-Path $bin 'lanbazd.exe') ./core/cmd/lanbazd
  if ($LASTEXITCODE -ne 0) { throw "go build lanbazd failed" }
  & $go build -trimpath -ldflags $ldflags -o (Join-Path $bin 'lanbazctl.exe') ./core/cmd/lanbazctl
  if ($LASTEXITCODE -ne 0) { throw "go build lanbazctl failed" }
} finally {
  Pop-Location
}

$binaries = Join-Path $Root 'app\src-tauri\binaries'
New-Item -ItemType Directory -Force $binaries | Out-Null
Assert-NotRunning 'lanbazd'
Assert-NotRunning 'lanbazd-x86_64-pc-windows-msvc'
Copy-Item -Force (Join-Path $bin 'lanbazd.exe') (Join-Path $binaries 'lanbazd-x86_64-pc-windows-msvc.exe')
Write-Host "== sidecar staged"

Copy-Wintun -Root $Root -Destination $binaries
Copy-Tap -Root $Root -Destination (Join-Path $binaries 'tap')
Write-Host "== done"
