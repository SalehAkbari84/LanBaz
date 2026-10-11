package peer

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// fakeLastRx is a transport that only answers LastReceived.
type fakeLastRx struct {
	transport.Transport
	at time.Time
}

func (f *fakeLastRx) LastReceived(transport.PeerID) time.Time { return f.at }

// Probes going unanswered while game data still arrives (a lossy line during a
// match) must not drop the player; with nothing arriving at all, it must.
func TestLateProbesDoNotDropAPeerThatIsStillSending(t *testing.T) {
	clock := time.Unix(1000, 0)
	tr := &fakeLastRx{}
	m := &Manager{
		tr: tr, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:         func() time.Time { return clock },
		peers:       map[transport.PeerID]*Peer{},
		outstanding: map[transport.PeerID]map[uint64]time.Time{},
	}
	p := newPeer("link-1", "room", true, false, m.log, clock)
	p.setState(protocol.PeerActive, "", "")
	m.peers[p.id] = p

	miss := func() {
		m.outMu.Lock()
		m.nextSeq++
		if m.outstanding[p.id] == nil {
			m.outstanding[p.id] = map[uint64]time.Time{}
		}
		m.outstanding[p.id][m.nextSeq] = clock.Add(-2 * pongTimeout) // long overdue
		m.outMu.Unlock()
		m.reap(p)
	}

	// Game data keeps arriving: ten overdue probes change nothing.
	for i := 0; i < 10; i++ {
		clock = clock.Add(time.Second)
		tr.at = clock.Add(-500 * time.Millisecond)
		miss()
	}
	if p.State() != protocol.PeerActive || p.missed != 0 {
		t.Fatalf("a peer that is still sending was demoted: state %s, missed %d", p.State(), p.missed)
	}

	// Silence: the usual rule applies again.
	tr.at = clock.Add(-time.Minute)
	for i := 0; i < missedLimit; i++ {
		clock = clock.Add(time.Second)
		miss()
	}
	if p.State() != protocol.PeerDisconnected {
		t.Fatalf("a silent peer was not marked disconnected: %s", p.State())
	}
}
