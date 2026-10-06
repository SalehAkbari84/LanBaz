package webrtc

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// The smart-connection monitor. While a link is checking paths it reads the
// per-path counters Pion keeps (checks sent, answered, and the friend's checks
// received) and writes what they mean to the log - which side's router is
// dropping packets, whether a relay is available to fall back on - instead of
// a bare "could not connect". ICE itself does the falling back: relay
// candidates are part of every link when a relay is configured, and they win
// automatically when no direct path answers.

const (
	monitorEvery    = 5 * time.Second
	diagnoseAfter   = 20 * time.Second
	rediagnoseEvery = 30 * time.Second
)

type pathTotals struct {
	pairs, sent, answered, received uint64
	localRelay, remoteRelay         bool
	localPublic                     bool
}

func (l *link) pathTotals() pathTotals {
	var t pathTotals
	st := l.pc.GetStats()
	kinds := map[string]webrtc.ICECandidateType{}
	for _, s := range st {
		if c, ok := s.(webrtc.ICECandidateStats); ok {
			kinds[c.ID] = c.CandidateType
			switch {
			case c.Type == webrtc.StatsTypeLocalCandidate && c.CandidateType == webrtc.ICECandidateTypeRelay:
				t.localRelay = true
			case c.Type == webrtc.StatsTypeLocalCandidate && c.CandidateType == webrtc.ICECandidateTypeSrflx:
				t.localPublic = true
			case c.Type == webrtc.StatsTypeRemoteCandidate && c.CandidateType == webrtc.ICECandidateTypeRelay:
				t.remoteRelay = true
			}
		}
	}
	for _, s := range st {
		if p, ok := s.(webrtc.ICECandidatePairStats); ok {
			t.pairs++
			t.sent += p.RequestsSent
			t.answered += p.ResponsesReceived
			t.received += p.RequestsReceived
		}
	}
	return t
}

// diagnosis turns the counters into a sentence for the log.
func (t pathTotals) diagnosis() string {
	relay := t.localRelay || t.remoteRelay
	switch {
	case t.pairs == 0:
		return "no path to try yet: the other side's reply has not been applied"
	case t.received == 0 && t.answered == 0 && !relay:
		return "nothing from the other player arrives and none of our checks are answered: either they have not finished joining, or both routers drop unknown incoming packets. A relay (Settings → Relay) gets around this"
	case t.received == 0 && t.answered == 0:
		return "no direct path answers yet; the relay path is being tried"
	case t.received > 0 && t.answered == 0 && !relay:
		return "the other player's checks reach this PC, but ours are never answered: their router (or firewall) drops incoming packets. A relay on either side gets around this"
	case t.received > 0 && t.answered == 0:
		return "their checks arrive but ours are not answered; falling back to the relay"
	default:
		return "paths answer in both directions; the encrypted handshake is completing"
	}
}

var monitorOnce sync.Map // *link -> struct{}

// monitor logs path progress until the link connects or closes.
func (l *link) monitor() {
	if _, dup := monitorOnce.LoadOrStore(l, struct{}{}); dup {
		return
	}
	defer monitorOnce.Delete(l)
	start := time.Now()
	tick := time.NewTicker(monitorEvery)
	defer tick.Stop()
	var lastDiag time.Time
	var last pathTotals
	for {
		select {
		case <-l.closedCh:
			return
		case <-l.connectedCh:
			return
		case <-tick.C:
		}
		t := l.pathTotals()
		if t != last {
			l.log.Debug("trying paths", "paths", t.pairs, "checks_sent", t.sent,
				"answered", t.answered, "received_from_peer", t.received,
				"relay_local", t.localRelay, "relay_remote", t.remoteRelay)
			last = t
		}
		if time.Since(start) >= diagnoseAfter && time.Since(lastDiag) >= rediagnoseEvery {
			lastDiag = time.Now()
			l.log.Warn("still connecting: "+t.diagnosis(),
				"after", time.Since(start).Round(time.Second), "paths", t.pairs,
				"checks_sent", t.sent, "answered", t.answered, "received_from_peer", t.received,
				"public_address", t.localPublic, "relay_available", t.localRelay || t.remoteRelay)
		}
	}
}
