package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/lanbaz/lanbaz/core/internal/security"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Keepalive tuning; variables only so tests can shorten them.
var (
	// pongWait must be exceeded before a connection is considered dead.
	pongWait = 60 * time.Second
	// pingPeriod keeps the link alive. Each pong (browsers answer pings
	// automatically) and each message moves the read deadline forward; three
	// pings fit in one pongWait, so one lost pong does not drop the UI.
	pingPeriod = 20 * time.Second
)

// Handshake and connection tuning.
const (
	// handshakeTimeout bounds how long a fresh connection may stay
	// unauthenticated before it is closed. A local client that connects and
	// says nothing must not be able to hold a slot.
	handshakeTimeout = 10 * time.Second
	// writeWait bounds a single write.
	writeWait = 10 * time.Second
	// readLimit mirrors protocol.MaxMessageBytes and is applied at the frame
	// level so an oversized frame never reaches the JSON decoder.
	readLimit = int64(protocol.MaxMessageBytes)
	// sendBuffer is the per-connection outbound queue depth.
	sendBuffer = 64
)

// Server is the local control API.
type Server struct {
	token   string
	log     *slog.Logger
	reg     *registry
	version protocol.DaemonVersion

	// maxConns bounds concurrent control connections.
	maxConns int
	// perSecond/burst configure the per-connection request rate limit.
	perSecond float64
	burst     int
	// host/port are the configured bind target; port 0 asks the OS to choose.
	host string
	port int
	// gracePeriod is the default graceful shutdown budget.
	gracePeriod time.Duration
	// onShutdown is invoked after daemon.shutdown is acknowledged.
	onShutdown func(gracePeriod time.Duration)

	// statusFn supplies the daemon.status payload. It is injected by the daemon
	// so the API package stays free of daemon internals.
	statusFn func(context.Context) (protocol.DaemonStatus, error)

	// roomSvc serves the room.* and peer.* methods. It is nil in a build with no
	// room service, and those methods then report UNSUPPORTED_VERSION rather than
	// failing obscurely.
	roomSvc RoomService
	// netSvc is the virtual-LAN service. It is optional: a build with no adapter
	// backend still serves every other method and answers these three with a
	// clear UNSUPPORTED_VERSION rather than failing to start.
	netSvc NetworkService

	mu       sync.Mutex
	conns    map[*conn]struct{}
	connSeq  atomic.Uint64
	listener net.Listener
	http     *http.Server
	serving  bool
	// shuttingDown makes the API reject new requests and new connections.
	shuttingDown atomic.Bool

	subMu sync.Mutex
	subs  map[*subscriber]struct{}
}

// Options configures a Server.
type Options struct {
	// Token is the shared secret clients must present in `hello`.
	Token string
	// Logger receives structured daemon logs. Required.
	Logger *slog.Logger
	// ListenHost and ListenPort select the loopback bind address. Port 0 asks
	// the OS for a free port, which is the default.
	ListenHost string
	ListenPort int
	// MaxConnections bounds concurrent control connections.
	MaxConnections int
	// RequestsPerSecond and RequestsBurst configure the per-connection limit.
	RequestsPerSecond float64
	RequestsBurst     int
	// GracePeriod is the default graceful shutdown budget.
	GracePeriod time.Duration
	// Version is reported by daemon.version.
	Version protocol.DaemonVersion
	// StatusFn supplies daemon.status. Optional in tests.
	StatusFn func(context.Context) (protocol.DaemonStatus, error)
	// OnShutdown is called after daemon.shutdown is acknowledged so the daemon
	// can stop itself. Optional in tests.
	OnShutdown func(gracePeriod time.Duration)
}

// New creates a Server. It does not listen until Start is called.
func New(opts Options) (*Server, error) {
	if opts.Token == "" {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "api token must not be empty")
	}
	if opts.Logger == nil {
		return nil, protocol.NewError(protocol.CodeConfigInvalid, "api logger must not be empty")
	}
	if opts.MaxConnections <= 0 {
		opts.MaxConnections = 8
	}
	if opts.GracePeriod <= 0 {
		opts.GracePeriod = 5 * time.Second
	}

	host := opts.ListenHost
	if host == "" {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, protocol.NewErrorf(protocol.CodeConfigInvalid,
			"listen host %q is not a loopback address; the control api is loopback only", host)
	}

	s := &Server{
		token:       opts.Token,
		log:         opts.Logger,
		reg:         newRegistry(),
		version:     opts.Version,
		maxConns:    opts.MaxConnections,
		perSecond:   opts.RequestsPerSecond,
		burst:       opts.RequestsBurst,
		host:        host,
		port:        opts.ListenPort,
		gracePeriod: opts.GracePeriod,
		onShutdown:  opts.OnShutdown,
		statusFn:    opts.StatusFn,
		conns:       make(map[*conn]struct{}),
		subs:        make(map[*subscriber]struct{}),
	}

	if err := s.registerBuiltins(); err != nil {
		return nil, err
	}
	return s, nil
}

// Register adds a method handler. It must be called before Start.
func (s *Server) Register(method string, h Handler) error {
	s.mu.Lock()
	serving := s.serving
	s.mu.Unlock()
	if serving {
		return protocol.NewErrorf(protocol.CodeInternal, "cannot register %s while the api is serving", method)
	}
	return s.reg.register(method, h)
}

func (s *Server) registerBuiltins() error {
	if err := s.reg.register(protocol.MethodHello, s.handleHello); err != nil {
		return err
	}
	if err := s.reg.register(protocol.MethodDaemonStatus, s.handleStatus); err != nil {
		return err
	}
	if err := s.reg.register(protocol.MethodDaemonVersion, s.handleVersion); err != nil {
		return err
	}
	return s.reg.register(protocol.MethodDaemonShutdown, s.handleShutdown)
}

// Start binds the listener and begins serving. It returns once the socket is
// accepting connections.
func (s *Server) Start(ctx context.Context) error {
	host, port := s.host, s.port

	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	if err != nil {
		return protocol.NewErrorf(protocol.CodeInternal, "listen on %s:%d: %v", host, port, err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close()
		return protocol.NewError(protocol.CodeInternal, "listener is not TCP")
	}
	if !tcp.IP.IsLoopback() {
		ln.Close()
		return protocol.NewError(protocol.CodeConfigInvalid, "refusing to serve a non-loopback address")
	}

	mux := http.NewServeMux()
	mux.HandleFunc(protocol.APIPath, s.handleWebSocket)
	mux.HandleFunc(protocol.HealthPath, s.handleHealth)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// No write timeout: a control connection is long lived and per-write
		// deadlines are enforced by the write pump instead.
		ErrorLog: nil,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	s.mu.Lock()
	s.listener = ln
	s.http = srv
	s.serving = true
	s.mu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("api serve failed", "error", err)
		}
	}()

	s.log.Info("API listening", "addr", ln.Addr().String(), "path", protocol.APIPath, "version", s.version.Version)
	return nil
}

// Addr returns the bound loopback address.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Port returns the dynamically chosen API port, or 0 before Start.
func (s *Server) Port() int {
	addr := s.Addr()
	if addr == nil {
		return 0
	}
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

// ConnectionCount reports the number of live control connections.
func (s *Server) ConnectionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Methods lists the registered methods, for diagnostics and tests.
func (s *Server) Methods() []string { return s.reg.methods() }

// Counts returns the room and peer totals for the status payload. It reports
// zeroes when no room service is installed.
func (s *Server) Counts() (rooms, peers int) {
	if s.roomSvc == nil {
		return 0, 0
	}
	return s.roomSvc.Counts()
}

// Shutdown stops accepting connections, waits for in-flight work up to the grace
// period and then closes remaining sockets. It is safe to call after
// daemon.shutdown already claimed the flag.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shuttingDown.Store(true)

	s.mu.Lock()
	srv := s.http
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	// http.Server.Shutdown only waits for in-flight requests; an upgraded
	// WebSocket is hijacked and therefore no longer tracked by the http server.
	// Control sockets must be closed explicitly, otherwise a client keeps
	// issuing daemon.status calls to a daemon that has already stopped.
	defer func() {
		for _, c := range conns {
			c.close()
		}
	}()

	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil {
			// The grace period expired; the deferred close force-closes the rest.
			return err
		}
	}
	s.log.Info("API stopped", "connections_closed", len(conns))
	return nil
}

// ---------------------------------------------------------------- health ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Deliberately minimal: no pid, no rooms, no peers, no token.
	fmt.Fprintf(w, `{"status":"ok","daemon":%q,"protocol_version":%d}`+"\n",
		s.version.Daemon, protocol.Version)
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---------------------------------------------------------------- events ----

// PublishEvent sends an event to every authenticated subscriber. A slow client
// never blocks the daemon: its queue is full, the event is dropped and the drop
// is counted in the log at debug level.
func (s *Server) PublishEvent(name string, payload any) {
	msg, err := protocol.NewEvent(name, payload)
	if err != nil {
		s.log.Error("cannot encode event", "event", name, "error", err)
		return
	}
	raw, err := msg.Marshal()
	if err != nil {
		s.log.Error("cannot marshal event", "event", name, "error", err)
		return
	}

	s.subMu.Lock()
	defer s.subMu.Unlock()
	for sub := range s.subs {
		if !sub.enqueue(writeItem{raw: raw}) {
			s.log.Debug("event dropped for slow subscriber", "event", name, "session", sub.id)
		}
	}
}

type subscriber struct {
	id     string
	ch     chan writeItem
	closed atomic.Bool
}

// writeItem is one item on the per-connection write queue.
//
// close is a sentinel that tells the writer to send a close frame and stop, so
// a final message (for example "UNAUTHORIZED") reaches the client before the
// socket disappears.
type writeItem struct {
	raw   []byte
	close bool
}

func (s *subscriber) enqueue(item writeItem) bool {
	if s.closed.Load() {
		return false
	}
	select {
	case s.ch <- item:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------- session ----

var upgrader = websocket.Upgrader{
	HandshakeTimeout: handshakeTimeout,
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
	// The control API is loopback only, so any origin is acceptable here; the
	// token is the actual gate. Restricting Origin would break the Tauri
	// webview on some platforms.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.shuttingDown.Load() {
		http.Error(w, "daemon is shutting down", http.StatusServiceUnavailable)
		return
	}

	s.mu.Lock()
	if len(s.conns) >= s.maxConns {
		s.mu.Unlock()
		s.log.Warn("control connection refused", "reason", "max_connections", "limit", s.maxConns)
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	s.mu.Unlock()

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		s.log.Debug("websocket upgrade failed", "error", err)
		return
	}

	c := newConn(s, ws)
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	s.log.Info("control client connected", "session", c.id, "remote", r.RemoteAddr, "clients", s.ConnectionCount())
	c.start(r.Context())
	c.run(r.Context())
}

// conn is one authenticated or pending control session.
type conn struct {
	id  string
	srv *Server
	ws  *websocket.Conn
	sub *subscriber

	limiter  *rateLimiter
	authed   atomic.Bool
	closeOne sync.Once

	// stop asks the writer goroutine to return; pumpDone is closed by that
	// goroutine once it has actually stopped. Keeping the two directions
	// separate is what stops close() from deadlocking on its own pump.
	stop     chan struct{}
	pumpDone chan struct{}
	// slow bounds how many long-running requests (room.create, room.accept,
	// peer.ping, ...) one client may have in flight. They run off the read
	// loop so a 30 s accept does not freeze every other call on the socket.
	slow chan struct{}
}

// slowMethods take seconds: ICE gathering, waiting for a data channel, a
// burst of pings. They are dispatched concurrently; everything else stays on
// the read loop, which keeps cheap calls strictly ordered.
var slowMethods = map[string]bool{
	protocol.MethodRoomCreate:            true,
	protocol.MethodRoomJoin:              true,
	protocol.MethodRoomAccept:            true,
	protocol.MethodRoomRegeneratePairing: true,
	protocol.MethodRoomLeave:             true,
	protocol.MethodRoomClose:             true,
	protocol.MethodPeerPing:              true,
	protocol.MethodPeerKick:              true,
	protocol.MethodNetworkDiagnose:       true,
	protocol.MethodFriendsAdd:            true,
	protocol.MethodFriendsRespond:        true,
	protocol.MethodFriendsRemove:         true,
	protocol.MethodJoinRequest:           true,
	protocol.MethodJoinInvite:            true,
	protocol.MethodJoinRespond:           true,
}

func newConn(s *Server, ws *websocket.Conn) *conn {
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := "sess-" + hex.EncodeToString(b[:])
	return &conn{
		id:       id,
		srv:      s,
		ws:       ws,
		sub:      &subscriber{id: id, ch: make(chan writeItem, sendBuffer)},
		limiter:  newRateLimiter(s.perSecond, s.burst),
		stop:     make(chan struct{}),
		pumpDone: make(chan struct{}),
		slow:     make(chan struct{}, 4),
	}
}

// start launches the single writer goroutine for this connection. Every write to
// the socket goes through it: gorilla/websocket forbids concurrent writers, and
// a single pump also keeps the per-write deadline logic in one place.
func (c *conn) start(ctx context.Context) {
	go c.writePump(ctx)
}

func (c *conn) writePump(ctx context.Context) {
	defer close(c.pumpDone)

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case item := <-c.sub.ch:
			if item.close {
				_ = c.ws.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(writeWait))
				return
			}
			if err := c.writeRaw(item.raw); err != nil {
				c.srv.log.Debug("control write failed", "session", c.id, "error", err)
				return
			}
		case <-ticker.C:
			if err := c.ws.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.srv.log.Debug("control ping failed", "session", c.id, "error", err)
				return
			}
		}
	}
}

func (c *conn) writeRaw(raw []byte) error {
	if err := c.ws.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return c.ws.WriteMessage(websocket.TextMessage, raw)
}

// enqueueWrite hands bytes to the writer goroutine.
//
// Every byte that reaches the socket goes through the single writer: gorilla
// permits only one concurrent writer, and writing a response directly from the
// read loop races the writer's keepalive ping ("concurrent write to websocket
// connection"). The queue applies backpressure to the read loop, and the select
// keeps it from blocking forever when the writer has already stopped.
func (c *conn) enqueueWrite(raw []byte) error {
	return c.enqueue(writeItem{raw: raw})
}

func (c *conn) enqueue(item writeItem) error {
	select {
	case c.sub.ch <- item:
		return nil
	case <-c.pumpDone:
		return protocol.NewError(protocol.CodeTimeout, "the connection writer stopped")
	case <-c.stop:
		return protocol.NewError(protocol.CodeTimeout, "the connection is closing")
	}
}

// closeAfterFlush asks the writer to send a close frame once everything already
// queued has been written. The handshake rejection path uses it so the client
// receives the machine-readable reason instead of a bare close.
func (c *conn) closeAfterFlush() {
	if err := c.enqueue(writeItem{close: true}); err != nil {
		return
	}
	select {
	case <-c.pumpDone:
	case <-time.After(writeWait):
	}
}

func (c *conn) run(ctx context.Context) {
	defer c.close()

	c.ws.SetReadLimit(readLimit)
	if err := c.ws.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return
	}

	for {
		mt, raw, err := c.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.srv.log.Debug("control read failed", "session", c.id, "error", err)
			}
			return
		}
		if mt != websocket.TextMessage {
			c.writeError("", protocol.NewError(protocol.CodeBadRequest, "only text messages are accepted"))
			continue
		}

		msg, err := protocol.Decode(raw)
		if err != nil {
			c.writeError("", err)
			continue
		}

		if !c.authed.Load() {
			if err := c.handleHandshake(ctx, msg); err != nil {
				c.writeError(msg.ID, err)
				// Flush the rejection before the socket goes away.
				c.closeAfterFlush()
				return
			}
			if err := c.ws.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
				return
			}
			// Without this the deadline set above was never moved again,
			// and every UI session dropped exactly pongWait after connecting.
			c.ws.SetPongHandler(func(string) error {
				return c.ws.SetReadDeadline(time.Now().Add(pongWait))
			})
			continue
		}
		if err := c.ws.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			return
		}

		if !c.limiter.allow() {
			c.writeError(msg.ID, protocol.NewError(protocol.CodeRateLimited,
				"too many requests; slow down"))
			continue
		}
		if slowMethods[msg.Type] {
			select {
			case c.slow <- struct{}{}:
				go func(msg protocol.Message) {
					defer func() { <-c.slow }()
					c.dispatch(ctx, msg)
				}(msg)
			default:
				c.writeError(msg.ID, protocol.NewError(protocol.CodeRateLimited,
					"too many long-running requests in flight; wait for one to finish"))
			}
			continue
		}
		c.dispatch(ctx, msg)
	}
}

func (c *conn) handleHandshake(ctx context.Context, msg protocol.Message) error {
	if msg.Type != protocol.MethodHello {
		return protocol.NewErrorf(protocol.CodeUnauthorized, "first message must be %s", protocol.MethodHello)
	}
	if err := msg.ValidateRequest(); err != nil {
		return err
	}
	var req protocol.HelloRequest
	if err := msg.BindPayload(&req); err != nil {
		return err
	}
	if !security.ConstantTimeEqual(req.Token, c.srv.token) {
		c.srv.log.Warn("control handshake rejected", "session", c.id, "reason", "bad_token")
		return protocol.NewError(protocol.CodeUnauthorized, "invalid api token")
	}

	c.authed.Store(true)
	c.sub.id = c.id
	c.srv.subMu.Lock()
	c.srv.subs[c.sub] = struct{}{}
	c.srv.subMu.Unlock()

	resp, err := protocol.NewResponse(msg.ID, protocol.HelloResponse{
		Daemon:          c.srv.version.Daemon,
		Version:         c.srv.version.Version,
		ProtocolVersion: protocol.Version,
		SessionID:       c.id,
		ServerTime:      time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	return c.writeMessage(resp)
}

func (c *conn) dispatch(ctx context.Context, msg protocol.Message) {
	if err := msg.ValidateRequest(); err != nil {
		c.writeError(msg.ID, err)
		return
	}
	if c.srv.shuttingDown.Load() && msg.Type != protocol.MethodDaemonStatus {
		c.writeError(msg.ID, protocol.NewError(protocol.CodeDaemonShuttingDown,
			"daemon is shutting down"))
		return
	}

	handler, ok := c.srv.reg.lookup(msg.Type)
	if !ok {
		c.writeError(msg.ID, protocol.NewErrorf(protocol.CodeMethodNotAllowed,
			"method %q is not implemented in this build", msg.Type))
		return
	}

	payload, err := handler(ctx, msg.Payload)
	if err != nil {
		c.writeError(msg.ID, err)
		return
	}
	resp, err := protocol.NewResponse(msg.ID, payload)
	if err != nil {
		c.writeError(msg.ID, err)
		return
	}
	if err := c.writeMessage(resp); err != nil {
		c.srv.log.Debug("control write failed", "session", c.id, "error", err)
	}
}

func (c *conn) writeError(id string, err error) {
	apiErr := asAPIError(err)
	resp := protocol.NewErrorResponse(id, apiErr)
	if writeErr := c.writeMessage(resp); writeErr != nil {
		c.srv.log.Debug("control error write failed", "session", c.id, "error", writeErr)
	}
}

func (c *conn) writeMessage(msg protocol.Message) error {
	raw, err := msg.Marshal()
	if err != nil {
		return err
	}
	return c.enqueueWrite(raw)
}

func (c *conn) close() {
	c.closeOne.Do(func() {
		c.sub.closed.Store(true)
		c.srv.subMu.Lock()
		delete(c.srv.subs, c.sub)
		c.srv.subMu.Unlock()

		c.srv.mu.Lock()
		delete(c.srv.conns, c)
		c.srv.mu.Unlock()

		// Tell the writer to stop and close the socket first, so a pump blocked
		// on a write is released by the socket going away.
		close(c.stop)
		_ = c.ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(writeWait))
		_ = c.ws.Close()

		// Then wait for the writer to finish its last write and exit.
		select {
		case <-c.pumpDone:
		case <-time.After(writeWait):
			c.srv.log.Warn("control writer did not stop in time", "session", c.id)
		}
		c.srv.log.Info("control client disconnected", "session", c.id, "clients", c.srv.ConnectionCount())
	})
}

// ---------------------------------------------------------------- methods ---

func (s *Server) handleHello(ctx context.Context, params json.RawMessage) (any, error) {
	return protocol.HelloResponse{
		Daemon:          s.version.Daemon,
		Version:         s.version.Version,
		ProtocolVersion: protocol.Version,
	}, nil
}

func (s *Server) handleStatus(ctx context.Context, params json.RawMessage) (any, error) {
	if s.statusFn == nil {
		return nil, protocol.NewError(protocol.CodeInternal, "daemon status provider is not configured")
	}
	return s.statusFn(ctx)
}

func (s *Server) handleVersion(ctx context.Context, params json.RawMessage) (any, error) {
	return s.version, nil
}

func (s *Server) handleShutdown(ctx context.Context, params json.RawMessage) (any, error) {
	// Claim the shutdown immediately, before the acknowledgement is sent. The
	// daemon's own stop path runs asynchronously, so without this flag a second
	// daemon.shutdown arriving in that window would be accepted again.
	if !s.shuttingDown.CompareAndSwap(false, true) {
		return nil, protocol.NewError(protocol.CodeDaemonShuttingDown, "daemon is already shutting down")
	}
	var req protocol.ShutdownRequest
	if err := decodeOptional(params, &req); err != nil {
		return nil, err
	}
	grace := s.gracePeriod
	if req.GracePeriod > 0 {
		grace = time.Duration(req.GracePeriod) * time.Millisecond
	}
	if grace > 30*time.Second {
		grace = 30 * time.Second
	}

	s.log.Info("shutdown requested", "grace_ms", grace.Milliseconds(), "reason", req.Reason)

	resp := protocol.ShutdownResponse{
		Accepted:     true,
		GracePeriod:  int(grace / time.Millisecond),
		StopDeadline: time.Now().Add(grace).UTC().Format(time.RFC3339Nano),
	}

	// Stop the socket first so the response reaches the client, then signal the
	// daemon. The goroutine keeps the acknowledgement deterministic: the
	// handler returns before any shutdown work begins.
	if s.onShutdown != nil {
		go func() {
			time.Sleep(150 * time.Millisecond)
			s.onShutdown(grace)
		}()
	}
	return resp, nil
}

func decodeOptional(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return protocol.NewErrorf(protocol.CodeBadRequest, "invalid payload: %v", err)
	}
	return nil
}

// asAPIError normalises any error into a protocol.Error with a machine-readable
// code.
func asAPIError(err error) *protocol.Error {
	if err == nil {
		return protocol.NewError(protocol.CodeInternal, "unexpected error")
	}
	var apiErr *protocol.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return protocol.NewError(protocol.CodeTimeout, "operation timed out")
	case errors.Is(err, context.Canceled):
		return protocol.NewError(protocol.CodeTimeout, "operation cancelled")
	default:
		return protocol.NewErrorf(protocol.CodeInternal, "%s", strings.TrimSpace(err.Error()))
	}
}

// PlatformString is used by daemon.version when the caller does not supply one.
func PlatformString() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
