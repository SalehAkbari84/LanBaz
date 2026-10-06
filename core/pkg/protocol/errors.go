package protocol

import (
	"errors"
	"fmt"
)

// Error codes are part of the public contract between the daemon and its local
// clients. Codes are stable, uppercase and machine-readable; the human readable
// message may change at any time and must never be parsed by a client.
//
// The full set is declared here (not only the ones implemented in Phase 0) so
// that the UI, the CLI and the daemon share one source of truth and so that
// later phases cannot silently invent a second spelling for the same failure.
const (
	// Phase 0 - control plane.
	CodeUnauthorized         = "UNAUTHORIZED"
	CodeBadRequest           = "BAD_REQUEST"
	CodeRateLimited          = "RATE_LIMITED"
	CodeNotFound             = "NOT_FOUND"
	CodeInternal             = "INTERNAL"
	CodePayloadTooLarge      = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedVersion   = "UNSUPPORTED_VERSION"
	CodeTimeout              = "TIMEOUT"
	CodeDaemonShuttingDown   = "DAEMON_SHUTTING_DOWN"
	CodeMethodNotAllowed     = "METHOD_NOT_ALLOWED"
	CodeHandshakeTimeout     = "HANDSHAKE_TIMEOUT"
	CodeTooManyConnections   = "TOO_MANY_CONNECTIONS"
	CodeConfigInvalid        = "CONFIG_INVALID"
	CodeStateFileUnwritable  = "STATE_FILE_UNWRITABLE"
	CodeDaemonAlreadyRunning = "DAEMON_ALREADY_RUNNING"

	// Phase 1+ - room, pairing and peer lifecycle.
	CodeNATBlocked       = "NAT_BLOCKED"
	CodeICEFailed        = "ICE_FAILED"
	CodeSTUNUnavailable  = "STUN_UNAVAILABLE"
	CodePairingExpired   = "PAIRING_EXPIRED"
	CodePairingInvalid   = "PAIRING_INVALID"
	CodePairingReplay    = "PAIRING_REPLAY"
	CodeRoomFull         = "ROOM_FULL"
	CodePeerRejected     = "PEER_REJECTED"
	CodePeerSpoofed      = "PEER_SPOOFED"
	CodeTransportTimeout = "TRANSPORT_TIMEOUT"

	// Phase 2+ - virtual network and host integration.
	CodeWintunCreateFailed     = "WINTUN_CREATE_FAILED"
	CodeWintunPermissionDenied = "WINTUN_PERMISSION_DENIED"
	CodeRouteCreateFailed      = "ROUTE_CREATE_FAILED"
	CodeIPAMExhausted          = "IPAM_EXHAUSTED"
	CodeFirewallFailed         = "FIREWALL_FAILED"
	CodeGameNotFound           = "GAME_NOT_FOUND"
	CodeGameProfileInvalid     = "GAME_PROFILE_INVALID"
	CodeBroadcastNotSupported  = "BROADCAST_NOT_SUPPORTED"
	CodeMulticastNotSupported  = "MULTICAST_NOT_SUPPORTED"
)

// Error is the machine-readable error payload of a failed response.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Code + ": " + e.Message
}

// NewError builds an API error.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// NewErrorf builds an API error with a formatted message.
func NewErrorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithDetails attaches structured details to an API error.
func (e *Error) WithDetails(details map[string]any) *Error {
	if e == nil {
		return nil
	}
	e.Details = details
	return e
}

// HasCode reports whether the error carries the given machine-readable code.
// The name deliberately avoids Is, which the error package reserves for
// Is(error) bool.
func (e *Error) HasCode(code string) bool {
	return e != nil && e.Code == code
}

// CodeOf returns the protocol code carried by err, or CodeInternal when err is
// not a protocol error. It lets a layer re-wrap an error with a friendlier
// message without losing the code a client branches on.
func CodeOf(err error) string {
	var pe *Error
	if errors.As(err, &pe) && pe.Code != "" {
		return pe.Code
	}
	return CodeInternal
}
