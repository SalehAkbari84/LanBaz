# Playing LAN games with friends

The short version: everyone installs **one file**, the host sends each friend a
code, each friend sends a reply code back, and from then on everyone's games see
each other as if they were on the same LAN at `10.200.x.x`.

---

## 1. Build or get the installer

On the machine that has the source:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build-installer.ps1
```

The installer is written to:

```
app\src-tauri\target\release\bundle\nsis\LanBaz_0.4.2_x64-setup.exe
```

That one file is the whole application: the window, the tray icon, the in-game
overlay, the `lanbazd` engine and the Wintun network driver. Send it to your
friends any way you like (Discord, Telegram, a USB stick).

- It is **not code-signed**, so SmartScreen warns. Click **More info → Run
  anyway**.
- It installs for all users into Program Files and asks for administrator rights
  once during installation.

## 2. Everyone installs and starts LanBaz

LanBaz **asks for administrator rights every time it starts** (a UAC prompt).
That is required: it creates a virtual network adapter, sets its address, MTU and
priority, marks it as a *Private* network and adds a Windows Firewall rule for
it. Accept the prompt.

If you turn on **Settings → Start with Windows**, it starts in the tray at logon
without a prompt (via a Task Scheduler entry).

The first launch may download the Microsoft Edge WebView2 runtime if the machine
does not have it.

## The easy way: friends (0.4)

Do this once with each friend:

1. **Friends** page → copy **your friend code** (`LBZ-XXXXX-XXXXX`) and send
   it to your friend.
2. Your friend pastes it into **Add a friend** → **Send request**.
3. You press **Accept** on the request that appears.

From then on, nobody copies anything:

- **To join a friend:** when they host a room, their card says *Hosting …*
  with a **Join** button. Press it; they get an Accept card; you are in.
- **To invite a friend:** in your room, press their name under **Invite
  friends**; they get an Accept card.
- **Always:** on an Accept card, *Always* marks that friend as trusted. Their
  next requests are accepted without asking (toggle it with the shield button
  on the Friends page).
- **Dropped?** If the connection to the host breaks, LanBaz asks to rejoin by
  itself for two minutes, and the host lets a friend back in automatically.
- The Games page shows which friends are playing what, with **Join their
  game**.

How it works: friend messages travel through public Nostr relays, end-to-end
encrypted; the relays only see an opaque blob addressed to a key. Game traffic
still goes directly between the PCs. If the relays are unreachable, the manual
codes below still work (under *Advanced* in the room card).

## Sending invites by code (manual)

- **Copy link** gives a `lanbaz://join/...` link: your friend clicks it and
  LanBaz opens with the invite ready.
- **Save as file** writes a small `.lanbaz` file to Downloads: send it in any
  chat (useful where messages are length-limited); double-clicking it opens
  LanBaz.
- Or just copy the text: when LanBaz comes to the front with an invite or a
  reply on the clipboard, it offers to use it.

The same works in reverse for the reply.

## 3. The host creates a room

Pick the player with the best internet connection as host: every guest's
traffic passes through the host.

**Rooms → Host a room → Create room.** You get a long pairing code. Send it to
**one** friend. For the next friend press **New code** and send that one — each
code is good for exactly one person, and several can be outstanding at once.

Codes expire after the time set when creating the room (30 minutes by default).

## 4. Each friend joins and sends a reply code

The friend pastes the code into **Rooms → Join a room**. LanBaz shows them a
**reply code**. They send it back to the host.

This is normal. There is no server in the middle, so the reply is how the host
learns where the friend is.

## 5. The host accepts

The host pastes the reply into **Answer a guest → Accept guest**. Within a few
seconds both sides show the player as `active`.

Now everyone has an address in the room:

- host: `10.200.N.1`
- guests: `10.200.N.2`, `.3`, …

## 6. Play

The host starts the game's LAN / multiplayer server (Minecraft: *Open to LAN*).

- Games that announce themselves on the LAN (Minecraft Java, many older RTS
  games) appear in the guests' **LAN server list** by themselves: LanBaz relays
  broadcast and multicast discovery through the host.
- If a game does not show up, use its **Direct Connect / Join IP** box with the
  host's `10.200.N.1` address. Every room card shows that address with a Copy
  button, and the overlay (**Ctrl+Alt+L**) shows it in-game.

Guests can also host a game for everyone else: every player can reach every
other player's `10.200.x.x` address.

---

## While playing

- **Chat** on the Chat page, or in the overlay: Ctrl+Alt+C opens it in game.
- **Who plays what**: LanBaz detects the game each player runs and whether
  they host. The Games page shows "hosting" players with **Copy address** and,
  for Steam/SA-MP games, a **Join** button.
- **Names instead of numbers**: in games that accept a host name, type your
  friend's name with `.local`, e.g. `ali.local` (names must contain Latin
  letters or digits; set yours in Settings).
- **Lower lag between guests**: guests connect directly to each other once the
  host introduces them; the player list shows `direct` or `via host`.
- **Old games (IPX, e.g. Red Alert 2)**: create the room as **Classic LAN**.
  Every player needs the TAP-Windows driver (installed with LanBaz when
  included in the installer, or from OpenVPN's TAP-Windows package).

## When two players cannot connect

**First run the connection check on both PCs:** Settings → Player and
connection → **Connection check → Run check**. It shows:

- **NAT type.** *Open* or *Cone*: direct links work. *Symmetric*: a direct
  link works only if the other player is Open or Cone. If **both** show
  Symmetric (typical for mobile data and many Iranian ISPs), you need a TURN
  relay, or let a player with home ADSL/fibre host.
- **STUN servers answering.** If none answer, the network blocks them; only a
  TURN relay or the same local network can work.
- **TURN relays.** Whether each configured relay really gives out a relay
  address.

There is no rush with the reply code: a guest's link now waits for the host to
accept for as long as the invite is valid (up to an hour), so take your time
copying it back.

LanBaz connects players directly (peer to peer). That works for most home
connections. It can fail when **both** players are behind strict or carrier-grade
NAT (common on mobile data and some ISPs): the host then sees *"the connection to
your friend could not be established"*.

The fix is a TURN relay server — for example [coturn](https://github.com/coturn/coturn)
on any small VPS:

1. On both machines: **Settings → Player and connection → Add TURN server**,
   e.g. `turn:your-server:3478` with its username and password.
2. Turn on **Use TURN relay when a direct connection fails** and **Save**.
3. Create a new room / new code.

Traffic through the relay is still encrypted end to end.

---

## Checklist

| Symptom | Cause | Fix |
|---|---|---|
| Red banner "not running as administrator" | UAC was declined | Quit from the tray and start LanBaz again; accept the prompt |
| "this answer is for a code that was already used or has expired" | Old or reused reply code | Host presses **New code**, friend joins again |
| "could not be established" after accepting | Strict NAT on both sides | Run the connection check on both PCs; if both are Symmetric, add a TURN server (see above) |
| `WINTUN_CREATE_FAILED ... file already exists` (0.3.1) | An adapter left over from a failed join | Fixed in 0.3.2: LanBaz removes leftover adapters at start and when this happens |
| `tap: could not open the classic LAN adapter ... not functioning` (0.3.1) | LanBaz picked another program's or a broken TAP adapter | Fixed in 0.3.2: only plain TAP adapters are used, a stuck one is restarted, other VPNs' adapters are left alone |
| "only one classic LAN (L2) room can be open at a time" | There is one TAP adapter per PC | Leave the other classic LAN room first |
| Game not in the LAN list | Game uses a discovery method LanBaz cannot relay, or firewall | Use Direct Connect with the host's `10.200.x.1` |
| Can connect but large transfers stall | MTU could not be set (see the LAN note in the room) | Restart LanBaz as administrator |
| Friend's room disappeared with "host closed the room or went offline" | Host left, closed the room or lost connection | Ask the host for a new code |
| Overlay does not appear over the game | Game in exclusive fullscreen | Use borderless windowed |

## Notes

- **The overlay** (Ctrl+Alt+L) never injects into the game, so anti-cheat has
  nothing to detect. It cannot cover exclusive fullscreen.
- **Quit cleanly** with tray → Quit LanBaz. The engine then removes the adapter,
  its routes and its firewall rule.
- While LanBaz is running, its adapter has the highest priority for broadcast
  and multicast. LAN discovery on your *physical* network (e.g. a game server on
  your home LAN) may not show up until you leave the room.
