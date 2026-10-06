# TAP-Windows6

The Ethernet (L2) virtual adapter driver used by LanBaz **Classic LAN** rooms,
from OpenVPN. LanBaz ships the files unmodified and installs the driver only
when a user first creates or joins a Classic LAN room (Settings / Rooms offer an
"install driver" button; the app runs `devcon install OemVista.inf tap0901`).

| | |
|---|---|
| Version | 9.27.0 |
| Source | `https://github.com/OpenVPN/tap-windows6/releases/download/9.27.0/dist.win10.zip` → `dist.win10/amd64/*` |
| Archive SHA-256 | `36e2609b7ceefedcb978ce5c48caf9e0e5af83423717c4e2e3c1d7ebca8f62a5` |
| License | GPL-2.0 (driver), see <https://github.com/OpenVPN/tap-windows6/blob/master/COPYRIGHT.GPL> |

| File | SHA-256 | Signed by |
|---|---|---|
| `amd64/OemVista.inf` | `1327ab3a8c50691f04bea8e2ca356c5b604092a719e219464f8cc4b42e192de9` | (covered by the catalog) |
| `amd64/tap0901.cat` | `ee062e5ef2743ceab10c64830e4cefe52e35cc1ece85947ac4e61ddd1c0b05f7` | Microsoft Windows Hardware Compatibility Publisher |
| `amd64/tap0901.sys` | `581dcaace05d5c1ac9512457ff50565aca5d904d2c209bd3fc369ca4d4a0d2b1` | Microsoft Windows Hardware Compatibility Publisher |
| `amd64/devcon.exe` | `bee3a63db18565ab77ad5714594b658c1d47c7e475009b25d430df9ed634ea46` | Microsoft Corporation |

`scripts/build.ps1` verifies these hashes before staging the files.

The driver is shared with OpenVPN: if OpenVPN is installed, its TAP adapter
works too. LanBaz never removes the driver on uninstall, because that would also
remove OpenVPN's adapters; remove it from Device Manager if you no longer need it.
