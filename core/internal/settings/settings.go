// Package settings persists the user-editable daemon settings: the display name
// and the STUN/TURN servers used for new connections.
//
// They live in their own file rather than the daemon config because the UI
// edits them at run time through the control API, while the config file is an
// administrator's start-up choice that the daemon never rewrites.
package settings

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// FileName is the settings file inside the state directory.
const FileName = "settings.json"

// MaxServers bounds each server list; ICE tries every server, so a long list
// only slows every connection down.
const MaxServers = 8

// MaxDisplayName bounds the display name, in runes.
const MaxDisplayName = 32

// DefaultSTUN is used when the user has not configured any STUN server.
//
// Chosen by measurement from Iran (October 2026): these answer in about 200 ms,
// while Google's servers answered only after almost 4 s, if at all - and ICE
// gathering waits for the slowest server, which is what made every invite
// wait out the full gathering timeout. Three operators, one of them on port
// 443, which filtered networks rarely block. Every extra server adds a
// candidate to the pairing code.
var DefaultSTUN = []string{
	"stun:stun.cloudflare.com:3478",
	"stun:global.stun.twilio.com:3478",
	"stun:stun.nextcloud.com:443",
}

// Store is the settings file plus an in-memory copy.
type Store struct {
	path string
	mu   sync.RWMutex
	cur  protocol.Settings
}

// Open loads the settings from stateDir. A missing file means defaults. A
// corrupt one also yields a usable store with defaults, plus an error to log:
// a bad settings file must never stop the daemon.
func Open(stateDir string) (*Store, error) {
	s := &Store{path: filepath.Join(stateDir, FileName)}
	data, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		var cur protocol.Settings
		if jerr := json.Unmarshal(data, &cur); jerr != nil {
			return s, protocol.NewErrorf(protocol.CodeConfigInvalid,
				"settings: %s is not valid JSON and was ignored: %v", s.path, jerr)
		}
		norm, verr := Normalize(cur)
		if verr != nil {
			return s, verr
		}
		s.cur = norm
	case errors.Is(err, os.ErrNotExist):
	default:
		return s, protocol.NewErrorf(protocol.CodeConfigInvalid, "settings: read %s: %v", s.path, err)
	}
	return s, nil
}

// Get returns a copy of the current settings.
func (s *Store) Get() protocol.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.cur)
}

// Set validates, stores and persists new settings.
func (s *Store) Set(next protocol.Settings) (protocol.Settings, error) {
	norm, err := Normalize(next)
	if err != nil {
		return protocol.Settings{}, err
	}
	data, err := json.MarshalIndent(norm, "", "  ")
	if err != nil {
		return protocol.Settings{}, protocol.NewErrorf(protocol.CodeInternal, "settings: encode: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return protocol.Settings{}, protocol.NewErrorf(protocol.CodeStateFileUnwritable, "settings: %v", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return protocol.Settings{}, protocol.NewErrorf(protocol.CodeStateFileUnwritable, "settings: write: %v", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return protocol.Settings{}, protocol.NewErrorf(protocol.CodeStateFileUnwritable, "settings: save: %v", err)
	}
	s.mu.Lock()
	s.cur = norm
	s.mu.Unlock()
	return clone(norm), nil
}

// EffectiveSTUN returns the STUN list to use: the configured one, or the
// defaults when none is configured.
func EffectiveSTUN(st protocol.Settings) []string {
	if len(st.STUNServers) == 0 || sameList(st.STUNServers, oldDefaultSTUN) {
		return append([]string(nil), DefaultSTUN...)
	}
	return append([]string(nil), st.STUNServers...)
}

// oldDefaultSTUN is the 0.3.x default. A settings file that saved exactly
// this list gets the new defaults: Google's servers are slow or filtered on
// many networks and stalled every invite.
var oldDefaultSTUN = []string{"stun:stun.l.google.com:19302", "stun:stun.cloudflare.com:3478"}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != b[i] {
			return false
		}
	}
	return true
}

// Normalize trims and validates settings.
func Normalize(in protocol.Settings) (protocol.Settings, error) {
	out := protocol.Settings{AllowRelay: in.AllowRelay}

	name := []rune{}
	for _, r := range strings.TrimSpace(in.DisplayName) {
		if r < 0x20 || r == 0x7f {
			continue
		}
		name = append(name, r)
		if len(name) >= MaxDisplayName {
			break
		}
	}
	out.DisplayName = strings.TrimSpace(string(name))

	for _, u := range in.STUNServers {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if err := checkURL(u, "stun", "stuns"); err != nil {
			return protocol.Settings{}, err
		}
		out.STUNServers = append(out.STUNServers, u)
	}
	if len(out.STUNServers) > MaxServers {
		return protocol.Settings{}, protocol.NewErrorf(protocol.CodeBadRequest,
			"settings: at most %d STUN servers", MaxServers)
	}
	for _, t := range in.TURNServers {
		t.URL = strings.TrimSpace(t.URL)
		if t.URL == "" {
			continue
		}
		if err := checkURL(t.URL, "turn", "turns"); err != nil {
			return protocol.Settings{}, err
		}
		t.Username = strings.TrimSpace(t.Username)
		out.TURNServers = append(out.TURNServers, t)
	}
	if len(out.TURNServers) > MaxServers {
		return protocol.Settings{}, protocol.NewErrorf(protocol.CodeBadRequest,
			"settings: at most %d TURN servers", MaxServers)
	}
	if in.Metered != nil && (strings.TrimSpace(in.Metered.App) != "" || strings.TrimSpace(in.Metered.Key) != "") {
		m := protocol.MeteredRelay{
			App: strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.Metered.App), ".metered.live")),
			Key: strings.TrimSpace(in.Metered.Key),
		}
		if !meteredApp(m.App) {
			return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest,
				"settings: the Metered app name is the part before .metered.live (letters, digits, '-')")
		}
		if !meteredKey(m.Key) {
			return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest,
				"settings: the Metered API key looks wrong (copy it from the Metered dashboard)")
		}
		out.Metered = &m
	}
	net := protocol.DefaultNetworkSettings()
	if in.Network != nil {
		net = *in.Network
	}
	if net.MTU != 0 && (net.MTU < 576 || net.MTU > 1400) {
		return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest,
			"settings: MTU must be between 576 and 1400, or 0 for the default")
	}
	if net.BroadcastRate < 0 || net.BroadcastRate > 2000 {
		return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest,
			"settings: the discovery budget must be between 0 and 2000 packets/s")
	}
	if net.PortMin < 0 || net.PortMax > 65535 || net.PortMin > net.PortMax ||
		(net.PortMin == 0) != (net.PortMax == 0) || (net.PortMin != 0 && net.PortMax-net.PortMin < 9) {
		return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest,
			"settings: the UDP port range must be like 50000-50100 (at least 10 ports), or 0-0 for any")
	}
	switch net.RoomMode {
	case "":
		net.RoomMode = protocol.RoomModeL3
	case protocol.RoomModeL3, protocol.RoomModeL2:
	default:
		return protocol.Settings{}, protocol.NewErrorf(protocol.CodeBadRequest, "settings: unknown room mode %q", net.RoomMode)
	}
	if net.RelayOnly && (!in.AllowRelay || len(out.TURNServers) == 0) {
		return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest,
			"settings: relay-only needs a TURN server and \"use a relay\" turned on")
	}
	out.Network = &net
	if out.AllowRelay && len(out.TURNServers) == 0 {
		return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest,
			"settings: relay is enabled but no TURN server is configured")
	}
	return out, nil
}

// checkURL accepts "scheme:host[:port][?transport=udp|tcp]".
func checkURL(raw string, schemes ...string) error {
	i := strings.IndexByte(raw, ':')
	if i <= 0 {
		return protocol.NewErrorf(protocol.CodeBadRequest,
			"settings: %q must look like %s:host:port", raw, schemes[0])
	}
	scheme := strings.ToLower(raw[:i])
	ok := false
	for _, s := range schemes {
		if scheme == s {
			ok = true
		}
	}
	if !ok {
		return protocol.NewErrorf(protocol.CodeBadRequest,
			"settings: %q must start with %s:", raw, strings.Join(schemes, ": or "))
	}
	rest := raw[i+1:]
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		if _, err := url.ParseQuery(rest[q+1:]); err != nil {
			return protocol.NewErrorf(protocol.CodeBadRequest, "settings: %q has a bad query: %v", raw, err)
		}
		rest = rest[:q]
	}
	if strings.TrimSpace(rest) == "" || strings.ContainsAny(rest, " /") {
		return protocol.NewErrorf(protocol.CodeBadRequest, "settings: %q has no usable host", raw)
	}
	return nil
}

func clone(s protocol.Settings) protocol.Settings {
	s.STUNServers = append([]string(nil), s.STUNServers...)
	s.TURNServers = append([]protocol.RelayServer(nil), s.TURNServers...)
	if s.Network != nil {
		n := *s.Network
		s.Network = &n
	} else {
		n := protocol.DefaultNetworkSettings()
		s.Network = &n
	}
	return s
}

// Net returns the effective network settings.
func Net(s protocol.Settings) protocol.NetworkSettings {
	if s.Network == nil {
		return protocol.DefaultNetworkSettings()
	}
	return *s.Network
}

func meteredApp(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func meteredKey(s string) bool {
	if len(s) < 8 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
