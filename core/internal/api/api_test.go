package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

const testToken = "test-token-0123456789abcdef"

// newTestServer starts an API server on a dynamic loopback port.
func newTestServer(t *testing.T, mutate func(*Options)) (*Server, protocol.DaemonVersion) {
	t.Helper()
	return newTestServerBefore(t, mutate, nil)
}

// newTestServerBefore is newTestServer with a hook that runs after New and
// before Start. Registration is refused once the server is serving, so a test
// that installs a service has to do it in this window - the same one the daemon
// uses.
func newTestServerBefore(t *testing.T, mutate func(*Options), beforeStart func(*Server)) (*Server, protocol.DaemonVersion) {
	t.Helper()

	version := protocol.DaemonVersion{
		Daemon:          "lanbazd",
		Version:         "0.0.0-test",
		Commit:          "test",
		BuildTime:       "test",
		ProtocolVersion: protocol.Version,
		Platform:        "test/arch",
		Phase:           "0-foundation",
	}
	opts := Options{
		Token:             testToken,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		ListenHost:        "127.0.0.1",
		ListenPort:        0,
		MaxConnections:    4,
		RequestsPerSecond: 1000,
		RequestsBurst:     1000,
		GracePeriod:       time.Second,
		Version:           version,
		StatusFn: func(ctx context.Context) (protocol.DaemonStatus, error) {
			return protocol.DaemonStatus{
				State:    protocol.StateRunning,
				Version:  version.Version,
				PID:      1,
				APIPort:  1234,
				LogLevel: "info",
				StateDir: "/tmp/lanbaz",
			}, nil
		},
	}
	if mutate != nil {
		mutate(&opts)
	}

	s, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if beforeStart != nil {
		beforeStart(s)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s, version
}

func dial(t *testing.T, s *Server) *protocol.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := protocol.Dial(ctx, protocol.DialOptions{
		Addr:    s.Addr().String(),
		Token:   testToken,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestServerRejectsEmptyTokenAndLogger(t *testing.T) {
	if _, err := New(Options{Logger: slog.Default()}); err == nil {
		t.Error("New accepted an empty token")
	}
	if _, err := New(Options{Token: "x"}); err == nil {
		t.Error("New accepted a nil logger")
	}
	if _, err := New(Options{Token: "x", Logger: slog.Default(), ListenHost: "0.0.0.0"}); err == nil {
		t.Error("New accepted a non-loopback listen host")
	}
}

func TestBindAndPort(t *testing.T) {
	s, _ := newTestServer(t, nil)
	if s.Port() <= 0 {
		t.Errorf("Port() = %d, want a dynamic port", s.Port())
	}
	if !strings.HasPrefix(s.Addr().String(), "127.0.0.1:") {
		t.Errorf("Addr() = %s, want a loopback address", s.Addr().String())
	}
}

func TestHelloAndDaemonStatus(t *testing.T) {
	s, version := newTestServer(t, nil)
	c := dial(t, s)

	ctx := context.Background()
	var hello protocol.HelloResponse
	if err := c.Call(ctx, protocol.MethodHello, protocol.HelloRequest{Token: testToken}, &hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if hello.Daemon != "lanbazd" || hello.ProtocolVersion != protocol.Version {
		t.Errorf("hello = %+v, want daemon lanbazd and protocol %d", hello, protocol.Version)
	}
	if c.SessionID() == "" {
		t.Error("session id must be assigned")
	}

	var st protocol.DaemonStatus
	if err := c.Call(ctx, protocol.MethodDaemonStatus, nil, &st); err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	if st.State != protocol.StateRunning {
		t.Errorf("state = %q, want %q", st.State, protocol.StateRunning)
	}
	if st.Version != version.Version {
		t.Errorf("version = %q, want %q", st.Version, version.Version)
	}
}

func TestDaemonVersion(t *testing.T) {
	s, version := newTestServer(t, nil)
	c := dial(t, s)

	var v protocol.DaemonVersion
	if err := c.Call(context.Background(), protocol.MethodDaemonVersion, nil, &v); err != nil {
		t.Fatalf("daemon.version: %v", err)
	}
	if v.Version != version.Version || v.Platform != version.Platform {
		t.Errorf("version = %+v, want %+v", v, version)
	}
}

func TestBadTokenIsRejected(t *testing.T) {
	s, _ := newTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := protocol.Dial(ctx, protocol.DialOptions{
		Addr:    s.Addr().String(),
		Token:   "wrong-token-that-is-long-enough",
		Timeout: 3 * time.Second,
	})
	if err == nil {
		t.Fatal("Dial accepted a wrong token")
	}
	var apiErr *protocol.Error
	if !errorsAs(err, &apiErr) || apiErr.Code != protocol.CodeUnauthorized {
		t.Errorf("error = %v, want %s", err, protocol.CodeUnauthorized)
	}
}

func TestEmptyTokenIsRejectedByClient(t *testing.T) {
	if _, err := protocol.Dial(context.Background(), protocol.DialOptions{Addr: "127.0.0.1:1"}); err == nil {
		t.Error("Dial accepted an empty token")
	}
}

func TestUnknownMethodReturnsMethodNotAllowed(t *testing.T) {
	s, _ := newTestServer(t, nil)
	c := dial(t, s)

	err := c.Call(context.Background(), protocol.MethodRoomCreate, nil, nil)
	if err == nil {
		t.Fatal("an unimplemented method must not succeed in phase 0")
	}
	var apiErr *protocol.Error
	if !errorsAs(err, &apiErr) || apiErr.Code != protocol.CodeMethodNotAllowed {
		t.Errorf("error = %v, want %s", err, protocol.CodeMethodNotAllowed)
	}
}

func TestOversizedMessageIsRejected(t *testing.T) {
	s, _ := newTestServer(t, nil)
	c := dial(t, s)

	// A payload larger than the frame limit must not be accepted.
	big := strings.Repeat("A", protocol.MaxMessageBytes)
	err := c.Call(context.Background(), protocol.MethodDaemonStatus, map[string]string{"x": big}, nil)
	if err == nil {
		t.Fatal("an oversized message must be rejected")
	}
}

func TestMalformedRequestPayloadIsRejected(t *testing.T) {
	s, _ := newTestServer(t, nil)
	c := dial(t, s)

	// daemon.shutdown expects a JSON object; sending an array must fail.
	err := c.Call(context.Background(), protocol.MethodDaemonShutdown, []int{1, 2, 3}, nil)
	if err == nil {
		t.Fatal("a malformed payload must be rejected")
	}
	var apiErr *protocol.Error
	if !errorsAs(err, &apiErr) || apiErr.Code != protocol.CodeBadRequest {
		t.Errorf("error = %v, want %s", err, protocol.CodeBadRequest)
	}
}

func TestRateLimitIsEnforced(t *testing.T) {
	// A deliberately tiny bucket: 1 request per second with a burst of 2.
	s, _ := newTestServer(t, func(o *Options) {
		o.RequestsPerSecond = 1
		o.RequestsBurst = 2
	})
	c := dial(t, s)

	var limited bool
	for i := 0; i < 20; i++ {
		err := c.Call(context.Background(), protocol.MethodDaemonVersion, nil, nil)
		if err == nil {
			continue
		}
		var apiErr *protocol.Error
		if errorsAs(err, &apiErr) && apiErr.Code == protocol.CodeRateLimited {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("the per-connection rate limit never triggered")
	}
}

func TestMaxConnectionsIsEnforced(t *testing.T) {
	s, _ := newTestServer(t, func(o *Options) { o.MaxConnections = 2 })

	var clients []*protocol.Client
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, err := protocol.Dial(ctx, protocol.DialOptions{
			Addr: s.Addr().String(), Token: testToken, Timeout: 2 * time.Second,
		})
		cancel()
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	t.Cleanup(func() {
		for _, c := range clients {
			_ = c.Close()
		}
	})

	// Give the server a moment to register both connections.
	deadline := time.Now().Add(2 * time.Second)
	for s.ConnectionCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.ConnectionCount(); got != 2 {
		t.Fatalf("connection count = %d, want 2", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := protocol.Dial(ctx, protocol.DialOptions{
		Addr: s.Addr().String(), Token: testToken, Timeout: 2 * time.Second,
	}); err == nil {
		t.Fatal("a third connection must be refused")
	}
}

func TestEventsArePushedToClients(t *testing.T) {
	s, _ := newTestServer(t, nil)
	c := dial(t, s)

	received := make(chan protocol.StateEvent, 1)
	var once sync.Once
	c.On(protocol.EventDaemonState, func(m protocol.Message) {
		var ev protocol.StateEvent
		if err := m.BindPayload(&ev); err != nil {
			return
		}
		once.Do(func() { received <- ev })
	})

	// The subscription is registered before the publish; give the daemon a
	// moment to attach the subscriber.
	waitFor(t, 2*time.Second, func() bool { return s.ConnectionCount() > 0 })

	s.PublishEvent(protocol.EventDaemonState, protocol.StateEvent{
		State: protocol.StateRunning, Previous: protocol.StateStarting, Timestamp: time.Now().UTC(),
	})

	select {
	case ev := <-received:
		if ev.State != protocol.StateRunning {
			t.Errorf("state = %q, want %q", ev.State, protocol.StateRunning)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no daemon.state event was delivered")
	}
}

func TestDaemonShutdownTriggersCallback(t *testing.T) {
	stopped := make(chan time.Duration, 1)
	s, _ := newTestServer(t, func(o *Options) {
		o.OnShutdown = func(grace time.Duration) { stopped <- grace }
	})
	c := dial(t, s)

	var resp protocol.ShutdownResponse
	if err := c.Call(context.Background(), protocol.MethodDaemonShutdown,
		protocol.ShutdownRequest{GracePeriod: 900}, &resp); err != nil {
		t.Fatalf("daemon.shutdown: %v", err)
	}
	if !resp.Accepted || resp.GracePeriod != 900 {
		t.Errorf("shutdown response = %+v, want accepted with 900ms", resp)
	}

	select {
	case grace := <-stopped:
		if grace != 900*time.Millisecond {
			t.Errorf("grace = %v, want 900ms", grace)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the shutdown callback was never invoked")
	}

	// Once shutting down, a second request must be refused.
	if err := c.Call(context.Background(), protocol.MethodDaemonShutdown, nil, nil); err == nil {
		t.Error("a second shutdown request must be refused")
	}
}

func TestShutdownGracePeriodIsCapped(t *testing.T) {
	stopped := make(chan time.Duration, 1)
	s, _ := newTestServer(t, func(o *Options) {
		o.OnShutdown = func(grace time.Duration) { stopped <- grace }
	})
	c := dial(t, s)

	var resp protocol.ShutdownResponse
	// Ask for five minutes; the daemon must clamp it.
	if err := c.Call(context.Background(), protocol.MethodDaemonShutdown,
		protocol.ShutdownRequest{GracePeriod: 300_000}, &resp); err != nil {
		t.Fatalf("daemon.shutdown: %v", err)
	}
	if resp.GracePeriod != 30_000 {
		t.Errorf("grace period = %d, want the 30s cap", resp.GracePeriod)
	}
}

func TestHealthEndpointHasNoSecrets(t *testing.T) {
	s, _ := newTestServer(t, nil)
	// Reuse the client transport helpers: an HTTP GET over the same listener.
	body := httpGet(t, "http://"+s.Addr().String()+protocol.HealthPath)
	for _, secret := range []string{testToken, "api_token", "pid"} {
		if strings.Contains(body, secret) {
			t.Errorf("health response leaks %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("health response = %s, want status ok", body)
	}
}

func TestRegistryRejectsDuplicatesAndNil(t *testing.T) {
	// Registration is only allowed before Start, so this server is not started.
	s, err := New(Options{Token: testToken, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Register(protocol.MethodDaemonStatus, func(context.Context, json.RawMessage) (any, error) {
		return nil, nil
	}); err == nil {
		t.Error("registering a duplicate method must fail")
	}
	if err := s.Register("brand.new", nil); err == nil {
		t.Error("registering a nil handler must fail")
	}
	if err := s.Register("", func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Error("registering an empty method name must fail")
	}
	if err := s.Register("brand.new", func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err != nil {
		t.Errorf("Register: %v", err)
	}
	if !contains(s.Methods(), "brand.new") {
		t.Errorf("methods = %v, want the new method to be listed", s.Methods())
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	if err := s.Register("after.start", func(context.Context, json.RawMessage) (any, error) { return nil, nil }); err == nil {
		t.Error("registering after Start must fail")
	}
}

func TestPhase0RegistersExactlyTheExpectedMethods(t *testing.T) {
	s, _ := newTestServer(t, nil)
	want := []string{
		protocol.MethodHello,
		protocol.MethodDaemonStatus,
		protocol.MethodDaemonVersion,
		protocol.MethodDaemonShutdown,
	}
	got := s.Methods()
	if len(got) != len(want) {
		t.Fatalf("methods = %v, want exactly %v", got, want)
	}
	for _, m := range want {
		if !contains(got, m) {
			t.Errorf("method %s is missing from %v", m, got)
		}
	}
}

func TestAsAPIErrorMapsSentinels(t *testing.T) {
	if got := asAPIError(context.DeadlineExceeded).Code; got != protocol.CodeTimeout {
		t.Errorf("deadline code = %q, want %q", got, protocol.CodeTimeout)
	}
	if got := asAPIError(context.Canceled).Code; got != protocol.CodeTimeout {
		t.Errorf("canceled code = %q, want %q", got, protocol.CodeTimeout)
	}
	apiErr := protocol.NewError(protocol.CodeRoomFull, "full")
	if got := asAPIError(apiErr); got.Code != protocol.CodeRoomFull {
		t.Errorf("passthrough code = %q, want %q", got.Code, protocol.CodeRoomFull)
	}
	if got := asAPIError(io.ErrUnexpectedEOF).Code; got != protocol.CodeInternal {
		t.Errorf("plain error code = %q, want %q", got, protocol.CodeInternal)
	}
	if got := asAPIError(nil).Code; got != protocol.CodeInternal {
		t.Errorf("nil code = %q, want %q", got, protocol.CodeInternal)
	}
}

// ---------------------------------------------------------------- helpers ---

func errorsAs(err error, target **protocol.Error) bool {
	if e, ok := err.(*protocol.Error); ok {
		*target = e
		return true
	}
	return false
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
