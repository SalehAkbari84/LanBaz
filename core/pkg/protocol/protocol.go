// Package protocol defines the local control API contract that is shared by the
// LanBaz daemon (lanbazd) and its local clients: the Tauri/React UI and the
// lanbazctl CLI.
//
// The JSON envelopes in this package are used ONLY on the localhost control API.
// Packets that travel over the virtual LAN between peers use the binary framing
// documented in docs/protocol.md and are never encoded as JSON.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	// Version is the control API protocol version. Clients send it on every
	// request; the daemon rejects a mismatched value with UNSUPPORTED_VERSION.
	Version = 1

	// VersionString is the human readable protocol version.
	VersionString = "1.0"

	// APIPath is the WebSocket path served by the daemon.
	APIPath = "/api"

	// HealthPath is a loopback-only, unauthenticated liveness endpoint. It
	// intentionally exposes no secrets and no room/peer information.
	HealthPath = "/healthz"

	// MaxMessageBytes bounds a single control message (request or response).
	// It protects the daemon from memory exhaustion by a local peer.
	MaxMessageBytes = 1 << 20 // 1 MiB

	// MaxRequestIDLen bounds the correlation id length.
	MaxRequestIDLen = 64

	// MaxMethodLen bounds a method or event name.
	MaxMethodLen = 128
)

// Kind discriminates the three message families that share one envelope.
type Kind string

const (
	KindRequest  Kind = "request"
	KindResponse Kind = "response"
	KindEvent    Kind = "event"
)

// Message is the single envelope used for requests, responses and events.
//
// Request:  {"id":"req-1","type":"daemon.status","version":1,"payload":{}}
// Response: {"id":"req-1","type":"response","version":1,"success":true,"payload":{}}
// Event:    {"id":"evt-1","type":"daemon.state","version":1,"payload":{}}
type Message struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Version int             `json:"version"`
	Kind    Kind            `json:"kind,omitempty"`
	Success *bool           `json:"success,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// NewRequestID returns a cryptographically random correlation id such as
// "req-9f2c1a0b4d7e8f10".
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on supported platforms; if it somehow
		// does we still return a unique-enough id rather than panicking in a
		// daemon that is only serving a local UI.
		return "req-0000000000000000"
	}
	return "req-" + hex.EncodeToString(b[:])
}

// NewEventID returns a correlation id for server initiated events.
func NewEventID() string {
	return NewRequestID()
}

// NewRequest builds a request message. A nil payload encodes as null.
func NewRequest(id, method string, payload any) (Message, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:      id,
		Type:    method,
		Version: Version,
		Kind:    KindRequest,
		Payload: raw,
	}, nil
}

// NewResponse builds a successful response for the given request id.
func NewResponse(id string, payload any) (Message, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	ok := true
	return Message{
		ID:      id,
		Type:    string(KindResponse),
		Version: Version,
		Kind:    KindResponse,
		Success: &ok,
		Payload: raw,
	}, nil
}

// NewErrorResponse builds a failed response carrying a machine-readable code.
func NewErrorResponse(id string, apiErr *Error) Message {
	ok := false
	if apiErr == nil {
		apiErr = NewError(CodeInternal, "unexpected error")
	}
	return Message{
		ID:      id,
		Type:    string(KindResponse),
		Version: Version,
		Kind:    KindResponse,
		Success: &ok,
		Error:   apiErr,
	}
}

// NewEvent builds a server initiated event.
func NewEvent(name string, payload any) (Message, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:      NewEventID(),
		Type:    name,
		Version: Version,
		Kind:    KindEvent,
		Payload: raw,
	}, nil
}

func marshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, NewErrorf(CodeInternal, "failed to encode payload: %v", err)
	}
	if len(raw) > MaxMessageBytes {
		return nil, NewErrorf(CodePayloadTooLarge, "payload of %d bytes exceeds the %d byte limit", len(raw), MaxMessageBytes)
	}
	return raw, nil
}

// Marshal encodes the message and enforces the global size limit.
func (m Message) Marshal() ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, NewErrorf(CodeInternal, "failed to encode message: %v", err)
	}
	if len(raw) > MaxMessageBytes {
		return nil, NewErrorf(CodePayloadTooLarge, "message of %d bytes exceeds the %d byte limit", len(raw), MaxMessageBytes)
	}
	return raw, nil
}

// Decode parses a raw control message and performs structural validation.
func Decode(raw []byte) (Message, error) {
	if len(raw) > MaxMessageBytes {
		return Message{}, NewErrorf(CodePayloadTooLarge, "message of %d bytes exceeds the %d byte limit", len(raw), MaxMessageBytes)
	}
	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return Message{}, NewErrorf(CodeBadRequest, "malformed message: %v", err)
	}
	if m.Type == "" {
		return Message{}, NewError(CodeBadRequest, "message type is required")
	}
	if len(m.Type) > MaxMethodLen {
		return Message{}, NewErrorf(CodeBadRequest, "message type exceeds %d characters", MaxMethodLen)
	}
	if m.Kind != "" && m.Kind != KindRequest && m.Kind != KindResponse && m.Kind != KindEvent {
		return Message{}, NewErrorf(CodeBadRequest, "unknown message kind %q", m.Kind)
	}
	return m, nil
}

// ValidateRequest checks the invariants the daemon relies on before dispatching
// a request to a handler.
func (m Message) ValidateRequest() error {
	if m.ID == "" {
		return NewError(CodeBadRequest, "request id is required")
	}
	if len(m.ID) > MaxRequestIDLen {
		return NewErrorf(CodeBadRequest, "request id exceeds %d characters", MaxRequestIDLen)
	}
	if m.Version != Version {
		return NewErrorf(CodeUnsupportedVersion, "unsupported protocol version %d, daemon speaks %d", m.Version, Version)
	}
	if m.Error != nil {
		return NewErrorf(CodeBadRequest, "a request must not carry an error object")
	}
	return nil
}

// BindPayload decodes the request payload into v.
func (m Message) BindPayload(v any) error {
	if len(m.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Payload, v); err != nil {
		return NewErrorf(CodeBadRequest, "invalid payload: %v", err)
	}
	return nil
}

// IsSuccess reports whether a response carried success=true.
func (m Message) IsSuccess() bool { return m.Success != nil && *m.Success }

// String renders a short description used by the structured logger.
func (m Message) String() string {
	return fmt.Sprintf("Message{ID:%s Type:%s Kind:%s Version:%d}", m.ID, m.Type, m.Kind, m.Version)
}
