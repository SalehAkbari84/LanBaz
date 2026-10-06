package peer

import (
	"testing"
	"time"
)

func TestQualityOf(t *testing.T) {
	ms := time.Millisecond
	for _, c := range []struct {
		t    Telemetry
		want string
	}{
		{Telemetry{Samples: 2, RTT: 500 * ms}, ""},
		{Telemetry{Samples: 20, RTT: 25 * ms, Jitter: 3 * ms}, qualityGood},
		{Telemetry{Samples: 20, RTT: 95 * ms}, qualityFair},
		{Telemetry{Samples: 20, RTT: 30 * ms, Loss: 0.03}, qualityFair},
		{Telemetry{Samples: 20, RTT: 30 * ms, Loss: 0.08}, qualityPoor},
		{Telemetry{Samples: 20, RTT: 210 * ms}, qualityPoor},
		{Telemetry{Samples: 20, RTT: 40 * ms, Jitter: 50 * ms}, qualityPoor},
	} {
		if got := qualityOf(c.t); got != c.want {
			t.Errorf("%+v: %q, want %q", c.t, got, c.want)
		}
	}
}
