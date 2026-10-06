# Game profiles

Game profiles are JSON files in [`profiles/`](../profiles). They live outside the
binary on purpose: a user can add a game without rebuilding LanBaz, and the
behaviour is inspectable.

## Status

111 bundled profiles, from late-90s classics (StarCraft, Red Alert, Age of
Empires, Unreal Tournament, Quake) through the mid-2000s (C&C Generals / Zero
Hour, Battlefield 2, Call of Duty 2/4, Need for Speed, Warcraft III) to modern
co-op games (Minecraft, Valheim, Terraria, Satisfactory, ARK). They are embedded
in the daemon (`profiles/profiles.go`), which detects running games by process
name and the ports they listen on, and serves them through `game.list` and
`game.detect`. `profiles/profiles_test.go` validates every file.

Extra fields beyond the schema below:

| Field | Meaning |
| --- | --- |
| `join_hint` | one line telling players how to host and join |
| `join_uri` | optional join link template, `{host}` and `{port}` filled in |
| `needs_l2` | the game's LAN mode only works in a Classic LAN (L2) room |
| `path_hints` | tell apart games sharing an executable (Generals vs Zero Hour both run `generals.exe`): a hint found in the install path selects this profile |

To add a game: copy a similar JSON file, change `id`, `name`, `executables`
and ports, and run `go test ./profiles`.

## Schema

```json
{
  "id": "minecraft",
  "name": "Minecraft",
  "version": 1,
  "executables": {
    "windows": ["javaw.exe"],
    "linux": ["java"],
    "macos": ["java"]
  },
  "network": {
    "protocol": "udp",
    "ports": [19132],
    "broadcast": true,
    "multicast": true,
    "discovery_ports": [4445, 19132]
  },
  "discovery": {
    "type": "minecraft_lan",
    "minecraft_lan": {
      "multicast_group": "224.0.0.251",
      "port": 4445,
      "payload_magic": 512170753
    }
  },
  "firewall": {
    "required": true,
    "protocols": ["udp"],
    "ports": [19132, 4445],
    "scope": "private"
  },
  "notes": "free text"
}
```

### Fields

| Field | Meaning |
| --- | --- |
| `id` | stable slug, unique, lowercase |
| `name` | display name |
| `version` | profile schema version, currently `1` |
| `executables` | process names per platform, matched case-insensitively |
| `network.protocol` | `tcp`, `udp` or `both` |
| `network.ports` | ports the game uses |
| `network.broadcast` / `multicast` | whether the game relies on fan-out traffic |
| `network.discovery_ports` | ports to watch for discovery packets |
| `discovery.type` | `minecraft_lan`, `mdns`, `ssdp`, `server_query` or `broadcast` |
| `discovery.*` | type-specific settings |
| `firewall.required` | whether a firewall rule is needed to play |
| `firewall.protocols` / `ports` / `scope` | what the rule should allow |
| `notes` | free text for the user |

## Bundled profiles

| Profile | Discovery | Why it is here |
| --- | --- | --- |
| `minecraft.json` | multicast 224.0.0.251:4445 | the reference LAN game, and the reason the L3 broadcast risk matters |
| `cs.json` | A2S_INFO on 27015 | classic LAN play; direct host address, L3 routing is enough |
| `aoe2.json` | UDP broadcast 5900 | needs broadcast relay through the room hub |
| `dota2.json` | A2S_INFO on 27036 | lobby is online-only; LAN override is niche |

## Design rules

- Profiles are data, never code. The UI does not hard-code a game list; it reads
  `game.list` from the daemon.
- A malformed profile is rejected with `GAME_PROFILE_INVALID` and does not stop
  the daemon or the other profiles.
- The `id` must match `^[a-z0-9_-]{1,64}$`; it is used in API paths and on disk.
- `ports` must be 1–65535. Anything else is a validation error, not a clamped
  value.
- Firewall rules are only ever created through `FirewallManager`. UI code never
  shells out to `netsh` or PowerShell.
- `discovery.type` selects a handler; unknown types degrade to "no discovery
  handling" rather than an error, so an older build can read a newer profile.

## Adding a profile

1. Copy an existing file in `profiles/`.
2. Set `id`, `name` and the executables.
3. Fill `network` and `discovery` for the game.
4. Set `firewall.required` and the ports.
5. Restart the daemon, or add one later in Phase 5 with `game.detect`.

Phase 5 adds a unit test that validates every bundled profile against this
schema, so a typo fails the build rather than a user's LAN session.