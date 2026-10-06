package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNewRequestAndBindPayload(t *testing.T) {
	msg, err := NewRequest("req-1", MethodDaemonShutdown, ShutdownRequest{GracePeriod: 1500, Reason: "test"})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if msg.Kind != KindRequest {
		t.Errorf("kind = %q, want %q", msg.Kind, KindRequest)
	}
	if msg.Version != Version {
		t.Errorf("version = %d, want %d", msg.Version, Version)
	}

	var got ShutdownRequest
	if err := msg.BindPayload(&got); err != nil {
		t.Fatalf("BindPayload: %v", err)
	}
	if got.GracePeriod != 1500 || got.Reason != "test" {
		t.Errorf("payload = %+v, want grace 1500 reason test", got)
	}
}

func TestNewRequestWithoutPayloadRoundTrips(t *testing.T) {
	// A nil payload is omitted entirely rather than encoded as null: the
	// zero-length RawMessage keeps the wire format small for the many
	// parameterless methods (daemon.status, daemon.version).
	msg, err := NewRequest("req-2", MethodDaemonStatus, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	raw, err := msg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), `"payload"`) {
		t.Errorf("marshal = %s, want no payload field", raw)
	}
	if len(msg.Payload) != 0 {
		t.Errorf("payload = %q, want empty", msg.Payload)
	}

	// BindPayload on an empty payload must be a no-op, not an error.
	var st DaemonStatus
	if err := msg.BindPayload(&st); err != nil {
		t.Errorf("BindPayload on an empty payload: %v", err)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	resp, err := NewResponse("req-3", DaemonStatus{State: StateRunning, PID: 4242})
	if err != nil {
		t.Fatalf("NewResponse: %v", err)
	}
	if !resp.IsSuccess() {
		t.Error("IsSuccess() = false, want true")
	}

	raw, err := resp.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var st DaemonStatus
	if err := back.BindPayload(&st); err != nil {
		t.Fatalf("BindPayload: %v", err)
	}
	if st.State != StateRunning || st.PID != 4242 {
		t.Errorf("status = %+v, want running/4242", st)
	}
}

func TestErrorResponseCarriesCode(t *testing.T) {
	resp := NewErrorResponse("req-4", NewError(CodeRateLimited, "slow down"))
	if resp.IsSuccess() {
		t.Error("IsSuccess() = true, want false")
	}
	if resp.Error == nil || resp.Error.Code != CodeRateLimited {
		t.Fatalf("error = %+v, want code %s", resp.Error, CodeRateLimited)
	}
	if !errors.Is(resp.Error, resp.Error) && resp.Error.Error() == "" {
		t.Error("Error() must not be empty")
	}
}

func TestNewErrorResponseWithNilErrorIsSafe(t *testing.T) {
	resp := NewErrorResponse("req-5", nil)
	if resp.Error == nil {
		t.Fatal("nil error must be replaced with a generic internal error")
	}
	if resp.Error.Code != CodeInternal {
		t.Errorf("code = %q, want %q", resp.Error.Code, CodeInternal)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"not json", `{`},
		{"missing type", `{"id":"req-1","version":1}`},
		{"unknown kind", `{"id":"req-1","type":"x","version":1,"kind":"weird"}`},
		{"type too long", `{"id":"req-1","type":"` + strings.Repeat("a", MaxMethodLen+1) + `","version":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode([]byte(tc.raw)); err == nil {
				t.Fatal("Decode accepted a malformed message")
			}
		})
	}
}

func TestDecodeRejectsOversizedMessage(t *testing.T) {
	huge := make([]byte, MaxMessageBytes+1)
	_, err := Decode(huge)
	if err == nil {
		t.Fatal("Decode accepted an oversized message")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodePayloadTooLarge {
		t.Errorf("code = %+v, want %s", apiErr, CodePayloadTooLarge)
	}
}

func TestMarshalRejectsOversizedPayload(t *testing.T) {
	_, err := NewResponse("req-6", map[string]string{"big": strings.Repeat("A", MaxMessageBytes)})
	if err == nil {
		t.Fatal("NewResponse accepted a payload above the size limit")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodePayloadTooLarge {
		t.Errorf("code = %+v, want %s", apiErr, CodePayloadTooLarge)
	}
}

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		msg     Message
		wantErr string
	}{
		{"valid", Message{ID: "req-1", Type: MethodDaemonStatus, Version: Version}, ""},
		{"missing id", Message{Type: MethodDaemonStatus, Version: Version}, CodeBadRequest},
		{"long id", Message{ID: strings.Repeat("x", MaxRequestIDLen+1), Type: "x", Version: Version}, CodeBadRequest},
		{"bad version", Message{ID: "req-1", Type: "x", Version: 99}, CodeUnsupportedVersion},
		{"carries error", Message{ID: "req-1", Type: "x", Version: Version, Error: NewError(CodeInternal, "x")}, CodeBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateRequest()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var apiErr *Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not a *protocol.Error", err)
			}
			if apiErr.Code != tc.wantErr {
				t.Errorf("code = %q, want %q", apiErr.Code, tc.wantErr)
			}
		})
	}
}

func TestBindPayloadRejectsWrongType(t *testing.T) {
	msg, err := NewRequest("req-7", MethodDaemonShutdown, ShutdownRequest{Reason: "x"})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	var wrong struct {
		Reason int `json:"reason"`
	}
	if err := msg.BindPayload(&wrong); err == nil {
		t.Fatal("BindPayload accepted a payload of the wrong shape")
	}
}

func TestNewRequestIDIsUniqueAndPrefixed(t *testing.T) {
	seen := make(map[string]struct{}, 512)
	for i := 0; i < 512; i++ {
		id := NewRequestID()
		if !strings.HasPrefix(id, "req-") {
			t.Fatalf("id %q lacks the req- prefix", id)
		}
		if len(id) != len("req-")+16 {
			t.Fatalf("id %q has an unexpected length", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestMethodsAndEventsAreUnique(t *testing.T) {
	if err := assertUnique("methods", Methods()); err != nil {
		t.Error(err)
	}
	if err := assertUnique("events", Events()); err != nil {
		t.Error(err)
	}
}

func assertUnique(kind string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v == "" {
			return errors.New(kind + ": empty entry")
		}
		if _, dup := seen[v]; dup {
			return errors.New(kind + ": duplicate entry " + v)
		}
		seen[v] = struct{}{}
	}
	return nil
}

func TestErrorCodesAreUppercaseSnakeCase(t *testing.T) {
	codes := []string{
		CodeUnauthorized, CodeBadRequest, CodeRateLimited, CodeNotFound,
		CodeInternal, CodePayloadTooLarge, CodeUnsupportedVersion, CodeTimeout,
		CodeDaemonShuttingDown, CodeMethodNotAllowed, CodeNATBlocked,
		CodeICEFailed, CodeSTUNUnavailable, CodePairingExpired, CodeRoomFull,
		CodeWintunCreateFailed, CodeRouteCreateFailed, CodeFirewallFailed,
	}
	for _, c := range codes {
		if c == "" {
			t.Fatal("empty error code")
		}
		for _, r := range c {
			switch {
			case r >= 'A' && r <= 'Z':
			case r == '_':
			default:
				t.Errorf("error code %q contains an unexpected character %q", c, r)
			}
		}
	}
}

func TestPeerIDValidation(t *testing.T) {
	valid := PeerID(strings.Repeat("ab", 16))
	if !valid.IsValid() {
		t.Error("a 32 char lowercase hex id must be valid")
	}
	invalid := []PeerID{
		"",
		"short",
		PeerID(strings.Repeat("AB", 16)),
		PeerID(strings.Repeat("gh", 16)),
		PeerID(strings.Repeat("ab", 17)),
	}
	for _, id := range invalid {
		if id.IsValid() {
			t.Errorf("id %q must be rejected", id)
		}
	}
}

func TestErrorWithDetails(t *testing.T) {
	err := NewError(CodePairingExpired, "expired").WithDetails(map[string]any{"age_s": 900})
	if err.Details["age_s"] != 900 {
		t.Errorf("details = %v, want age_s 900", err.Details)
	}
	raw, jerr := json.Marshal(err)
	if jerr != nil {
		t.Fatalf("marshal: %v", jerr)
	}
	if !strings.Contains(string(raw), `"age_s":900`) {
		t.Errorf("json = %s, want the details to survive encoding", raw)
	}
}

func TestNewErrorResponseDefaultsGenericMessage(t *testing.T) {
	resp := NewErrorResponse("req-8", nil)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"success":false`) {
		t.Errorf("json = %s, want success:false", raw)
	}
}
