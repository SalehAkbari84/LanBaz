package peer

import (
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// canTransition is the rule that keeps Pion's unordered callbacks from
// resurrecting a dead link, so it gets tested directly and exhaustively rather
// than only through the paths that happen to be exercised.

func TestForwardTransitionsAreAllowed(t *testing.T) {
	ladder := []protocol.PeerState{
		protocol.PeerNew,
		protocol.PeerDiscovering,
		protocol.PeerSignaling,
		protocol.PeerICEChecking,
		protocol.PeerConnected,
		protocol.PeerDTLS,
		protocol.PeerDataChannel,
		protocol.PeerNetworkReady,
		protocol.PeerActive,
	}
	for i, from := range ladder {
		for j, to := range ladder {
			want := j >= i
			if got := canTransition(from, to); got != want {
				t.Errorf("canTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestActiveAndDegradedAreLateral(t *testing.T) {
	for _, pair := range [][2]protocol.PeerState{
		{protocol.PeerActive, protocol.PeerDegraded},
		{protocol.PeerDegraded, protocol.PeerActive},
	} {
		if !canTransition(pair[0], pair[1]) {
			t.Errorf("canTransition(%s, %s) = false, want true", pair[0], pair[1])
		}
	}
	// Degraded is not a step forward, so it must not be reachable by skipping
	// the ladder and must not let a link skip ahead of it either.
	if canTransition(protocol.PeerNew, protocol.PeerDegraded) {
		t.Error("a new peer must not jump straight to degraded")
	}
	if canTransition(protocol.PeerDegraded, protocol.PeerConnected) {
		t.Error("a degraded link must not fall back to connected")
	}
}

func TestBackwardTransitionsAreRefused(t *testing.T) {
	backward := [][2]protocol.PeerState{
		{protocol.PeerNetworkReady, protocol.PeerDTLS},
		{protocol.PeerDataChannel, protocol.PeerConnected},
		{protocol.PeerDTLS, protocol.PeerICEChecking},
		{protocol.PeerSignaling, protocol.PeerNew},
		{protocol.PeerActive, protocol.PeerConnected},
	}
	for _, pair := range backward {
		if canTransition(pair[0], pair[1]) {
			t.Errorf("canTransition(%s, %s) = true; a late callback must not walk a peer backwards",
				pair[0], pair[1])
		}
	}
}

func TestFailureIsFinal(t *testing.T) {
	if canTransition(protocol.PeerFailed, protocol.PeerActive) {
		t.Error("a failed link must not come back")
	}
	if canTransition(protocol.PeerFailed, protocol.PeerDisconnected) {
		t.Error("a failed link must not become disconnected")
	}
}

func TestAnyLiveStateCanDrop(t *testing.T) {
	for _, s := range []protocol.PeerState{
		protocol.PeerNew, protocol.PeerSignaling, protocol.PeerICEChecking,
		protocol.PeerConnected, protocol.PeerNetworkReady, protocol.PeerActive,
	} {
		if !canTransition(s, protocol.PeerDisconnected) {
			t.Errorf("canTransition(%s, disconnected) = false, want true", s)
		}
		if !canTransition(s, protocol.PeerFailed) {
			t.Errorf("canTransition(%s, failed) = false, want true", s)
		}
	}
}

func TestDisconnectedCanRecover(t *testing.T) {
	// Recovery has to be allowed: ICE can find a new path on a connection that
	// merely went quiet, and a VPN that never recovers on its own is a VPN the
	// user has to restart.
	if !canTransition(protocol.PeerDisconnected, protocol.PeerActive) {
		t.Error("a disconnected link must be able to recover")
	}
	if !canTransition(protocol.PeerDisconnected, protocol.PeerDisconnected) {
		t.Error("repeated disconnection must be tolerated, not treated as unknown")
	}
}

func TestUnknownStatesAreRefused(t *testing.T) {
	if canTransition(protocol.PeerState("bogus"), protocol.PeerActive) {
		t.Error("an unknown source state must not advance")
	}
	if canTransition(protocol.PeerActive, protocol.PeerState("bogus")) {
		t.Error("an unknown destination state must not be accepted")
	}
}

// --- estimator ---

func TestEstimatorNeedsASampleBeforeReporting(t *testing.T) {
	var e estimator
	snap := e.snapshot()
	if snap.RTT != 0 || snap.MinRTT != 0 || snap.MaxRTT != 0 {
		t.Errorf("an empty estimator reported rtt=%s min=%s max=%s, want all zero",
			snap.RTT, snap.MinRTT, snap.MaxRTT)
	}
	if snap.Loss != 0 {
		t.Errorf("an empty estimator reported loss %v, want 0", snap.Loss)
	}
}

func TestFirstSampleSeedsBothEstimates(t *testing.T) {
	var e estimator
	e.observe(40 * time.Millisecond)
	snap := e.snapshot()
	// RFC 6298: the first sample is the mean outright, so the UI does not have to
	// converge before it can show a number.
	if snap.RTT != 40*time.Millisecond {
		t.Errorf("rtt after one sample = %s, want 40ms", snap.RTT)
	}
	if snap.MinRTT != 40*time.Millisecond || snap.MaxRTT != 40*time.Millisecond {
		t.Errorf("extremes after one sample = %s..%s, want 40ms..40ms", snap.MinRTT, snap.MaxRTT)
	}
}

func TestEstimatorIgnoresNonPositiveSamples(t *testing.T) {
	var e estimator
	e.observe(20 * time.Millisecond)
	e.observe(0)
	e.observe(-5 * time.Millisecond)
	if got := e.snapshot().RTT; got != 20*time.Millisecond {
		t.Errorf("rtt = %s after bogus samples, want the original 20ms", got)
	}
	if got := e.snapshot().Received; got != 1 {
		t.Errorf("received = %d, want 1; a bogus sample is not a measurement", got)
	}
}

func TestJitterTracksVariation(t *testing.T) {
	var steady, varying estimator
	for i := 0; i < 20; i++ {
		steady.observe(30 * time.Millisecond)
	}
	for i := 0; i < 20; i++ {
		// Alternate by 10ms, which a correct jitter estimate must notice.
		if i%2 == 0 {
			varying.observe(25 * time.Millisecond)
		} else {
			varying.observe(35 * time.Millisecond)
		}
	}
	if steady.snapshot().Jitter != 0 {
		t.Errorf("a perfectly steady link reported jitter %s, want 0", steady.snapshot().Jitter)
	}
	if varying.snapshot().Jitter == 0 {
		t.Error("a link alternating by 10ms reported zero jitter")
	}
}

func TestLossWindowAgesSamplesOut(t *testing.T) {
	var e estimator
	// Fill the window with successes, then lose the next two.
	for i := 0; i < LossWindowSize; i++ {
		e.observe(10 * time.Millisecond)
	}
	if got := e.snapshot().Loss; got != 0 {
		t.Fatalf("loss after a clean window = %v, want 0", got)
	}
	e.recordTimeout()
	e.recordTimeout()
	if got := e.snapshot().Loss; got == 0 {
		t.Fatal("two lost probes inside the window reported no loss")
	}

	// Fill past the window: the early successes must age out and the ratio must
	// return to zero rather than staying permanently impaired.
	for i := 0; i < LossWindowSize; i++ {
		e.observe(10 * time.Millisecond)
	}
	if got := e.snapshot().Loss; got != 0 {
		t.Errorf("loss after the window refilled = %v, want 0", got)
	}
}

func TestLossRatioIsBoundedAndCorrect(t *testing.T) {
	var e estimator
	for i := 0; i < 10; i++ {
		e.recordSent()
	}
	for i := 0; i < 4; i++ {
		e.recordTimeout()
	}
	for i := 0; i < 6; i++ {
		e.observe(5 * time.Millisecond)
	}
	snap := e.snapshot()
	if snap.Sent != 10 {
		t.Errorf("sent = %d, want 10", snap.Sent)
	}
	if snap.Loss < 0.39 || snap.Loss > 0.41 {
		t.Errorf("loss = %v, want about 0.4", snap.Loss)
	}
	if snap.Received != 6 {
		t.Errorf("received = %d, want 6", snap.Received)
	}
}

func TestExtremesExcludeLosses(t *testing.T) {
	var e estimator
	e.observe(50 * time.Millisecond)
	e.observe(10 * time.Millisecond)
	if got := e.snapshot().MinRTT; got != 10*time.Millisecond {
		t.Errorf("min = %s, want 10ms", got)
	}
	if got := e.snapshot().MaxRTT; got != 50*time.Millisecond {
		t.Errorf("max = %s, want 50ms", got)
	}
}

func TestNonPositiveSamplesDoNotBreakExtremes(t *testing.T) {
	var e estimator
	e.observe(30 * time.Millisecond)
	e.observe(-time.Second)
	e.observe(0)
	snap := e.snapshot()
	if snap.MinRTT != 30*time.Millisecond || snap.MaxRTT != 30*time.Millisecond {
		t.Errorf("extremes = %s..%s, want 30ms..30ms", snap.MinRTT, snap.MaxRTT)
	}
}
