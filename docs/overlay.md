# The in-game overlay

A floating panel that shows the room, the virtual LAN and the peer addresses
while a game is running. It is a second webview of the same app, not a separate
feature: it renders from `useRoomsStore`, which the daemon already feeds with
`room.created`, `peer.joined`, `peer.state` and `network.changed`.

## Why it is not an injected overlay

Injecting a DLL into another process is what overlay software does to draw over
exclusive fullscreen, and it is also the fastest way to get flagged by
anti-cheat. A LAN overlay that kicks you out of the game is worse than no overlay
at all, so this one is an ordinary top-level window:

| Property | Value | Why |
| --- | --- | --- |
| `alwaysOnTop` | true | Stays above a borderless-fullscreen game. |
| `skipTaskbar` | true | No taskbar button, no Alt+Tab entry. |
| `transparent` | true | Only the panel is painted; the rest is see-through. |
| `decorations` / `shadow` | false | No chrome to fight with the game's own. |
| `set_ignore_cursor_events` | true in toast mode | Clicks fall through to the game. |

The cost is stated plainly in [src-tauri/src/overlay.rs](../app/src-tauri/src/overlay.rs):
**Windows hands the display to a DirectX exclusive-fullscreen game, and no
ordinary window can stay above it.** Borderless fullscreen and windowed modes,
which is what nearly every current title uses, are unaffected. If a specific
game only offers exclusive fullscreen, the overlay will be hidden behind it
during play — the tray icon and the hotkey still work, and the LAN itself is
unaffected.

## The hotkey

`Ctrl+Alt+L`, registered once at startup with a null window handle so the message
goes to [hotkey.rs](../app/src-tauri/src/hotkey.rs)'s own message-pump thread.
Two consequences worth knowing:

- The hotkey still fires while the overlay is hidden, which is the case that
  matters. A hotkey aimed at the overlay's own window would be posted to a queue
  nobody reads.
- Another application may already own `Ctrl+Alt+L`. That is reported, not
  swallowed: the shell emits `lanbaz://hotkey` and the tray button keeps working.

## Two modes

| Mode | Focus | Clicks | Dismissed by |
| --- | --- | --- | --- |
| `toast` | not taken | pass through to the game | itself, after 5s |
| `panel` | taken | normal | Escape, the hotkey, or × |

A peer joining raises a toast, but only when the overlay is already hidden. If
the panel is open the user is looking at the answer, and switching it to
click-through under the cursor would make the buttons stop responding
mid-click.

## What the panel shows

Ordered by what a user mid-match needs:

1. **Connection** — one dot, green when the daemon socket is up.
2. **The address to type into the game** — the local address in the room subnet,
   with a copy button, because that is the value being carried out of this panel.
3. **Peers** — one line each: name, virtual address, state dot, RTT. A peer with
   no lease yet shows `—` rather than a spinner.
4. **Actions** — create a room, leave.

When there is no room, the panel shows the two things that fix that: create, or
paste a pairing code.

## Connections

Each webview is a separate JavaScript world, so the overlay opens its own
WebSocket to the daemon. That is intentional: it means the panel is already
subscribed and already has the peer list the instant it is shown, with no spinner
in front of a user who is trying to start a match. The daemon's connection limit
is 8 (`MaxConnections` in `core/pkg/config/config.go`), so the two windows are
comfortably inside it.

## Verified, and not

Verified here: `cargo check --offline`, `tsc --noEmit`, `vite build`, and the
panel's rendering at exactly the configured 340x460 window size with room and
peer data. The shell also builds and runs as a release binary, and the hotkey
registration failure path was observed working (it reported `Ctrl+Alt+L is
already taken by another application` rather than failing silently).

Still needs a real Windows machine with a game: whether the panel is correctly
transparent and always-on-top in practice, whether `Ctrl+Alt+L` is reachable
while a game holds focus, and the toast auto-dismiss timing. See
[install.md](install.md) for the full verified/not-verified list.