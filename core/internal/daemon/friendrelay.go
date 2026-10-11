package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/friendrelay"
	"github.com/lanbaz/lanbaz/core/internal/network/egress"
	"github.com/lanbaz/lanbaz/core/internal/network/portmap"
	"github.com/lanbaz/lanbaz/core/internal/outage"
	"github.com/lanbaz/lanbaz/core/internal/settings"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// The relay for friends and the outage check. See packages friendrelay and
// outage for why they exist.

const (
	relayLease     = time.Hour
	relayRenew     = 30 * time.Minute
	relayFirewall  = "LanBaz-FriendRelay"
	outageCacheFor = time.Minute
)

type relayHost struct {
	mu       sync.Mutex
	srv      *friendrelay.Server
	client   portmap.Client
	mappings []portmap.Mapping
	public   netip.Addr
	via      string
	err      string
	stop     chan struct{}

	outageMu sync.Mutex
	outage   *protocol.OutageStatus

	// Swapped in tests.
	findPublic func(ctx context.Context) (netip.Addr, portmap.Client, error)
	firewall   func(ctx context.Context, port int, on bool) error
}

// publicAddress finds how friends can reach this PC: a public address on the
// PC itself, or the router's public address with port forwarding.
func publicAddress(ctx context.Context) (netip.Addr, portmap.Client, error) {
	if ifs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() && portmap.Public(p.Addr()) {
				return p.Addr(), nil, nil
			}
		}
	}
	local, gw := egress.PhysicalGateway()
	if !local.IsValid() || !gw.IsValid() {
		return netip.Addr{}, nil, errors.New("no network adapter with a router")
	}
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	c, err := portmap.Discover(dctx, local, gw)
	if err != nil {
		return netip.Addr{}, nil, err
	}
	ext, err := c.ExternalIP(dctx)
	if err != nil {
		return netip.Addr{}, nil, err
	}
	if !portmap.Public(ext) {
		return netip.Addr{}, nil, portmap.ErrNotPublic
	}
	return ext, c, nil
}

func relayFirewallRule(ctx context.Context, port int, on bool) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	script := fmt.Sprintf("Remove-NetFirewallRule -Name %s -ErrorAction SilentlyContinue", psQuote(relayFirewall))
	if on {
		script += fmt.Sprintf(";New-NetFirewallRule -Name %s -DisplayName %s -Group 'LanBaz' -Direction Inbound -Action Allow -Protocol UDP -LocalPort %d-%d -Profile Any | Out-Null",
			psQuote(relayFirewall), psQuote("LanBaz: relay for friends"), port, port+friendrelay.RelayPorts)
	}
	out, err := runPS(ctx, script+"; exit 0")
	if err != nil {
		return fmt.Errorf("%v %s", err, strings.TrimSpace(out))
	}
	return nil
}

func (d *Daemon) relayCfg() protocol.FriendRelayHost {
	st := d.settings.Get()
	if st.FriendRelay == nil {
		return protocol.FriendRelayHost{}
	}
	return *st.FriendRelay
}

// startFriendRelay starts the relay when the settings say so.
func (d *Daemon) startFriendRelay(ctx context.Context) {
	cfg := d.relayCfg()
	h := &d.relay
	h.mu.Lock()
	defer h.mu.Unlock()
	if !cfg.Enabled || h.srv != nil {
		return
	}
	if h.findPublic == nil {
		h.findPublic = publicAddress
	}
	if h.firewall == nil {
		h.firewall = relayFirewallRule
	}
	port := cfg.Port
	if port == 0 {
		port = friendrelay.DefaultPort
	}
	h.err = ""
	pub, client, err := h.findPublic(ctx)
	if err != nil {
		if errors.Is(err, portmap.ErrNotPublic) {
			h.err = "cgnat"
		} else {
			h.err = "no_router: " + err.Error()
		}
		d.log.Warn("the relay for friends cannot start: this PC is not reachable from the internet", "error", err)
		return
	}
	var maps []portmap.Mapping
	if client != nil {
		for _, p := range friendrelay.Ports(port) {
			mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			m, err := client.Map(mctx, uint16(p), relayLease)
			cancel()
			if err != nil || int(m.External) != p {
				for _, done := range maps {
					_ = client.Unmap(context.Background(), done)
				}
				if err == nil {
					_ = client.Unmap(context.Background(), m)
					err = fmt.Errorf("the router gave port %d instead of %d", m.External, p)
				}
				h.err = "router: " + err.Error()
				d.log.Warn("the router did not forward the relay's ports", "error", err)
				return
			}
			maps = append(maps, m)
		}
	}
	if err := h.firewall(ctx, port, true); err != nil {
		d.log.Warn("could not open the firewall for the relay", "error", err)
	}
	srv, err := friendrelay.Start(friendrelay.Config{Port: port, Public: pub, User: cfg.User, Pass: cfg.Pass, CapMbps: cfg.CapMbps, Log: d.log})
	if err != nil {
		for _, m := range maps {
			_ = client.Unmap(context.Background(), m)
		}
		h.err = err.Error()
		return
	}
	h.srv, h.client, h.mappings, h.public = srv, client, maps, pub
	h.via = "public"
	if client != nil {
		h.via = client.Method()
	}
	h.stop = make(chan struct{})
	if client != nil {
		go d.renewRelayPorts(h.stop)
	}
}

// renewRelayPorts keeps the router's forwarding alive.
func (d *Daemon) renewRelayPorts(stop chan struct{}) {
	t := time.NewTicker(relayRenew)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			h := &d.relay
			h.mu.Lock()
			for i, m := range h.mappings {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if nm, err := h.client.Map(ctx, m.Internal, relayLease); err == nil {
					h.mappings[i] = nm
				}
				cancel()
			}
			h.mu.Unlock()
		}
	}
}

func (d *Daemon) stopFriendRelay() {
	h := &d.relay
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.srv == nil {
		return
	}
	close(h.stop)
	_ = h.srv.Close()
	for _, m := range h.mappings {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_ = h.client.Unmap(ctx, m)
		cancel()
	}
	if h.firewall != nil {
		_ = h.firewall(context.Background(), 0, false)
	}
	h.srv, h.client, h.mappings, h.public, h.via = nil, nil, nil, netip.Addr{}, ""
}

func (d *Daemon) friendRelayStatus() protocol.FriendRelayStatus {
	cfg := d.relayCfg()
	h := &d.relay
	h.mu.Lock()
	defer h.mu.Unlock()
	st := protocol.FriendRelayStatus{Enabled: cfg.Enabled, Port: cfg.Port, CapMbps: cfg.CapMbps, Error: h.err}
	if st.Port == 0 {
		st.Port = friendrelay.DefaultPort
	}
	if st.CapMbps == 0 {
		st.CapMbps = friendrelay.DefaultCapMbps
	}
	if h.srv != nil {
		st.Running = true
		st.Public = h.public.String()
		st.Via = h.via
		st.RelayedMB = h.srv.Relayed() / 1_000_000
		name := d.settings.Get().DisplayName
		if name == "" {
			name = hostname()
		}
		st.Code = h.srv.Code(name).Encode()
	}
	return st
}

func (d *Daemon) setFriendRelay(ctx context.Context, req protocol.FriendRelaySetRequest) (protocol.FriendRelayStatus, error) {
	cur := d.settings.Get()
	fr := protocol.FriendRelayHost{}
	if cur.FriendRelay != nil {
		fr = *cur.FriendRelay
	}
	fr.Enabled = req.Enabled
	if req.CapMbps > 0 {
		fr.CapMbps = req.CapMbps
	}
	// The credential is made once and kept, so a code friends already have
	// keeps working after a restart.
	if fr.User == "" || fr.Pass == "" {
		fr.User, fr.Pass = "lanbaz", friendrelay.NewSecret()
	}
	cur.FriendRelay = &fr
	if _, err := d.settings.Set(cur); err != nil {
		return protocol.FriendRelayStatus{}, err
	}
	d.stopFriendRelay()
	if req.Enabled {
		d.startFriendRelay(ctx)
	}
	return d.friendRelayStatus(), nil
}

// addFriendRelay adds a friend's relay as STUN and TURN server and turns the
// relay fallback on. The built-in STUN servers stay in the list.
func (d *Daemon) addFriendRelay(req protocol.FriendRelayAddRequest) (protocol.Settings, error) {
	code, err := friendrelay.ParseCode(req.Code)
	if err != nil {
		return protocol.Settings{}, protocol.NewError(protocol.CodeBadRequest, err.Error())
	}
	stun, turn := code.Servers()
	cur := d.settings.Get()
	next := cur
	next.STUNServers = nil
	for _, s := range settings.EffectiveSTUN(cur) {
		if s != stun {
			next.STUNServers = append(next.STUNServers, s)
		}
	}
	// The friend's relay first: in an outage it is the one that answers.
	next.STUNServers = append([]string{stun}, next.STUNServers...)
	next.TURNServers = []protocol.RelayServer{turn}
	for _, t := range cur.TURNServers {
		if t.URL != turn.URL {
			next.TURNServers = append(next.TURNServers, t)
		}
	}
	if len(next.STUNServers) > settings.MaxServers {
		next.STUNServers = next.STUNServers[:settings.MaxServers]
	}
	if len(next.TURNServers) > settings.MaxServers {
		next.TURNServers = next.TURNServers[:settings.MaxServers]
	}
	next.AllowRelay = true
	return settingsService{d: d}.Set(next)
}

func (d *Daemon) outageCheck(ctx context.Context, fresh bool) protocol.OutageStatus {
	h := &d.relay
	h.outageMu.Lock()
	defer h.outageMu.Unlock()
	if !fresh && h.outage != nil && time.Since(h.outage.At) < outageCacheFor {
		return *h.outage
	}
	r := outage.Check(ctx, outage.TCPDial, 4*time.Second)
	st := protocol.OutageStatus{Mode: string(r.Mode), ForeignOK: r.Foreign, DomesticOK: r.Domestic, At: r.At}
	if h.outage == nil || h.outage.Mode != st.Mode {
		d.log.Info("internet reachability", "mode", st.Mode, "foreign_ok", r.Foreign, "domestic_ok", r.Domestic)
	}
	h.outage = &st
	return st
}

func (d *Daemon) registerFriendRelay() error {
	reg := []struct {
		name string
		fn   func(ctx context.Context, raw json.RawMessage) (any, error)
	}{
		{protocol.MethodFriendRelayStatus, func(ctx context.Context, _ json.RawMessage) (any, error) {
			return d.friendRelayStatus(), nil
		}},
		{protocol.MethodFriendRelaySet, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FriendRelaySetRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, protocol.NewErrorf(protocol.CodeBadRequest, "bad request: %v", err)
			}
			return d.setFriendRelay(ctx, req)
		}},
		{protocol.MethodFriendRelayAdd, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FriendRelayAddRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, protocol.NewErrorf(protocol.CodeBadRequest, "bad request: %v", err)
			}
			return d.addFriendRelay(req)
		}},
		{protocol.MethodOutageCheck, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req struct {
				Fresh bool `json:"fresh"`
			}
			_ = json.Unmarshal(raw, &req)
			return d.outageCheck(ctx, req.Fresh), nil
		}},
	}
	for _, r := range reg {
		if err := d.api.Register(r.name, r.fn); err != nil {
			return err
		}
	}
	return nil
}
