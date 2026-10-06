package transport

import (
	"context"
	"errors"
	"strconv"
)

// Signalling is the transport-agnostic description of the offer/answer exchange
// every connection-oriented transport needs before traffic can flow.
//
// This interface exists because LanBaz must be able to establish a WebRTC link
// with no room server in the loop. The only way two hosts behind NAT can find
// each other is if enough of the handshake to produce candidates travels
// between them out of band. LanBaz uses the pairing code for exactly that: the
// initiator's description is compressed into the code, the responder's reply is
// compressed into a reply code, and the human carries it between the two
// machines. A relay or a room server, when one exists, is just a faster courier
// for the same two blobs.
//
// Nothing above this interface learns that the payloads are SDP. A future
// direct-UDP transport can implement Signalling by exchanging an address and a
// token instead of an SDP offer, and the room, the peer manager and the UI stay
// unchanged.
type Signalling interface {
	// CreateOffer produces the local description to send to a peer and records
	// the pending negotiation. It returns the opaque description blob, which
	// the caller is free to base64 into a pairing code.
	CreateOffer(ctx context.Context, peer PeerInfo) (Description, error)
	// AcceptOffer consumes a remote description, produces the local reply
	// description and returns it to the caller.
	AcceptOffer(ctx context.Context, peer PeerInfo, remote Description) (Description, error)
	// ApplyAnswer completes the negotiation started by CreateOffer.
	ApplyAnswer(ctx context.Context, peer PeerInfo, remote Description) error
	// WaitConnected blocks until the peer link is usable, the negotiation
	// fails, or ctx is done.
	WaitConnected(ctx context.Context, peer PeerInfo) error
}

// Description is an opaque, transport-specific handshake blob.
//
// It is deliberately untyped. The pairing code carries it as an opaque string
// so that the pairing package stays independent of WebRTC, and so that a future
// UDP transport can put something entirely different in the same field.
type Description struct {
	// Kind identifies the description role for transports that need one, e.g.
	// "offer" and "answer". It is informational; implementations must not
	// depend on it for correctness.
	Kind string
	// Data is the transport-specific payload, typically a session description.
	Data []byte
}

// Description kinds.
const (
	DescriptionOffer  = "offer"
	DescriptionAnswer = "answer"
)

// Signalling-capable is implemented by transports that can bootstrap a link
// without a rendezvous server. A transport that cannot should return
// ErrSignallingUnsupported so the room can fall back to a server-assisted path
// instead of failing obscurely.
type SignallingCapable interface {
	SupportsSignalling() bool
}

// ErrSignallingUnsupported is returned by a transport that cannot bootstrap a
// link out of band.
var ErrSignallingUnsupported = errors.New("transport: out-of-band signalling is not supported")

// PayloadTooLargeError reports a description or packet above the transport's
// own bound, distinct from the transport-wide MTU so the UI can tell "your
// pairing code is too big" from "that game packet did not fit".
type PayloadTooLargeError struct {
	Size int
	Max  int
}

// Error implements the error interface.
func (e *PayloadTooLargeError) Error() string {
	return "transport: payload of " + strconv.Itoa(e.Size) +
		" bytes exceeds the limit of " + strconv.Itoa(e.Max)
}
