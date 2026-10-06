package api

import (
	"sync"
	"time"
)

// rateLimiter is a token bucket: it allows bursts up to `burst` and then
// refills at `perSecond` requests per second. Each connection owns one, so a
// single misbehaving local client cannot starve the UI.
//
// A limiter with perSecond <= 0 is unlimited.
type rateLimiter struct {
	mu        sync.Mutex
	perSecond float64
	burst     float64
	tokens    float64
	last      time.Time
	now       func() time.Time
}

func newRateLimiter(perSecond float64, burst int) *rateLimiter {
	if perSecond <= 0 {
		return &rateLimiter{perSecond: 0, burst: 0}
	}
	b := float64(burst)
	if b < 1 {
		b = 1
	}
	return &rateLimiter{
		perSecond: perSecond,
		burst:     b,
		tokens:    b,
		now:       time.Now,
	}
}

// allow reports whether one request may proceed right now.
func (l *rateLimiter) allow() bool {
	if l == nil || l.perSecond <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if l.last.IsZero() {
		l.last = now
	}
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.perSecond
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
