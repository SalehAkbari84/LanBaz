// Package games notices which game this machine is running and whether it is
// hosting, so friends see "Ali is hosting Minecraft" with a join button
// instead of asking which address and port to type.
//
// Detection reads the process list and the system socket tables. It never
// opens, injects into or reads the memory of a game process, so anti-cheat has
// nothing to object to.
package games

import (
	"net/netip"
	"sort"
	"strings"

	"github.com/lanbaz/lanbaz/profiles"
)

// Socket is one socket a process holds.
type Socket struct {
	Proto string // "tcp" (listening) or "udp"
	Addr  netip.Addr
	Port  int
}

// Process is a running process with its sockets.
type Process struct {
	PID     uint32
	Exe     string // lowercase base name
	Path    string // full image path, filled only for known games
	Sockets []Socket
}

// Detection is the game this machine is running, if any.
type Detection struct {
	Profile *profiles.Profile // nil for an unknown game
	Exe     string
	PID     uint32
	// Path is the game's full executable path, when Windows tells.
	Path string
	// Ports are the ports the game listens on that friends can reach.
	Ports []int
}

// Hosting reports whether the game is accepting connections.
func (d Detection) Hosting() bool { return len(d.Ports) > 0 }

// Name is the display name.
func (d Detection) Name() string {
	if d.Profile != nil {
		return d.Profile.Name
	}
	return d.Exe
}

// ID is the profile id, or "unknown".
func (d Detection) ID() string {
	if d.Profile != nil {
		return d.Profile.ID
	}
	return "unknown"
}

// Detector matches processes against the bundled profiles.
type Detector struct {
	index map[string][]profiles.Profile
	scan  func() ([]Process, error)
}

// New builds a detector over the bundled profiles.
func New(ps []profiles.Profile) *Detector {
	d := &Detector{index: profiles.Index(ps)}
	// Full paths are read only for executables several games share, so the
	// scan does not open every process on the machine.
	d.scan = func() ([]Process, error) {
		return scanProcesses(func(exe string) bool { return len(d.index[exe]) > 1 })
	}
	return d
}

// lanbazRange is LanBaz's own address space.
var lanbazRange = netip.MustParsePrefix("10.200.0.0/16")

// Detect returns the most relevant game: a known game that hosts beats a
// known game that only plays, which beats an unknown program that listens on
// a LanBaz address. Ok is false when nothing is running.
func (d *Detector) Detect() (Detection, bool) {
	procs, err := d.scan()
	if err != nil || len(procs) == 0 {
		return Detection{}, false
	}
	var best *Detection
	score := func(x Detection) int {
		s := 0
		if x.Profile != nil {
			s += 2
		}
		if x.Hosting() {
			s++
		}
		return s
	}
	for _, p := range procs {
		var det *Detection
		if prof, ok := profiles.Pick(d.index[p.Exe], p.Path); ok {
			pc := prof
			det = &Detection{Profile: &pc, Exe: p.Exe, PID: p.PID, Path: p.Path, Ports: reachablePorts(p.Sockets, &pc)}
		} else if systemProcess(p) {
			continue
		} else if ports := lanbazBound(p.Sockets); len(ports) > 0 {
			det = &Detection{Exe: p.Exe, PID: p.PID, Ports: ports}
		}
		if det == nil {
			continue
		}
		if best == nil || score(*det) > score(*best) {
			best = det
		}
	}
	if best == nil {
		return Detection{}, false
	}
	if best.Path == "" {
		best.Path = imagePath(best.PID)
	}
	return *best, true
}

// reachablePorts picks the ports a friend could connect to: TCP listeners on
// any address or a LanBaz address, and UDP sockets on the profile's ports (a
// UDP game server is just a bound socket).
func reachablePorts(socks []Socket, p *profiles.Profile) []int {
	known := map[int]bool{}
	for _, port := range p.Network.Ports {
		known[port] = true
	}
	set := map[int]bool{}
	for _, s := range socks {
		if systemPorts[s.Port] {
			continue
		}
		reach := s.Addr.IsUnspecified() || lanbazRange.Contains(s.Addr)
		if !reach {
			continue
		}
		switch s.Proto {
		case "tcp":
			set[s.Port] = true
		case "udp":
			if known[s.Port] {
				set[s.Port] = true
			}
		}
	}
	return sortedPorts(set)
}

// lanbazBound returns ports bound specifically to a LanBaz address: a program
// that does that is almost certainly a game server meant for the room.
func lanbazBound(socks []Socket) []int {
	set := map[int]bool{}
	for _, s := range socks {
		if lanbazRange.Contains(s.Addr) && !systemPorts[s.Port] {
			set[s.Port] = true
		}
	}
	return sortedPorts(set)
}

func sortedPorts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// systemPorts are Windows' own services, which bind to every adapter
// (LanBaz's included): NetBIOS, SMB, SSDP, mDNS, LLMNR, WS-Discovery.
var systemPorts = map[int]bool{137: true, 138: true, 139: true, 445: true, 1900: true, 3702: true, 5353: true, 5355: true}

// systemExes are Windows processes that are never a game.
var systemExes = map[string]bool{
	"system": true, "svchost.exe": true, "lsass.exe": true, "services.exe": true,
	"wininit.exe": true, "spoolsv.exe": true, "smss.exe": true, "csrss.exe": true,
	"system idle process": true, "registry": true, "dns.exe": true,
}

// launcherExes are stores, launchers, overlays, chat, browsers and VPN/LAN
// tools. They bind sockets on every adapter - Steam's LAN discovery and
// in-home streaming listen on LanBaz's address too - so without this list
// "Steam" would be reported as the game instead of the game Steam started.
var launcherExes = map[string]bool{
	// Stores and launchers.
	"steam.exe": true, "steamwebhelper.exe": true, "steamservice.exe": true, "gameoverlayui.exe": true,
	"steamerrorreporter.exe": true, "steamerrorreporter64.exe": true,
	"epicgameslauncher.exe": true, "epicwebhelper.exe": true, "epiconlineservices.exe": true,
	"eosoverlayrenderer-win64-shipping.exe": true, "eosoverlayrenderer-win32-shipping.exe": true,
	"eadesktop.exe": true, "eabackgroundservice.exe": true, "eaconnect_microsoft.exe": true,
	"origin.exe": true, "originwebhelperservice.exe": true, "originclientservice.exe": true,
	"battle.net.exe": true, "agent.exe": true, "blizzard battle.net app.exe": true,
	"upc.exe": true, "uplaywebcore.exe": true, "ubisoftconnect.exe": true, "ubisoftgamelauncher.exe": true,
	"ubisoftgamelauncher64.exe": true, "galaxyclient.exe": true, "galaxyclient helper.exe": true,
	"gogcommunicationservice.exe": true, "rockstarservice.exe": true, "launcher.exe": true,
	"socialclubhelper.exe": true, "playnite.desktopapp.exe": true, "playnite.fullscreenapp.exe": true,
	"xboxpcapp.exe": true, "gamingservices.exe": true, "gamingservicesnet.exe": true, "xboxapp.exe": true,
	"gamebar.exe": true, "gamebarftserver.exe": true, "xlive.exe": true, "gfwlclient.exe": true,
	"amazongames.exe": true, "itch.exe": true, "riotclientservices.exe": true, "riotclientux.exe": true,
	"heroiclauncher.exe": true, "heroic.exe": true,
	// Overlays, capture and hardware tools.
	"nvcontainer.exe": true, "nvidia share.exe": true, "nvidia web helper.exe": true, "nvidia overlay.exe": true,
	"nvidia app.exe": true, "radeonsoftware.exe": true, "amdrsserv.exe": true, "obs64.exe": true, "obs32.exe": true,
	"medal.exe": true, "overwolf.exe": true, "rtss.exe": true, "msiafterburner.exe": true,
	// Chat.
	"discord.exe": true, "discordptb.exe": true, "discordcanary.exe": true, "telegram.exe": true,
	"teamspeak3.exe": true, "ts3client_win64.exe": true, "ts3client_win32.exe": true, "teamspeak.exe": true,
	"skype.exe": true, "whatsapp.exe": true, "slack.exe": true, "zoom.exe": true, "ms-teams.exe": true,
	"mumble.exe": true,
	// Browsers.
	"chrome.exe": true, "msedge.exe": true, "msedgewebview2.exe": true, "firefox.exe": true, "opera.exe": true,
	"brave.exe": true, "vivaldi.exe": true, "yandex.exe": true,
	// VPNs and virtual LANs (LanBaz included).
	"hotspotshield.exe": true, "hsscp.exe": true, "hydra.exe": true, "hsswd.exe": true,
	"radmin_vpn.exe": true, "rvpnnetmp.exe": true, "hamachi-2.exe": true, "hamachi-2-ui.exe": true,
	"zerotier-one_x64.exe": true, "zerotier one.exe": true, "tailscaled.exe": true, "tailscale-ipn.exe": true,
	"openvpn.exe": true, "openvpn-gui.exe": true, "wireguard.exe": true, "nordvpn.exe": true,
	"expressvpn.exe": true, "protonvpn.exe": true, "lanbaz.exe": true, "lanbazd.exe": true, "lanbaz-ui.exe": true,
	// Remote desktop and streaming.
	"anydesk.exe": true, "teamviewer.exe": true, "teamviewer_service.exe": true, "parsecd.exe": true,
	"sunshine.exe": true, "moonlight.exe": true, "rustdesk.exe": true,
}

// systemProcess reports whether a process belongs to Windows itself or is a
// launcher, overlay or network tool rather than a game.
func systemProcess(p Process) bool {
	if p.PID == 0 || p.PID == 4 || systemExes[p.Exe] || launcherExes[p.Exe] {
		return true
	}
	path := strings.ToLower(p.Path)
	return strings.HasPrefix(path, `c:\windows\`)
}

// normExe lowercases an executable name.
func normExe(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
