# Wintun

The Windows driver LanBaz binds by hand. The upstream project is
<https://www.wintun.net/> and it is distributed under the terms in
[LICENSE.txt](LICENSE.txt); LanBaz ships the binary unmodified.

| | |
|---|---|
| Version | 0.14.1 |
| Architecture | amd64 (x86_64) |
| Source | `https://www.wintun.net/builds/wintun-0.14.1.zip` → `wintun/bin/amd64/wintun.dll` |
| SHA-256 | `e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce` |

[wintun.h](wintun.h) is the upstream header the binding in
`core/internal/network/wintun/wintun_windows.go` is written against. It is kept
here for the same reason: the binding is written by hand, and the only thing
that keeps it honest is being able to read the signatures it depends on. Every
argument list in that file traces to a declaration in that header, and getting
one of them wrong is not a compile error — it is a runtime `ERROR_INVALID_PARAMETER`
from the driver after the network has already reported itself ready.

## Why the checksum is enforced

`scripts/build-installer.sh` refuses to build if the DLL it is about to ship
does not match the hash above. The failure mode this guards against is real and
costly: a `wintun.dll` copied from another VPN's install directory is
byte-different, nobody notices, and the installer ships a driver whose behaviour
cannot be reproduced or explained. A build that stops is cheaper than that.

## Upgrading

1. Download the new archive from wintun.net.
2. Replace `wintun.dll` with `wintun/bin/amd64/wintun.dll` and `wintun.h`.
3. Update the version, path and SHA-256 above.
4. Read the header's changelog notes and re-check every signature the binding
   uses against it. Wintun has changed `WintunStartSession`'s signature before.

## Diagnosing a driver that misbehaves

When the daemon reports a Wintun failure and the reason is not obvious, run the
probe against the exact DLL the daemon loads:

```
go build -tags wtprobe -o wtprobe.exe ./scripts/wtprobe
wtprobe.exe wintun.dll
```

It prints the export list and the raw `GetLastError` after create, session and
read-wait, which separates "the driver refused the call" from "the call was made
wrong" without reading daemon log lines. See
[../../scripts/wtprobe/main.go](../../scripts/wtprobe/main.go).