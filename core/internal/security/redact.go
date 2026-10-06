package security

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// Redacted is the placeholder that replaces any registered secret.
const Redacted = "[redacted]"

// Redactor masks secret strings inside log output. The daemon registers the API
// token (and later room secrets and private keys) before the first log line, so
// an accidental %v of a secret can never reach daemon.log or a LogEvent pushed
// to the UI.
//
// A Redactor is safe for concurrent use and deliberately refuses to register
// very short values: masking a 1-2 character "secret" would shred unrelated
// log output instead of protecting anything.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor {
	return &Redactor{}
}

const minRedactableLength = 8

// Register adds a secret to the mask list. Empty and very short values are
// ignored; Register reports whether the value was accepted.
func (r *Redactor) Register(secret string) bool {
	if len(secret) < minRedactableLength {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.secrets {
		if existing == secret {
			return true
		}
	}
	r.secrets = append(r.secrets, secret)
	return true
}

// String masks every registered secret in s.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Redacted)
		}
	}
	return s
}

// Has reports whether at least one secret is registered.
func (r *Redactor) Has() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.secrets) > 0
}

// NewRedactingHandler wraps base so that both the message and every string
// attribute value pass through the redactor. Attribute *keys* are left alone so
// that structured queries (slog.Attr("peer_id", ...)) keep working.
func NewRedactingHandler(base slog.Handler, r *Redactor) slog.Handler {
	if r == nil {
		return base
	}
	return &redactingHandler{base: base, redactor: r}
}

type redactingHandler struct {
	base     slog.Handler
	redactor *Redactor
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.redactor.String(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.base.Handle(ctx, out)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		redacted = append(redacted, h.redactAttr(a))
	}
	return &redactingHandler{base: h.base.WithAttrs(redacted), redactor: h.redactor}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{base: h.base.WithGroup(name), redactor: h.redactor}
}

func (h *redactingHandler) redactAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.redactor.String(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, h.redactor.String(err.Error()))
		}
		if s, ok := a.Value.Any().(fmt.Stringer); ok {
			return slog.String(a.Key, h.redactor.String(s.String()))
		}
	}
	return a
}
