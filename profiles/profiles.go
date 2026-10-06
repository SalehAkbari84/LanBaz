// Package profiles holds the bundled game profiles: which executables a game
// runs as, which ports it uses, how its LAN discovery works and how a player
// joins. They are embedded into the daemon so detection works with no files
// beside it; the JSON stays readable and editable in the repository.
package profiles

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed *.json
var files embed.FS

// Profile is one game.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	Executables struct {
		Windows []string `json:"windows"`
		Linux   []string `json:"linux"`
		MacOS   []string `json:"macos"`
	} `json:"executables"`
	Network struct {
		Protocol       string `json:"protocol"`
		Ports          []int  `json:"ports"`
		Broadcast      bool   `json:"broadcast"`
		Multicast      bool   `json:"multicast"`
		DiscoveryPorts []int  `json:"discovery_ports"`
	} `json:"network"`
	Discovery struct {
		Type string `json:"type"`
	} `json:"discovery"`
	JoinHint string `json:"join_hint"`
	// JoinURI is a template with {host} and {port}, e.g. steam://connect/{host}:{port}.
	JoinURI string `json:"join_uri,omitempty"`
	// NeedsL2 marks games whose LAN mode only works on a Classic LAN (L2) room.
	NeedsL2 bool `json:"needs_l2,omitempty"`
	// PathHints tell apart games that share an executable name (Generals and
	// Zero Hour both run generals.exe): a hint found in the install path,
	// lowercase, picks this profile. A profile without hints is the fallback.
	PathHints []string `json:"path_hints,omitempty"`
	Notes     string   `json:"notes"`
	// Availability says how to get the game without Steam: "free" (free or
	// open source) or "drm_free" (sold DRM-free, e.g. on GOG). Empty means
	// a store with DRM.
	Availability []string `json:"availability,omitempty"`
	// Requires names data a free engine needs from the original game.
	Requires string `json:"requires,omitempty"`
	// MaxLanRTTMs is the highest round trip the game accepts for a LAN
	// player (Gears of War refuses anyone above about 30 ms); 0 = no limit.
	MaxLanRTTMs int `json:"max_lan_rtt_ms,omitempty"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// Validate reports the first problem with a profile.
func (p Profile) Validate() error {
	if !idPattern.MatchString(p.ID) {
		return fmt.Errorf("profile id %q is not a lowercase slug", p.ID)
	}
	if p.Name == "" || p.Version != 1 {
		return fmt.Errorf("profile %s: missing name or unsupported version %d", p.ID, p.Version)
	}
	switch p.Network.Protocol {
	case "tcp", "udp", "both":
	default:
		return fmt.Errorf("profile %s: network.protocol %q", p.ID, p.Network.Protocol)
	}
	for _, port := range append(append([]int{}, p.Network.Ports...), p.Network.DiscoveryPorts...) {
		if port < 1 || port > 65535 {
			return fmt.Errorf("profile %s: port %d out of range", p.ID, port)
		}
	}
	return nil
}

// Load returns every bundled profile, sorted by name. A broken file is
// skipped and reported, never fatal.
func Load() ([]Profile, []error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, []error{err}
	}
	var out []Profile
	var errs []error
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := files.ReadFile(e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var p Profile
		if err := json.Unmarshal(raw, &p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if err := p.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

// Index maps lowercase Windows executable names to the profiles that use them.
func Index(ps []Profile) map[string][]Profile {
	idx := make(map[string][]Profile)
	for _, p := range ps {
		for _, exe := range p.Executables.Windows {
			k := strings.ToLower(exe)
			idx[k] = append(idx[k], p)
		}
	}
	return idx
}

// Pick chooses among profiles sharing an executable using the process's full
// path: the longest matching path hint wins, else the profile without hints.
func Pick(cands []Profile, path string) (Profile, bool) {
	if len(cands) == 0 {
		return Profile{}, false
	}
	path = strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	best, bestLen := -1, -1
	fallback := -1
	for i, p := range cands {
		if len(p.PathHints) == 0 && fallback < 0 {
			fallback = i
		}
		for _, h := range p.PathHints {
			h = strings.ToLower(h)
			if h != "" && strings.Contains(path, h) && len(h) > bestLen {
				best, bestLen = i, len(h)
			}
		}
	}
	switch {
	case best >= 0:
		return cands[best], true
	case fallback >= 0:
		return cands[fallback], true
	default:
		return cands[0], true
	}
}

// JoinURIFor fills a profile's join template, or returns "".
func (p Profile) JoinURIFor(host string, port int) string {
	if p.JoinURI == "" || host == "" || port == 0 {
		return ""
	}
	r := strings.NewReplacer("{host}", host, "{port}", fmt.Sprint(port))
	return r.Replace(p.JoinURI)
}
