package network

import (
	"net/netip"
	"sync"
	"time"
)

// Broadcast amplification limits.
//
// # The problem
//
// A guest that can send broadcast packets can make the host send N copies of
// each one, to every other guest. One guest sending a 100-byte packet to a
// seven-guest room turns into 600 bytes on the host's uplink, and the cost is
// paid by the host, which is the machine the user least wants to slow down. It
// is the classic amplification attack, and the guest is the attacker because
// the guest is the party with something to gain.
//
// # Why the limit is per peer and not global
//
// A global limit would let one noisy peer consume the whole room's budget and
// then stop everyone else's traffic - including a legitimate game - from
// getting through. That is a denial of service dressed up as a fix. A per-peer
// bucket means a peer can only ever hurt itself.
//
// # Why the numbers are generous
//
// A token bucket with a burst equal to the rate permits a sustained rate and
// absorbs a discovery burst. The defaults allow a hundred fan-out packets a
// second per peer, which is far above what any game's LAN discovery does -
// Minecraft announces on the order of once every few seconds - and still two
// orders of magnitude below what would hurt a link.
const (
	broadcastRate  = 100.0
	broadcastBurst = 100.0
	// bucketTTL is how long an idle bucket is kept before it is reclaimed. A
	// peer that disconnects and never comes back would otherwise leave an entry
	// behind forever, and a room that has seen many guests over a week would
	// accumulate one per guest.
	bucketTTL = 5 * time.Minute
)

// broadcastLimiter is a set of per-source token buckets.
//
// It is not a general-purpose rate limiter: it has no notion of cost, only of
// packets, and it is only ever consulted for packets that are about to be
// multiplied by the size of the room.
type broadcastLimiter struct {
	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
	rate    float64
	burst   float64
	now     func() time.Time
}

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

// newBroadcastLimiter builds a limiter. now is injectable so a test can advance
// time without sleeping.
func newBroadcastLimiter(rate, burst float64, now func() time.Time) *broadcastLimiter {
	if rate <= 0 {
		rate = broadcastRate
	}
	if burst <= 0 {
		burst = broadcastBurst
	}
	if now == nil {
		now = time.Now
	}
	return &broadcastLimiter{
		buckets: make(map[netip.Addr]*bucket),
		rate:    rate,
		burst:   burst,
		now:     now,
	}
}

// allow consumes one token for src and reports whether the packet may be
// relayed.
//
// A source seen for the first time starts with a full bucket, because the burst
// allowance exists precisely to let the first discovery announcement through
// without waiting for the bucket to fill.
func (l *broadcastLimiter) allow(src netip.Addr, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	b, ok := l.buckets[src]
	if !ok {
		l.buckets[src] = &bucket{tokens: l.burst - 1, last: now, lastSeen: now}
		return true
	}
	// Refill for the time that has passed. Negative elapsed time - a clock that
	// went backwards - would take tokens away, so it is clamped to zero rather
	// than trusted.
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
	}
	b.last = now
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets nobody has touched for bucketTTL.
//
// It runs on the request path rather than on a timer because the limiter only
// exists inside a room, and a room with no broadcast traffic has nothing to
// sweep. The map is small - one entry per peer that ever sent a broadcast - so
// walking it costs nothing measurable.
func (l *broadcastLimiter) sweep(now time.Time) {
	cutoff := now.Add(-bucketTTL)
	for src, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, src)
		}
	}
}

// Len returns the number of live buckets, for tests.
func (l *broadcastLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
