// Package api implements the localhost control API of the LanBaz daemon.
//
// Transport: a WebSocket on a dynamically chosen loopback port, plus a
// loopback-only /healthz liveness endpoint. The WebSocket is the only way to
// issue commands; /healthz exposes no state and no secrets so that a liveness
// probe cannot learn anything about rooms or peers.
//
// Every connection must send a `hello` request carrying the token from
// <state-dir>/daemon.json before any other method is dispatched. The socket is
// bound to loopback only, which is enforced by config.Validate.
package api

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Handler executes one method. params is the raw request payload; the handler
// decodes it into its own request struct. The returned value is marshalled as
// the response payload.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// registry maps method names to handlers. Methods are registered once at
// startup; the registry itself is immutable afterwards, so dispatch is a plain
// map read guarded by an RWMutex.
type registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func newRegistry() *registry {
	return &registry{handlers: make(map[string]Handler)}
}

func (r *registry) register(method string, h Handler) error {
	if method == "" {
		return protocol.NewError(protocol.CodeInternal, "method name must not be empty")
	}
	if h == nil {
		return protocol.NewErrorf(protocol.CodeInternal, "handler for %s is nil", method)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[method]; exists {
		return protocol.NewErrorf(protocol.CodeInternal, "method %s is already registered", method)
	}
	r.handlers[method] = h
	return nil
}

func (r *registry) lookup(method string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[method]
	return h, ok
}

func (r *registry) methods() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for m := range r.handlers {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
