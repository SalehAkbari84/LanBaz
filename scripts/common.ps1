# Shared helpers for the PowerShell build scripts. Dot-source it.

# Kept in step with third_party/wintun/README.md. A driver that fails this
# check still installs and creates an adapter - it just behaves in ways nobody
# can reproduce - so the check happens before the build.
$WintunSha256 = 'e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce'

function Find-Go {
  param([string]$Root)
  if ($env:GO_BIN -and (Test-Path $env:GO_BIN)) { return $env:GO_BIN }
  $cmd = Get-Command go -ErrorAction SilentlyContinue
  if ($cmd) { return $cmd.Source }
  $bundled = Join-Path (Split-Path -Parent $Root) '.tools\go\bin\go.exe'
  if (Test-Path $bundled) { return $bundled }
  throw "Go 1.24+ was not found. Install it, or set GO_BIN to go.exe."
}

function Assert-NotRunning {
  param([string]$Name)
  $p = Get-Process -Name $Name -ErrorAction SilentlyContinue
  if ($p) {
    throw "$Name.exe is running (pid $($p.Id -join ', ')), so it cannot be replaced. Quit LanBaz from the tray menu (or: taskkill /F /IM $Name.exe) and run this again."
  }
}

function Copy-Wintun {
  param([string]$Root, [string]$Destination)
  $src = Join-Path $Root 'third_party\wintun\wintun.dll'
  if (-not (Test-Path $src)) { throw "$src is missing. See third_party\wintun\README.md." }
  $actual = (Get-FileHash -Algorithm SHA256 $src).Hash.ToLowerInvariant()
  if ($actual -ne $WintunSha256) {
    throw "wintun.dll is not the expected build.`n  expected $WintunSha256`n  actual   $actual`nRefusing to ship an unverified network driver."
  }
  New-Item -ItemType Directory -Force $Destination | Out-Null
  Copy-Item -Force $src (Join-Path $Destination 'wintun.dll')
  Copy-Item -Force (Join-Path $Root 'third_party\wintun\LICENSE.txt') (Join-Path $Destination 'wintun-LICENSE.txt')
  Write-Host "== wintun.dll staged ($($WintunSha256.Substring(0,12))...)"
}

# TAP-Windows6 files for Classic LAN rooms; see third_party/tap-windows6/README.md.
$TapFiles = @{
  'OemVista.inf' = '1327ab3a8c50691f04bea8e2ca356c5b604092a719e219464f8cc4b42e192de9'
  'tap0901.cat'  = 'ee062e5ef2743ceab10c64830e4cefe52e35cc1ece85947ac4e61ddd1c0b05f7'
  'tap0901.sys'  = '581dcaace05d5c1ac9512457ff50565aca5d904d2c209bd3fc369ca4d4a0d2b1'
  'devcon.exe'   = 'bee3a63db18565ab77ad5714594b658c1d47c7e475009b25d430df9ed634ea46'
}

function Copy-Tap {
  param([string]$Root, [string]$Destination)
  $src = Join-Path $Root 'third_party\tap-windows6\amd64'
  New-Item -ItemType Directory -Force $Destination | Out-Null
  foreach ($name in $TapFiles.Keys) {
    $f = Join-Path $src $name
    if (-not (Test-Path $f)) { throw "$f is missing. See third_party\tap-windows6\README.md." }
    $actual = (Get-FileHash -Algorithm SHA256 $f).Hash.ToLowerInvariant()
    if ($actual -ne $TapFiles[$name]) { throw "$name is not the expected TAP-Windows build (got $actual)." }
    Copy-Item -Force $f (Join-Path $Destination $name)
  }
  Write-Host "== TAP-Windows driver staged"
}
