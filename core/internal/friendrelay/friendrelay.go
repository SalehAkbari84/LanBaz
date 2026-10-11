// Package friendrelay lets a player be the relay for their friends.
//
// When two players are both behind carrier-grade NAT (most mobile and many
// home lines) no direct path exists, and the connection needs a TURN relay.
// LanBaz's usual relays and STUN servers are abroad; during an international
// outage none of them answers, while Iranian addresses still reach each
// other. A friend with a home line that has a public address (or a router
// that forwards ports) can then run a small TURN+STUN server inside LanBaz
// and hand the others a share code. Traffic through it stays end-to-end
// encrypted (DTLS between the two players); the relay sees opaque packets.
package friendrelay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v5"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

const (
	// Realm is the TURN realm; it is part of the credential hash.
	Realm = "lanbaz"
	// DefaultPort is the listening port (STUN and TURN on one UDP port).
	DefaultPort = 3478
	// RelayPorts is how many ports after the listening port carry relayed
	// traffic. Each player's connection through the relay takes one.
	RelayPorts = 32
	// DefaultCapMbps bounds the relay's total throughput.
	DefaultCapMbps = 40

	codePrefix = "lanbaz-relay:"
)

// Code is what a friend pastes to use someone's relay.
type Code struct {
	V    int    `json:"v"`
	Host string `json:"h"`
	Port int    `json:"p"`
	User string `json:"u"`
	Pass string `json:"k"`
	Name string `json:"n,omitempty"`
}

// Encode renders the share code.
func (c Code) Encode() string {
	raw, _ := json.Marshal(c)
	return codePrefix + base64.RawURLEncoding.EncodeToString(raw)
}

// ParseCode reads a share code.
func ParseCode(s string) (Code, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(s), codePrefix) {
		return Code{}, errors.New("not a LanBaz relay code")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(codePrefix):])
	if err != nil {
		return Code{}, errors.New("the relay code is damaged")
	}
	var c Code
	if err := json.Unmarshal(raw, &c); err != nil || c.V != 1 {
		return Code{}, errors.New("the relay code is damaged or from a newer LanBaz")
	}
	ip, err := netip.ParseAddr(c.Host)
	if err != nil || !ip.Is4() || c.Port <= 0 || c.Port > 65535 || c.User == "" || c.Pass == "" || len(c.Pass) > 128 || len(c.User) > 64 {
		return Code{}, errors.New("the relay code is damaged")
	}
	return c, nil
}

// Servers are the STUN and TURN entries a code adds to the settings.
func (c Code) Servers() (stun string, relay protocol.RelayServer) {
	hp := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	return "stun:" + hp, protocol.RelayServer{URL: "turn:" + hp + "?transport=udp", Username: c.User, Credential: c.Pass}
}

// NewSecret makes a random credential.
func NewSecret() string {
	var b [18]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Config starts a relay.
type Config struct {
	// Listen is the local UDP port; the relay ports follow it.
	Port int
	// Public is the address friends reach this relay at.
	Public netip.Addr
	User   string
	Pass   string
	// CapMbps bounds total throughput; 0 = DefaultCapMbps.
	CapMbps int
	Log     *slog.Logger
}

// Server is a running relay.
type Server struct {
	cfg  Config
	srv  *turn.Server
	lim  *limiter
	conn net.PacketConn
}

// Start runs the relay until Close.
func Start(cfg Config) (*Server, error) {
	if cfg.Port <= 0 {
		cfg.Port = DefaultPort
	}
	if cfg.CapMbps <= 0 {
		cfg.CapMbps = DefaultCapMbps
	}
	if !cfg.Public.Is4() {
		return nil, errors.New("friendrelay: no public IPv4 address")
	}
	if cfg.User == "" || cfg.Pass == "" {
		return nil, errors.New("friendrelay: missing credentials")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	lim := newLimiter(int64(cfg.CapMbps) * 1_000_000 / 8)
	udp, err := net.ListenPacket("udp4", fmt.Sprintf("0.0.0.0:%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("friendrelay: port %d is busy: %w", cfg.Port, err)
	}
	key := turn.GenerateAuthKey(cfg.User, Realm, cfg.Pass)
	gen := &limitedGenerator{
		inner: &turn.RelayAddressGeneratorPortRange{
			RelayAddress: net.IP(cfg.Public.AsSlice()),
			Address:      "0.0.0.0",
			MinPort:      uint16(cfg.Port + 1),
			MaxPort:      uint16(cfg.Port + RelayPorts),
		},
		lim: lim,
	}
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: Realm,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			if ra.Username != cfg.User {
				return "", nil, false
			}
			return ra.Username, key, true
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn:            &limitedConn{PacketConn: udp, lim: lim},
			RelayAddressGenerator: gen,
		}},
	})
	if err != nil {
		_ = udp.Close()
		return nil, fmt.Errorf("friendrelay: %w", err)
	}
	cfg.Log.Info("relay for friends started", "public", cfg.Public, "port", cfg.Port, "relay_ports", fmt.Sprintf("%d-%d", cfg.Port+1, cfg.Port+RelayPorts), "cap_mbps", cfg.CapMbps)
	return &Server{cfg: cfg, srv: srv, lim: lim, conn: udp}, nil
}

// Code is the share code for this relay.
func (s *Server) Code(name string) Code {
	return Code{V: 1, Host: s.cfg.Public.String(), Port: s.cfg.Port, User: s.cfg.User, Pass: s.cfg.Pass, Name: name}
}

// Relayed is the number of bytes relayed so far.
func (s *Server) Relayed() int64 { return s.lim.total() }

// Close stops the relay.
func (s *Server) Close() error {
	err := s.srv.Close()
	s.cfg.Log.Info("relay for friends stopped", "relayed_mb", s.lim.total()/1_000_000)
	return err
}

// Ports lists every UDP port the relay needs forwarded on the router.
func Ports(port int) []int {
	if port <= 0 {
		port = DefaultPort
	}
	out := make([]int, 0, RelayPorts+1)
	for p := port; p <= port+RelayPorts; p++ {
		out = append(out, p)
	}
	return out
}

// ---------------------------------------------------------------- limits --

// limiter is a shared token bucket over everything the relay sends. Over the
// cap, packets are dropped (UDP semantics) rather than queued, so the
// players' congestion control sees loss and slows down instead of the relay
// building a backlog.
type limiter struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	tokens float64
	last   time.Time
	sent   int64
}

func newLimiter(bytesPerSec int64) *limiter {
	return &limiter{rate: float64(bytesPerSec), tokens: float64(bytesPerSec) / 4, last: time.Now()}
}

func (l *limiter) allow(n int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if burst := l.rate / 4; l.tokens > burst {
		l.tokens = burst
	}
	l.last = now
	if l.tokens < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	l.sent += int64(n)
	return true
}

func (l *limiter) total() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sent
}

type limitedConn struct {
	net.PacketConn
	lim *limiter
}

func (c *limitedConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if !c.lim.allow(len(p)) {
		return len(p), nil // dropped, as a congested link would
	}
	return c.PacketConn.WriteTo(p, addr)
}

type limitedGenerator struct {
	inner *turn.RelayAddressGeneratorPortRange
	lim   *limiter
}

func (g *limitedGenerator) Validate() error { return g.inner.Validate() }

func (g *limitedGenerator) AllocatePacketConn(conf turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	c, a, err := g.inner.AllocatePacketConn(conf)
	if err != nil {
		return nil, nil, err
	}
	return &limitedConn{PacketConn: c, lim: g.lim}, a, nil
}

func (g *limitedGenerator) AllocateListener(conf turn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	return g.inner.AllocateListener(conf)
}

func (g *limitedGenerator) AllocateConn(conf turn.AllocateConnConfig) (net.Conn, error) {
	return g.inner.AllocateConn(conf)
}

// ensure the interface still matches the library.
var _ turn.RelayAddressGenerator = (*limitedGenerator)(nil)
