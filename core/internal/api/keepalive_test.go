package api

import (
	"context"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// An idle but healthy UI connection must stay up. In 0.3.1 the read deadline
// was set once after the handshake and never extended, so every UI session
// dropped exactly pongWait after it connected.
func TestIdleConnectionSurvivesPongWait(t *testing.T) {
	oldWait, oldPing := pongWait, pingPeriod
	pongWait, pingPeriod = 1500*time.Millisecond, 300*time.Millisecond
	defer func() { pongWait, pingPeriod = oldWait, oldPing }()

	s, _ := newTestServer(t, nil)
	c := dial(t, s)
	time.Sleep(3 * pongWait)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var st protocol.DaemonStatus
	if err := c.Call(ctx, protocol.MethodDaemonStatus, nil, &st); err != nil {
		t.Fatalf("the idle connection was dropped: %v", err)
	}
}
