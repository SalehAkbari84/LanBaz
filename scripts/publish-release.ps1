<#
.SYNOPSIS
  Publishes a built LanBaz version as a GitHub release, which every installed
  LanBaz then offers as an automatic update.

.DESCRIPTION
  Uploads LanBaz_<version>_x64-setup.exe and latest.json (both made by
  build-installer.ps1 -UpdateRepo owner/repo) to a new release v<version>.
  Needs the GitHub CLI (gh), signed in with `gh auth login`. Without gh, it
  prints the two files to upload by hand at github.com/<repo>/releases/new.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\publish-release.ps1 -Repo someone/lanbaz -Version 0.5.0
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory)][string]$Repo,
  [Parameter(Mandatory)][string]$Version,
  [string]$Notes = ""
)

$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $PSScriptRoot
$nsis = Join-Path $Root 'app\src-tauri\target\release\bundle\nsis'
$setup = Join-Path $nsis "LanBaz_$($Version)_x64-setup.exe"
$latest = Join-Path $nsis 'latest.json'

foreach ($f in @($setup, $latest)) {
  if (-not (Test-Path $f)) { throw "missing $f - build with: scripts\build-installer.ps1 -Version $Version -UpdateRepo $Repo" }
}
$feed = Get-Content -Raw $latest | ConvertFrom-Json
if ($feed.version -ne $Version) { throw "latest.json is for $($feed.version), not $Version" }
if ($feed.platforms.'windows-x86_64'.url -notlike "https://github.com/$Repo/*") { throw "latest.json points at another repo: $($feed.platforms.'windows-x86_64'.url)" }

if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
  Write-Host "GitHub CLI (gh) is not installed. Publish by hand:"
  Write-Host "  1. open https://github.com/$Repo/releases/new"
  Write-Host "  2. tag: v$Version   title: LanBaz $Version"
  Write-Host "  3. attach: $setup"
  Write-Host "             $latest"
  Write-Host "  4. Publish release (not draft, not pre-release)"
  exit 0
}
if (-not $Notes) { $Notes = "LanBaz $Version" }
gh release create "v$Version" $setup $latest --repo $Repo --title "LanBaz $Version" --notes $Notes
if ($LASTEXITCODE -ne 0) { throw "gh release create failed" }
Write-Host "Published. Installed copies of LanBaz will offer $Version within 6 hours (or at Settings -> About -> Check now)."
