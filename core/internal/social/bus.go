package social

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Bus moves signed Nostr events. Production uses public relays; tests use the
// in-memory bus below, so the friend logic is tested without a network.
type Bus interface {
	Publish(ctx context.Context, ev nostr.Event) error
	// Subscribe delivers matching events until ctx is done.
	Subscribe(ctx context.Context, f nostr.Filter) <-chan nostr.Event
	// Query returns the stored events matching f.
	Query(ctx context.Context, f nostr.Filter) []nostr.Event
}

// relayBus is a Bus over a set of public relays.
type relayBus struct {
	pool   *nostr.SimplePool
	relays []string
}

// NewRelayBus connects lazily to the given relays.
func NewRelayBus(ctx context.Context, relays []string) Bus {
	return &relayBus{pool: nostr.NewSimplePool(ctx), relays: relays}
}

func (b *relayBus) Publish(ctx context.Context, ev nostr.Event) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var lastErr error
	ok := false
	for r := range b.pool.PublishMany(ctx, b.relays, ev) {
		if r.Error == nil {
			ok = true
		} else {
			lastErr = r.Error
		}
	}
	if ok {
		return nil
	}
	if lastErr == nil {
		lastErr = context.DeadlineExceeded
	}
	return lastErr
}

func (b *relayBus) Subscribe(ctx context.Context, f nostr.Filter) <-chan nostr.Event {
	out := make(chan nostr.Event, 64)
	go func() {
		defer close(out)
		seen := map[string]bool{}
		for re := range b.pool.SubscribeMany(ctx, b.relays, f) {
			if re.Event == nil || seen[re.Event.ID] {
				continue
			}
			seen[re.Event.ID] = true
			if len(seen) > 4096 {
				seen = map[string]bool{re.Event.ID: true}
			}
			select {
			case out <- *re.Event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (b *relayBus) Query(ctx context.Context, f nostr.Filter) []nostr.Event {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	seen := map[string]bool{}
	var out []nostr.Event
	for re := range b.pool.FetchMany(ctx, b.relays, f) {
		if re.Event != nil && !seen[re.Event.ID] {
			seen[re.Event.ID] = true
			out = append(out, *re.Event)
		}
	}
	return out
}

// MemBus is an in-process Bus for tests: a relay that keeps every event and
// fans new ones out to matching subscribers.
type MemBus struct {
	mu     sync.Mutex
	events []nostr.Event
	subs   []memSub
}

type memSub struct {
	ctx context.Context
	f   nostr.Filter
	ch  chan nostr.Event
}

// Publish implements Bus. Replaceable events (kind 30000-39999) replace the
// previous one with the same author and d tag, as a relay would.
func (m *MemBus) Publish(_ context.Context, ev nostr.Event) error {
	if ok, err := ev.CheckSignature(); !ok || err != nil {
		return fmt.Errorf("membus: bad signature: %v", err)
	}
	m.mu.Lock()
	if ev.Kind >= 30000 && ev.Kind < 40000 {
		d := ev.Tags.GetD()
		kept := m.events[:0]
		for _, old := range m.events {
			if !(old.Kind == ev.Kind && old.PubKey == ev.PubKey && old.Tags.GetD() == d) {
				kept = append(kept, old)
			}
		}
		m.events = kept
	}
	m.events = append(m.events, ev)
	subs := append([]memSub(nil), m.subs...)
	m.mu.Unlock()
	for _, s := range subs {
		if s.ctx.Err() == nil && s.f.Matches(&ev) {
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
	return nil
}

// Subscribe implements Bus.
func (m *MemBus) Subscribe(ctx context.Context, f nostr.Filter) <-chan nostr.Event {
	ch := make(chan nostr.Event, 256)
	m.mu.Lock()
	// Like a relay, a new subscription first gets the stored events that
	// match, then live ones.
	for i := range m.events {
		if f.Matches(&m.events[i]) {
			select {
			case ch <- m.events[i]:
			default:
			}
		}
	}
	m.subs = append(m.subs, memSub{ctx: ctx, f: f, ch: ch})
	m.mu.Unlock()
	return ch
}

// Query implements Bus.
func (m *MemBus) Query(_ context.Context, f nostr.Filter) []nostr.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []nostr.Event
	for i := range m.events {
		if f.Matches(&m.events[i]) {
			out = append(out, m.events[i])
		}
	}
	return out
}
