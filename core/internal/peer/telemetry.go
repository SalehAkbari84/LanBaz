package peer

import (
	"sync"
	"time"
)

// Estimator tuning constants.
//
// These are the coefficients from RFC 6298 (TCP's retransmission timer) and RFC
// 3550 (RTP's jitter estimate). They are not magic numbers tuned for this
// project: they are the well-reviewed values for exactly this problem, and
// deviating from them without a measurement to justify it would only make the
// numbers less comparable with every other implementation on the network.
const (
	// rttAlpha is the weight given to a new RTT sample in the smoothed mean.
	rttAlpha = 0.125
	// rttBeta is the weight given to the deviation in the variance estimate.
	rttBeta = 0.25
	// jitterGain is RFC 3550's 1/16. Larger values track change faster but
	// report a noisier number.
	jitterGain = 1.0 / 16.0
)

// LossWindowSize is how many recent probe outcomes the loss ratio is computed
// over.
//
// A ping every two seconds fills this in just over two minutes. That is long
// enough that one congested moment does not dominate the number, and short
// enough that the UI visibly recovers after a problem instead of averaging it
// away for the rest of the session.
const LossWindowSize = 64

// Telemetry is the measurement half of a peer's health. It is a snapshot: the
// caller cannot use it to mutate the estimator.
type Telemetry struct {
	// RTT is the smoothed round trip time, zero until a probe has completed.
	RTT time.Duration
	// Jitter is the smoothed variation in round trip time.
	Jitter time.Duration
	// Loss is the ratio of probes that did not come back, over LossWindowSize.
	Loss float64
	// MinRTT and MaxRTT are the extremes of the samples in the current window.
	// Both are zero while no sample has completed.
	MinRTT time.Duration
	MaxRTT time.Duration
	// Sent and Received are lifetime counters of probes.
	Sent     uint64
	Received uint64
	// Samples is the number of round trips folded into the estimates.
	Samples uint64
}

// sample is one probe outcome. The round trip time is kept rather than just a
// boolean so the window's extremes can be recomputed after an eviction instead
// of being tracked incrementally and drifting out of sync with the ring.
type sample struct {
	replied bool
	rtt     time.Duration
}

// estimator turns round trip samples into the numbers the UI shows.
//
// One goroutine per peer feeds it (the manager's liveness loop), but Summary may
// be read from an API handler at the same time, so it is mutex-guarded. The
// critical sections are a few integer operations and never span a channel send.
type estimator struct {
	mu sync.Mutex

	// have distinguishes "no samples yet" from "the samples said zero", which a
	// zero-valued struct cannot express on its own.
	have   bool
	srtt   time.Duration
	rttvar time.Duration
	// last is the previous round trip, which seeds the jitter difference.
	last   time.Duration
	jitter time.Duration

	sent     uint64
	received uint64

	// window is a ring of the most recent LossWindowSize outcomes, oldest
	// first; head is where the next one is written and count how many are live.
	window [LossWindowSize]sample
	head   int
	count  int
	lost   int
}

// recordSent accounts for a probe going out.
func (e *estimator) recordSent() {
	e.mu.Lock()
	e.sent++
	e.mu.Unlock()
}

// observe folds a completed round trip into the estimates.
func (e *estimator) observe(rtt time.Duration) {
	if rtt <= 0 {
		// A non-positive round trip is a clock artefact, not a measurement.
		// Folding it in would drag the mean to zero and the variance with it.
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	e.received++
	e.push(sample{replied: true, rtt: rtt})

	if !e.have {
		// RFC 6298: the first sample is both the mean and, halved, the
		// deviation. Starting the variance anywhere else makes the first few
		// readings jump for no reason.
		e.have = true
		e.srtt = rtt
		e.rttvar = rtt / 2
		e.last = rtt
		return
	}

	// Jitter is the mean absolute difference between consecutive round trips.
	// RFC 3550's transit-time formulation reduces exactly to this, because the
	// unknown one-way delay cancels when two samples are subtracted.
	dev := rtt - e.last
	if dev < 0 {
		dev = -dev
	}
	e.jitter += time.Duration((float64(dev) - float64(e.jitter)) * jitterGain)

	// RFC 6298: deviation is measured against the old mean, so the new mean
	// cannot chase its own tail.
	diff := e.srtt - rtt
	if diff < 0 {
		diff = -diff
	}
	e.rttvar = time.Duration((1-rttBeta)*float64(e.rttvar) + rttBeta*float64(diff))
	e.srtt = time.Duration((1-rttAlpha)*float64(e.srtt) + rttAlpha*float64(rtt))

	e.last = rtt
}

// recordTimeout accounts for a probe that never came back.
func (e *estimator) recordTimeout() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.push(sample{})
}

// push writes one outcome into the ring, evicting the oldest when full.
func (e *estimator) push(s sample) {
	if e.count == LossWindowSize {
		oldest := (e.head - LossWindowSize + LossWindowSize) % LossWindowSize
		if !e.window[oldest].replied {
			e.lost--
		}
	} else {
		e.count++
	}
	e.window[e.head] = s
	e.head = (e.head + 1) % LossWindowSize
	if !s.replied {
		e.lost++
	}
}

// extremesLocked walks the live window. It runs once per probe over at most 64
// entries, which is cheaper than maintaining running extremes incrementally and
// impossible to get subtly wrong when a slow sample ages out of the window.
func (e *estimator) extremesLocked() (min, max time.Duration) {
	first := true
	for i := 0; i < e.count; i++ {
		idx := (e.head - e.count + i + LossWindowSize) % LossWindowSize
		s := e.window[idx]
		if !s.replied {
			continue
		}
		if first {
			min, max, first = s.rtt, s.rtt, false
			continue
		}
		if s.rtt < min {
			min = s.rtt
		}
		if s.rtt > max {
			max = s.rtt
		}
	}
	return min, max
}

// snapshot returns the current estimates.
func (e *estimator) snapshot() Telemetry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Telemetry{
		Jitter:   e.jitter,
		Sent:     e.sent,
		Received: e.received,
		Samples:  e.received,
	}
	if e.count > 0 {
		out.Loss = float64(e.lost) / float64(e.count)
	}
	if e.have {
		out.RTT = e.srtt
		out.MinRTT, out.MaxRTT = e.extremesLocked()
	}
	return out
}
