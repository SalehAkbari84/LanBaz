package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/config"
	"github.com/lanbaz/lanbaz/core/internal/security"
	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

const testToken = "daemon-test-token-0123456789"

func newTestDaemon(t *testing.T) (*Daemon, *config.Config) {
	t.Helper()
	return newTestDaemonBus(t, &social.MemBus{})
}

func newTestDaemonBus(t *testing.T, bus social.Bus) (*Daemon, *config.Config) {
	t.Helper()

	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	cfg.LogLevel = config.LogLevelError
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}

	d, err := New(Options{
		Config:    cfg,
		Token:     testToken,
		Logger:    testLogger(),
		BuildInfo: BuildInfo{Version: "1.2.3", Commit: "abc123", BuildTime: "2026-01-01"},
		SocialBus: bus,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, &cfg
}

func TestNewGeneratesTokenWhenEmpty(t *testing.T) {
	cfg := config.Default()
	cfg.StateDir = t.TempDir()

	d, err := New(Options{
		Config: cfg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.Token() == "" {
		t.Fatal("a token must be generated when none is supplied")
	}
	if len(d.Token()) < 40 {
		t.Errorf("generated token %q is too short", d.Token())
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	cfg := config.Default()
	cfg.ListenHost = "0.0.0.0"
	if _, err := New(Options{Config: cfg, Logger: slog.Default()}); err == nil {
		t.Fatal("New accepted a non-loopback control api")
	}
}

// startDaemon runs a daemon in the background and returns a connected client.
// The daemon is always stopped on cleanup, so a test can never leak a running
// listener into the next test.
func startDaemon(t *testing.T, d *Daemon, cfg *config.Config) *protocol.Client {
	t.Helper()

	// A cancellable context lets the test stop the daemon deterministically
	// instead of relying on Run noticing an external signal.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := d.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	// Wait for the state file, which is written once the API is listening.
	statePath := filepath.Join(cfg.StateDir, security.StateFileName)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(statePath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, err := security.ReadStateFile(cfg.StateDir)
	if err != nil {
		cancel()
		t.Fatalf("state file after start: %v", err)
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	client, err := protocol.Dial(dialCtx, protocol.DialOptions{
		Addr:    state.APIListen,
		Token:   state.APIToken,
		Timeout: 3 * time.Second,
	})
	dialCancel()
	if err != nil {
		cancel()
		wg.Wait()
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		waitForDone(t, d, &wg)
	})
	return client
}

// waitForDone stops the daemon if it is still running and blocks until Run has
// returned, bounded so a bug cannot hang the test binary forever.
func waitForDone(t *testing.T, d *Daemon, wg *sync.WaitGroup) {
	t.Helper()
	select {
	case <-d.Done():
	case <-time.After(3 * time.Second):
		d.Shutdown(time.Second)
	}
	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Error("the daemon did not return from Run after shutdown")
	}
}

func TestDaemonLifecycleAndStatus(t *testing.T) {
	d, cfg := newTestDaemon(t)
	client := startDaemon(t, d, cfg)

	var st protocol.DaemonStatus
	if err := client.Call(context.Background(), protocol.MethodDaemonStatus, nil, &st); err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	if st.State != protocol.StateRunning {
		t.Errorf("state = %q, want %q", st.State, protocol.StateRunning)
	}
	if st.Version != "1.2.3" || st.Commit != "abc123" {
		t.Errorf("build info = %s/%s, want 1.2.3/abc123", st.Version, st.Commit)
	}
	if st.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", st.PID, os.Getpid())
	}
	if st.APIPort <= 0 {
		t.Errorf("api port = %d, want a real port", st.APIPort)
	}
	if st.StateDir != cfg.StateDir {
		t.Errorf("state dir = %q, want %q", st.StateDir, cfg.StateDir)
	}
	if st.UptimeSeconds <= 0 {
		t.Errorf("uptime = %v, want a positive duration", st.UptimeSeconds)
	}
	if st.ActiveClients < 1 {
		t.Errorf("active clients = %d, want at least the connected client", st.ActiveClients)
	}
}

func TestDaemonVersionMethod(t *testing.T) {
	d, cfg := newTestDaemon(t)
	client := startDaemon(t, d, cfg)

	var v protocol.DaemonVersion
	if err := client.Call(context.Background(), protocol.MethodDaemonVersion, nil, &v); err != nil {
		t.Fatalf("daemon.version: %v", err)
	}
	if v.Daemon != "lanbazd" {
		t.Errorf("daemon = %q, want lanbazd", v.Daemon)
	}
	if v.ProtocolVersion != protocol.Version {
		t.Errorf("protocol = %d, want %d", v.ProtocolVersion, protocol.Version)
	}
	if !strings.Contains(v.Platform, "/") {
		t.Errorf("platform = %q, want os/arch", v.Platform)
	}
}

func TestDaemonShutdownStopsAndCleansUp(t *testing.T) {
	d, cfg := newTestDaemon(t)
	client := startDaemon(t, d, cfg)

	var resp protocol.ShutdownResponse
	if err := client.Call(context.Background(), protocol.MethodDaemonShutdown,
		protocol.ShutdownRequest{Reason: "test"}, &resp); err != nil {
		t.Fatalf("daemon.shutdown: %v", err)
	}
	if !resp.Accepted {
		t.Fatal("shutdown was not accepted")
	}

	select {
	case <-d.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("the daemon did not stop after daemon.shutdown")
	}

	// The state file must be gone so the next start does not read a stale port.
	if _, err := os.Stat(filepath.Join(cfg.StateDir, security.StateFileName)); !os.IsNotExist(err) {
		t.Errorf("state file still exists after shutdown: %v", err)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	d, cfg := newTestDaemon(t)
	startDaemon(t, d, cfg)

	// Repeated shutdown requests must be a no-op, not a panic or a double
	// close of the shutdown channel.
	d.Shutdown(time.Second)
	d.Shutdown(time.Second)
	d.Shutdown(time.Second)

	select {
	case <-d.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("the daemon did not stop")
	}
}

func TestContextCancellationStopsDaemon(t *testing.T) {
	d, cfg := newTestDaemon(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(cfg.StateDir, security.StateFileName)); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil after a clean cancellation", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cancelling the context did not stop the daemon")
	}
}

func TestStateFileNeverContainsTheTokenInLogs(t *testing.T) {
	d, cfg := newTestDaemon(t)
	startDaemon(t, d, cfg)

	// The daemon log must not contain the token. This guards the redactor
	// wiring: a token leak into a log file is a local privilege escalation.
	raw, err := os.ReadFile(cfg.LogFile())
	if os.IsNotExist(err) {
		return // no log sink configured in this test; nothing to assert
	}
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if bytesContain(raw, d.Token()) {
		t.Error("the api token leaked into daemon.log")
	}
}

func TestStatusAfterShutdownIsNotServed(t *testing.T) {
	d, cfg := newTestDaemon(t)
	client := startDaemon(t, d, cfg)

	_ = client.Call(context.Background(), protocol.MethodDaemonShutdown, nil, nil)
	<-d.Done()

	// The socket is closed, so a fresh call must fail rather than hang.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Call(ctx, protocol.MethodDaemonStatus, nil, nil); err == nil {
		t.Error("a request after shutdown must fail")
	}
}

// The counts in daemon.status must come from the live room manager, not from a
// cache the daemon maintains separately. A fresh daemon has no rooms, and that is
// what it must report.
func TestStatusCountsComeFromTheRoomManager(t *testing.T) {
	d, cfg := newTestDaemon(t)
	client := startDaemon(t, d, cfg)

	var st protocol.DaemonStatus
	if err := client.Call(context.Background(), protocol.MethodDaemonStatus, nil, &st); err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	if st.RoomCount != 0 || st.PeerCount != 0 {
		t.Errorf("a fresh daemon reports %d rooms / %d peers, want 0/0", st.RoomCount, st.PeerCount)
	}
	if d.Rooms() == nil {
		t.Error("the room manager was not wired in")
	}
}

// The room and peer methods must be registered, not merely implemented. A
// handler that exists but was never registered is a method-not-allowed at the
// exact moment a user clicks the button that needs it.
func TestRoomMethodsAreRegistered(t *testing.T) {
	d, _ := newTestDaemon(t)
	registered := map[string]bool{}
	for _, m := range d.API().Methods() {
		registered[m] = true
	}
	want := []string{
		protocol.MethodRoomCreate, protocol.MethodRoomJoin, protocol.MethodRoomAccept,
		protocol.MethodRoomLeave, protocol.MethodRoomGet, protocol.MethodRoomList,
		protocol.MethodRoomRegeneratePairing,
		protocol.MethodPeerList, protocol.MethodPeerGet,
		protocol.MethodPeerKick, protocol.MethodPeerPing,
	}
	for _, m := range want {
		if !registered[m] {
			t.Errorf("%s is not registered", m)
		}
	}
}

// room.create must work end to end over the control API: it is the one call that
// starts everything else, so a failure here means the UI has no way in.
func TestRoomCreateOverTheControlAPI(t *testing.T) {
	d, cfg := newTestDaemon(t)
	client := startDaemon(t, d, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var created protocol.RoomCreateResponse
	if err := client.Call(ctx, protocol.MethodRoomCreate, protocol.RoomCreateRequest{
		Name:              "API Test",
		MaxPeers:          4,
		PairingTTLSeconds: 120,
	}, &created); err != nil {
		t.Fatalf("room.create: %v", err)
	}
	if created.PairingCode == "" {
		t.Fatal("room.create returned no pairing code")
	}
	if !created.Room.IsHost {
		t.Error("the created room is not marked as hosted")
	}
	if created.Room.RoomID == "" {
		t.Error("the created room has no id")
	}

	var list []protocol.RoomSummary
	if err := client.Call(ctx, protocol.MethodRoomList, nil, &list); err != nil {
		t.Fatalf("room.list: %v", err)
	}
	if len(list) != 1 || list[0].RoomID != created.Room.RoomID {
		t.Fatalf("room.list returned %d rooms, want the one just created", len(list))
	}

	var peers []protocol.PeerSummary
	if err := client.Call(ctx, protocol.MethodPeerList,
		struct {
			RoomID string `json:"room_id"`
		}{RoomID: created.Room.RoomID}, &peers); err != nil {
		t.Fatalf("peer.list: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("a fresh room has %d peers, want 0", len(peers))
	}
}

// A room.join with a nonsense code must be refused with a machine-readable
// pairing error, not a generic failure. The UI branches on the code to say
// "this code has expired" instead of "something went wrong".
func TestRoomJoinRejectsAnInvalidCode(t *testing.T) {
	d, cfg := newTestDaemon(t)
	client := startDaemon(t, d, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := client.Call(ctx, protocol.MethodRoomJoin, protocol.RoomJoinRequest{
		PairingCode: "LBZ-0000-0000-0000",
	}, nil)
	if err == nil {
		t.Fatal("room.join accepted a bogus pairing code")
	}
	if !strings.Contains(err.Error(), protocol.CodePairingInvalid) {
		t.Errorf("room.join error = %v, want a PAIRING_INVALID code", err)
	}
}

func TestStatusPayloadIsJSONSerialisable(t *testing.T) {
	d, _ := newTestDaemon(t)
	st, err := d.status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("status is not serialisable: %v", err)
	}
	if !strings.Contains(string(raw), `"state":"starting"`) {
		t.Errorf("status json = %s, want the starting state before Run", raw)
	}
}

func bytesContain(haystack []byte, needle string) bool {
	return len(needle) > 0 && strings.Contains(string(haystack), needle)
}

// testLogger discards the daemon log unless LANBAZ_TEST_LOG is set.
func testLogger() *slog.Logger {
	if os.Getenv("LANBAZ_TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
