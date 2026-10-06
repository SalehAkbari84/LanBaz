// Package logging builds the daemon logger.
//
// The daemon writes a human readable line to stderr (so `lanbazd` in a terminal
// shows "LanBaz daemon started" and "API listening on 127.0.0.1:<port>") and a
// detailed line to <state-dir>/daemon.log. Both sinks pass through the security
// redactor, so the API token and any future room secret are masked even if a
// message formats them with %v.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lanbaz/lanbaz/core/internal/config"
	"github.com/lanbaz/lanbaz/core/internal/security"
)

// LevelTrace sits below slog.LevelDebug. It is used for per-packet tracing that
// must never be enabled by default because of volume.
const LevelTrace = slog.LevelDebug - 4

// Options configures New.
type Options struct {
	Level     string
	StateDir  string
	Redactor  *security.Redactor
	LogToFile bool
	// Stderr is the console sink; tests inject a buffer instead.
	Stderr io.Writer
	// Ring, when set, also receives every record (debug included) for the
	// in-app developer log.
	Ring *Ring
}

// Logger is the daemon logger plus the closers it owns.
type Logger struct {
	*slog.Logger
	closers []io.Closer
	once    sync.Once
}

// LevelVar is the live level, so the control API can change verbosity without a
// restart once the daemon.shutdown/settings surface lands.
type LevelVar struct {
	level *slog.LevelVar
}

// New builds the logger described by opts.
func New(opts Options) (*Logger, error) {
	var level slog.LevelVar
	if err := setLevel(&level, opts.Level); err != nil {
		return nil, err
	}

	redactor := opts.Redactor
	if redactor == nil {
		redactor = security.NewRedactor()
	}

	conOpts := &slog.HandlerOptions{
		Level:       &level,
		ReplaceAttr: redactAttr,
	}
	handlers := []slog.Handler{slog.NewTextHandler(stderrOr(opts.Stderr), conOpts)}

	var closers []io.Closer
	if opts.LogToFile && opts.StateDir != "" {
		if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
			return nil, fmt.Errorf("create state dir for logs: %w", err)
		}
		f, err := os.OpenFile(filepath.Join(opts.StateDir, "daemon.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		if err := security.RestrictToOwner(filepath.Join(opts.StateDir, "daemon.log")); err != nil {
			f.Close()
			return nil, fmt.Errorf("secure log file: %w", err)
		}
		closers = append(closers, f)
		fileOpts := &slog.HandlerOptions{Level: &level, ReplaceAttr: redactAttr}
		handlers = append(handlers, slog.NewTextHandler(f, fileOpts))
	}

	if opts.Ring != nil {
		handlers = append(handlers, ringHandler{ring: opts.Ring})
	}
	handler := security.NewRedactingHandler(newMultiHandler(handlers), redactor)
	return &Logger{Logger: slog.New(handler), closers: closers}, nil
}

// NewLevelVar returns a live level holder plus its setter, exposed so tests and
// a future daemon.settings method can adjust verbosity at runtime.
func NewLevelVar(initial string) (*LevelVar, error) {
	var lv slog.LevelVar
	if err := setLevel(&lv, initial); err != nil {
		return nil, err
	}
	return &LevelVar{level: &lv}, nil
}

// Set changes the level at runtime.
func (l *LevelVar) Set(name string) error { return setLevel(l.level, name) }

// String returns the current level name.
func (l *LevelVar) String() string { return l.level.Level().String() }

// Level exposes the underlying slog level var.
func (l *LevelVar) Level() *slog.LevelVar { return l.level }

func setLevel(lv *slog.LevelVar, name string) error {
	switch name {
	case config.LogLevelTrace:
		lv.Set(LevelTrace)
	case config.LogLevelDebug:
		lv.Set(slog.LevelDebug)
	case config.LogLevelInfo:
		lv.Set(slog.LevelInfo)
	case config.LogLevelWarn:
		lv.Set(slog.LevelWarn)
	case config.LogLevelError:
		lv.Set(slog.LevelError)
	default:
		return fmt.Errorf("unknown log level %q (want trace|debug|info|warn|error)", name)
	}
	return nil
}

func stderrOr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}

// multiHandler fans one log record out to several sinks. slog gained a
// MultiHandler only recently, and a tiny local implementation keeps the daemon
// buildable on the Go versions listed in go.mod.
type multiHandler struct {
	handlers []slog.Handler
}

func newMultiHandler(handlers []slog.Handler) slog.Handler {
	if len(handlers) == 1 {
		return handlers[0]
	}
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, rec slog.Record) error {
	var firstErr error
	for _, h := range m.handlers {
		if !h.Enabled(ctx, rec.Level) {
			continue
		}
		if err := h.Handle(ctx, rec.Clone()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: next}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		next[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: next}
}

// redactAttr drops the raw PC/line noise from the console sink so the startup
// banner reads cleanly, and removes anything that looks like a secret from log
// attribute values.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.LevelKey:
		return a
	}
	if a.Value.Kind() == slog.KindGroup {
		return a
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, maskTokens(a.Value.String()))
	}
	return a
}

// maskTokens masks values that follow a "key=value" or "key: value" shape where
// the key names a secret. This is a cheap backstop for log statements written
// before the redactor existed; the redactor itself remains the primary defence.
//
// The scan advances past each replacement rather than restarting, otherwise the
// still-present key prefix would be matched again forever.
func maskTokens(s string) string {
	needles := []string{
		"token=", "token:", "secret=", "secret:",
		"private_key=", "private_key:", "password=", "password:",
	}
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		valueStart := matchSecretKey(s, i, needles)
		if valueStart < 0 || valueStart >= len(s) {
			// No value follows the key: "token=" at the end of the line.
			b.WriteByte(s[i])
			i++
			continue
		}
		// A quoted value keeps its quotes so the log line stays parseable.
		if q := s[valueStart]; q == '"' || q == '\'' {
			close := valueStart + 1
			for close < len(s) && s[close] != q {
				close++
			}
			if close == valueStart+1 || close >= len(s) {
				// An opening quote with no matching close: nothing safe to mask.
				b.WriteByte(s[i])
				i++
				continue
			}
			// valueStart+1 keeps the opening quote in the copied prefix.
			b.WriteString(s[i : valueStart+1])
			b.WriteString(security.Redacted)
			b.WriteByte(q)
			i = close + 1
			continue
		}

		// Unquoted value: scan up to a delimiter.
		end := valueStart
		for end < len(s) && !strings.ContainsRune(" \t\n\r,}\"'", rune(s[end])) {
			end++
		}
		if end == valueStart {
			// No value to mask: "token=" on its own. Leave the line untouched
			// rather than inserting a placeholder for an absent secret.
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteString(s[i:valueStart])
		b.WriteString(security.Redacted)
		i = end
	}
	return b.String()
}

// matchSecretKey reports where a value begins when a secret key starts at
// position i, or -1 when no key matches there.

func matchSecretKey(s string, i int, needles []string) int {
	for _, needle := range needles {
		if i+len(needle) <= len(s) && equalFold(s[i:i+len(needle)], needle) {
			return i + len(needle)
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// Close releases the log file handles.
func (l *Logger) Close() error {
	var err error
	l.once.Do(func() {
		for _, c := range l.closers {
			if cerr := c.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
	})
	return err
}
