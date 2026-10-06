package peer

import (
	"fmt"
	"time"
)

// Link quality for the developer log: one line when a player's link gets
// noticeably worse or better, or switches between a direct path and the
// relay, instead of a number that silently changes in the UI.

const (
	qualityGood = "good"
	qualityFair = "fair"
	qualityPoor = "poor"
)

// qualityOf grades a link the way it feels in a game. Too few samples is no
// grade at all, so one lost probe on a new link does not cry wolf.
func qualityOf(t Telemetry) string {
	if t.Samples < 5 {
		return ""
	}
	switch {
	case t.Loss >= 0.05 || t.RTT >= 150*time.Millisecond || t.Jitter >= 40*time.Millisecond:
		return qualityPoor
	case t.Loss >= 0.02 || t.RTT >= 80*time.Millisecond || t.Jitter >= 15*time.Millisecond:
		return qualityFair
	default:
		return qualityGood
	}
}

func qualityRank(q string) int {
	switch q {
	case qualityGood:
		return 2
	case qualityFair:
		return 1
	}
	return 0
}

// noteQuality logs changes of a peer's link grade and path.
func (m *Manager) noteQuality(p *Peer) {
	if p == nil || p.isSelf {
		return
	}
	t := p.est.snapshot()
	s := p.Summary()
	grade := qualityOf(t)

	p.mu.Lock()
	prevGrade, prevKind := p.qGrade, p.qKind
	if grade != "" {
		p.qGrade = grade
	}
	p.qKind = s.LinkKind
	p.mu.Unlock()

	who := s.DisplayName
	if who == "" {
		who = string(s.PeerID)
	}
	numbers := []any{"peer", who, "rtt_ms", t.RTT.Milliseconds(), "jitter_ms", t.Jitter.Milliseconds(),
		"loss", fmt.Sprintf("%.0f%%", t.Loss*100), "path", s.LinkKind}
	if grade != "" && prevGrade != "" && grade != prevGrade {
		msg := fmt.Sprintf("link to %s is now %s (was %s)", who, grade, prevGrade)
		if qualityRank(grade) < qualityRank(prevGrade) {
			if grade == qualityPoor {
				msg += ": games may lag or drop; loss or a relay path is usually the cause"
			}
			m.log.Warn(msg, numbers...)
		} else {
			m.log.Info(msg, numbers...)
		}
	}
	if prevKind != "" && prevKind != "unknown" && s.LinkKind != "unknown" && s.LinkKind != prevKind {
		if s.LinkKind == "relay" {
			m.log.Warn(fmt.Sprintf("link to %s moved from a %s path to the relay: it still works, with more delay", who, prevKind), numbers...)
		} else {
			m.log.Info(fmt.Sprintf("link to %s moved from the %s to a %s path", who, prevKind, s.LinkKind), numbers...)
		}
	}
}
