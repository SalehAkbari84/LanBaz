// Package outage tells an international internet outage apart from no
// internet at all.
//
// During Iran's shutdowns the domestic network keeps working while anything
// abroad is unreachable: LanBaz's default STUN and relay servers, the Nostr
// relays friends use, GitHub (updates). Rooms still work between Iranian
// players - with codes passed over a domestic messenger and, behind
// carrier-grade NAT, a friend's relay - but the user needs to be told what
// works and what does not, instead of watching things time out.
package outage

import (
	"context"
	"net"
	"sync"
	"time"
)

// Mode is the verdict.
type Mode string

const (
	// Normal: the international internet answers.
	Normal Mode = "normal"
	// International: only domestic hosts answer.
	International Mode = "international"
	// Offline: nothing answers.
	Offline Mode = "offline"
)

// Foreign and Domestic are well-known hosts on each side. A TCP connection to
// port 443 is enough: no data is sent, nothing is fetched.
var (
	Foreign  = []string{"github.com:443", "www.cloudflare.com:443", "relay.damus.io:443"}
	Domestic = []string{"www.aparat.com:443", "www.digikala.com:443", "divar.ir:443"}
)

// Result is one check.
type Result struct {
	Mode     Mode      `json:"mode"`
	Foreign  int       `json:"foreign_ok"`
	Domestic int       `json:"domestic_ok"`
	At       time.Time `json:"at"`
}

// Dialer is swapped in tests.
type Dialer func(ctx context.Context, addr string) error

// TCPDial connects and closes.
func TCPDial(ctx context.Context, addr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// Check probes both sides at once; it takes at most timeout.
func Check(ctx context.Context, dial Dialer, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	count := func(hosts []string) int {
		var wg sync.WaitGroup
		var mu sync.Mutex
		n := 0
		for _, h := range hosts {
			wg.Add(1)
			go func(h string) {
				defer wg.Done()
				if dial(ctx, h) == nil {
					mu.Lock()
					n++
					mu.Unlock()
				}
			}(h)
		}
		wg.Wait()
		return n
	}
	var f, d int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); f = count(Foreign) }()
	go func() { defer wg.Done(); d = count(Domestic) }()
	wg.Wait()
	r := Result{Foreign: f, Domestic: d, At: time.Now().UTC()}
	switch {
	case f > 0:
		r.Mode = Normal
	case d > 0:
		r.Mode = International
	default:
		r.Mode = Offline
	}
	return r
}
