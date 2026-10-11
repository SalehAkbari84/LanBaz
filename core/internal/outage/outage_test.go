package outage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fake(up map[string]bool) Dialer {
	return func(ctx context.Context, addr string) error {
		if up[addr] {
			return nil
		}
		<-ctx.Done() // an unreachable host hangs until the timeout, as in a shutdown
		return errors.New("timeout")
	}
}

func TestModes(t *testing.T) {
	all := map[string]bool{}
	domestic := map[string]bool{}
	for _, h := range Foreign {
		all[h] = true
	}
	for _, h := range Domestic {
		all[h] = true
		domestic[h] = true
	}
	oneForeign := map[string]bool{Foreign[1]: true}

	cases := []struct {
		name string
		up   map[string]bool
		want Mode
	}{
		{"everything answers", all, Normal},
		{"one foreign host is enough", oneForeign, Normal},
		{"only Iranian hosts", domestic, International},
		{"nothing", map[string]bool{}, Offline},
	}
	for _, c := range cases {
		start := time.Now()
		r := Check(context.Background(), fake(c.up), 300*time.Millisecond)
		if r.Mode != c.want {
			t.Errorf("%s: mode = %s, want %s (%+v)", c.name, r.Mode, c.want, r)
		}
		if time.Since(start) > time.Second {
			t.Errorf("%s: took %v, the timeout is 300 ms", c.name, time.Since(start))
		}
	}
}
