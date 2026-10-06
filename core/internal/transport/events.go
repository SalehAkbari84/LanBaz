package transport

import (
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// StateEvent is one link state transition reported by a transport.
//
// Transports report the mechanical facts of a connection - ICE picked a pair,
// DTLS finished, a data channel opened - and nothing about health. Deciding
// that a link is "active" or "degraded" belongs to the peer manager, which is
// the only component that sees latency, loss and history.
type StateEvent struct {
	// Peer is the link the event is about.
	Peer PeerID
	// State is the new state.
	State protocol.PeerState
	// Code is a machine-readable failure code, set only for terminal states.
	// The UI branches on it; it must never be a Pion or OS error string.
	Code string
	// Reason is human-readable and for logs and display only.
	Reason string
}

// StateReporter is implemented by transports that can push state transitions
// instead of forcing the caller to poll Ready().
//
// It exists so that exactly one component owns the peer state machine. A
// transport that also kept its own copy of the state would produce two machines
// that can disagree - the transport hearing ICE complete while the manager has
// already written the link off as failed - and the UI would render whichever
// was written last.
type StateReporter interface {
	// SetStateHandler installs the transition sink. Passing nil removes it,
	// which is what a shutting-down manager does to break the reference cycle
	// between the transport and itself.
	SetStateHandler(func(StateEvent))
}

// ControlTransport is implemented by transports that carry a reliable control
// channel alongside the data path.
//
// It is separate from Transport on purpose. The reliable channel carries
// liveness probes and state messages whose loss must never be confused with
// game packet loss, and it is also the channel a future session key exchange
// would use. Those needs exist on every transport, but a hypothetical raw UDP
// transport might reasonably have none, and forcing SendControl onto the base
// interface would let it return a fake success. Separate interfaces let the
// room check once and report a clean capability failure instead.
type ControlTransport interface {
	// SendControl delivers one control frame. It blocks briefly for the
	// channel to open, because control frames are small and must not be
	// dropped.
	SendControl(peer PeerID, payload []byte) error
}

// ControlReceiver is the inbound half of ControlTransport.
//
// It is a separate method rather than a field on Transport because the handler
// is installed once by the peer manager and must be removable when the manager
// closes, or the transport would keep a closed room alive.
type ControlReceiver interface {
	// SetControlHandler installs the inbound frame sink. Passing nil removes it.
	//
	// The payload belongs to the receiver and must be copied if retained; the
	// transport reuses its read buffer.
	SetControlHandler(fn func(peer PeerID, payload []byte))
}
