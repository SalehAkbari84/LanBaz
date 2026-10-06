package api

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsInitialBurst(t *testing.T) {
	// A fake clock keeps the test deterministic instead of sleeping.
	now := time.Unix(1700000000, 0)
	l := newRateLimiter(10, 3)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.allow() {
			t.Fatalf("request %d must be allowed within the initial burst", i+1)
		}
	}
	if l.allow() {
		t.Fatal("the fourth request must be denied; the burst is exhausted")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newRateLimiter(10, 1)
	l.now = func() time.Time { return now }

	if !l.allow() {
		t.Fatal("the first request must be allowed")
	}
	if l.allow() {
		t.Fatal("the second request must be denied while the bucket is empty")
	}

	// 10 per second means one token every 100ms.
	now = now.Add(150 * time.Millisecond)
	if !l.allow() {
		t.Error("a request after 150ms must be allowed; the bucket refilled")
	}
}

func TestRateLimiterCapsAtBurst(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newRateLimiter(1000, 2)
	l.now = func() time.Time { return now }

	// A long idle period must not bank more than the burst allowance.
	now = now.Add(time.Hour)
	allowed := 0
	for i := 0; i < 10; i++ {
		if l.allow() {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("allowed %d requests after a long idle period, want the burst of 2", allowed)
	}
}

func TestRateLimiterUnlimitedWhenRateIsZero(t *testing.T) {
	l := newRateLimiter(0, 0)
	for i := 0; i < 1000; i++ {
		if !l.allow() {
			t.Fatalf("request %d was denied; a zero rate means unlimited", i+1)
		}
	}
}

func TestRateLimiterWithMinimumBurst(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// A burst of 0 or 1 must still allow one request so a misconfiguration
	// cannot deadlock the control API.
	for _, burst := range []int{0, -5, 1} {
		l := newRateLimiter(1, burst)
		l.now = func() time.Time { return now }
		if !l.allow() {
			t.Errorf("burst %d: the first request must be allowed", burst)
		}
	}
}

func TestRateLimiterIsConcurrencySafe(t *testing.T) {
	l := newRateLimiter(1000, 500)
	done := make(chan int, 8)
	for i := 0; i < 8; i++ {
		go func() {
			allowed := 0
			for j := 0; j < 200; j++ {
				if l.allow() {
					allowed++
				}
			}
			done <- allowed
		}()
	}
	total := 0
	for i := 0; i < 8; i++ {
		total += <-done
	}
	// 8 workers x 200 requests against a 500 burst; some must be denied, and
	// none may be granted beyond the burst plus refills.
	if total > 1600 {
		t.Errorf("granted %d requests, which exceeds the plausible burst ceiling", total)
	}
}
