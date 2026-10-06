package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one log record as the developer panel shows it.
type Entry struct {
	Seq   uint64    `json:"seq"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"` // DEBUG, INFO, WARN, ERROR
	Msg   string    `json:"msg"`
	Attrs string    `json:"attrs,omitempty"`
}

// Ring keeps the most recent log records in memory for the in-app developer
// log, including debug records the log file does not get. It sits behind the
// redacting handler, so secrets never reach it.
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	next    uint64
	size    int
	notify  func(Entry)
}

// NewRing keeps up to size records.
func NewRing(size int) *Ring { return &Ring{size: size} }

// OnEntry sets a callback for every new record (the daemon pushes them to the UI).
func (r *Ring) OnEntry(fn func(Entry)) {
	r.mu.Lock()
	r.notify = fn
	r.mu.Unlock()
}

// Since returns the records after seq, oldest first.
func (r *Ring) Since(seq uint64) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out
}

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	r.next++
	e.Seq = r.next
	r.entries = append(r.entries, e)
	if len(r.entries) > r.size {
		r.entries = r.entries[len(r.entries)-r.size:]
	}
	fn := r.notify
	r.mu.Unlock()
	if fn != nil {
		fn(e)
	}
}

// ringHandler adapts a Ring to slog.
type ringHandler struct {
	ring   *Ring
	attrs  []slog.Attr
	groups string
}

func (h ringHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h ringHandler) Handle(_ context.Context, rec slog.Record) error {
	var b strings.Builder
	write := func(a slog.Attr) {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s%s=%v", h.groups, a.Key, a.Value)
	}
	for _, a := range h.attrs {
		write(a)
	}
	rec.Attrs(func(a slog.Attr) bool { write(a); return true })
	h.ring.add(Entry{Time: rec.Time, Level: rec.Level.String(), Msg: maskTokens(rec.Message), Attrs: maskTokens(b.String())})
	return nil
}

func (h ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return ringHandler{ring: h.ring, attrs: append(append([]slog.Attr(nil), h.attrs...), as...), groups: h.groups}
}

func (h ringHandler) WithGroup(name string) slog.Handler {
	return ringHandler{ring: h.ring, attrs: h.attrs, groups: h.groups + name + "."}
}
