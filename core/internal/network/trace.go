package network

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// LAN traffic tracer for the developer log.
//
// When a game does not show up in a friend's LAN list, the question is always
// the same: did the game's discovery packet reach LanBaz at all, did it leave
// for the other PC, and did the other PC write it to its adapter? The tracer
// answers that. It logs every new kind of flow once (direction, broadcast,
// multicast or unicast, protocol, port, what happened to it) and a count of
// each every 30 s, without logging every packet of a 60 Hz game.

type flowKey struct {
	dir    string // "out" (a game on this PC sent it) or "in" (from the room)
	kind   string // broadcast, multicast, unicast
	proto  string
	port   uint16
	action string
}

type tracer struct {
	log     *slog.Logger
	mu      sync.Mutex
	seen    map[flowKey]uint64 // packets per flow in the current 30 s window
	told    map[flowKey]bool   // flows already announced; never repeated
	last    time.Time
	started time.Time
	anyOut  bool
	warned  bool
	gameAt  time.Time // when the running game was first seen; zero if none

	// A server browser sweeps a port range (Source games broadcast to 27015
	// through 27040 at once); the sweep is one line, not 26.
	sweepAt   time.Time
	sweepKey  flowKey
	sweepMore int
}

func newTracer(log *slog.Logger, now time.Time) *tracer {
	return &tracer{log: log, seen: map[flowKey]uint64{}, told: map[flowKey]bool{}, last: now, started: now}
}

func flowOf(payload []byte, dir, action string, subnet netip.Prefix) (flowKey, string, bool) {
	h, err := ParseIPv4(payload)
	if err != nil {
		return flowKey{}, "", false
	}
	k := flowKey{dir: dir, action: action, kind: "unicast", proto: fmt.Sprintf("proto %d", h.Protocol)}
	switch {
	case h.IsLimitedBroadcast() || h.IsSubnetBroadcast(subnet):
		k.kind = "broadcast"
	case h.IsMulticast():
		k.kind = "multicast"
	}
	switch h.Protocol {
	case 6, 17:
		if len(payload) >= h.HeaderLen+4 {
			k.port = binary.BigEndian.Uint16(payload[h.HeaderLen+2:])
		}
		if h.Protocol == 6 {
			k.proto = "TCP"
		} else {
			k.proto = "UDP"
		}
	case 1:
		k.proto = "ICMP"
	case 2:
		k.proto = "IGMP"
	}
	return k, fmt.Sprintf("%s → %s", h.Src, h.Dst), true
}

// noise are Windows' own chatty ports; counted, but never announced.
var noise = map[uint16]bool{137: true, 138: true, 1900: true, 5353: true, 5355: true, 3702: true, 67: true, 68: true}

func (t *tracer) note(payload []byte, dir, action string, subnet netip.Prefix, now time.Time) {
	if t == nil {
		return
	}
	k, addrs, ok := flowOf(payload, dir, action, subnet)
	if !ok || k.proto == "IGMP" {
		return
	}
	t.mu.Lock()
	t.seen[k]++
	if dir == "out" && k.kind != "unicast" && !noise[k.port] {
		t.anyOut = true
	}
	announce := !t.told[k] && !noise[k.port]
	if announce {
		t.told[k] = true
		sk := k
		sk.port = 0
		if k.kind != "unicast" && sk == t.sweepKey && now.Sub(t.sweepAt) < 2*time.Second {
			// Part of a sweep that was just announced.
			t.sweepMore++
			announce = false
		} else {
			t.sweepKey, t.sweepAt = sk, now
		}
	}
	var summary map[flowKey]uint64
	more := 0
	if now.Sub(t.last) >= 30*time.Second {
		summary, more = t.seen, t.sweepMore
		t.seen, t.sweepMore = map[flowKey]uint64{}, 0
		t.last = now
	}
	t.mu.Unlock()

	if announce {
		what := "game traffic"
		if k.kind != "unicast" {
			what = "LAN discovery"
		}
		lvl := slog.LevelInfo
		if action == "dropped" {
			lvl = slog.LevelWarn
		}
		t.log.Log(context.Background(), lvl, fmt.Sprintf("%s %s: %s %s port %d %s", what, arrow(dir), k.kind, k.proto, k.port, action),
			"packet", addrs)
	}
	if summary != nil {
		t.summarize(summary, more)
	}
}

// summarize logs one line per 30 s: how many packets went each way and on
// which ports, which is what tells a live match (thousands) from a game that
// only exchanged a few probes.
func (t *tracer) summarize(seen map[flowKey]uint64, sweepMore int) {
	type portCount struct {
		label string
		n     uint64
	}
	var out, in, dropped uint64
	var outPorts, inPorts []portCount
	for k, n := range seen {
		if noise[k.port] {
			continue
		}
		pc := portCount{fmt.Sprintf("%s %d", k.proto, k.port), n}
		if k.proto == "ICMP" {
			pc.label = "ping"
		}
		switch {
		case k.action == "dropped":
			dropped += n
		case k.dir == "out":
			out += n
			outPorts = append(outPorts, pc)
		default:
			in += n
			inPorts = append(inPorts, pc)
		}
	}
	if out+in+dropped == 0 {
		return
	}
	top := func(ps []portCount) string {
		sort.Slice(ps, func(i, j int) bool { return ps[i].n > ps[j].n })
		if len(ps) > 3 {
			ps = ps[:3]
		}
		parts := make([]string, len(ps))
		for i, p := range ps {
			parts[i] = fmt.Sprintf("%s ×%d", p.label, p.n)
		}
		return strings.Join(parts, ", ")
	}
	msg := fmt.Sprintf("game traffic, last 30 s: %d packets sent to the room (%s), %d received from the room (%s)",
		out, top(outPorts), in, top(inPorts))
	if sweepMore > 0 {
		msg += fmt.Sprintf("; LAN search also covered %d more ports", sweepMore)
	}
	if dropped > 0 {
		t.log.Warn(msg + fmt.Sprintf("; %d packets dropped", dropped))
		return
	}
	t.log.Info(msg)
}

// checkSilence warns once when a game has run for a minute (and the room has
// been open for one) without any LAN discovery from this PC reaching LanBaz.
func (t *tracer) checkSilence(now time.Time, gameRunning bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if !gameRunning {
		t.gameAt = time.Time{}
	} else if t.gameAt.IsZero() {
		t.gameAt = now
	}
	since := t.started
	if t.gameAt.After(since) {
		since = t.gameAt
	}
	warn := !t.warned && !t.anyOut && gameRunning && now.Sub(since) > time.Minute
	if warn {
		t.warned = true
	}
	t.mu.Unlock()
	if warn {
		t.log.Warn("a game has been running for a minute and has not searched for or announced a LAN game through LanBaz yet. Fine if you are still in the menus; if you are already in the LAN / System Link screen, the game is using another network adapter: for GFWL games restart the game after joining the room, for others pick the LanBaz adapter in the game's settings or connect directly to the host's 10.200 address")
	}
}

func arrow(dir string) string {
	if dir == "out" {
		return "from this PC →"
	}
	return "from the room ←"
}
