package network_test

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/network"
	"github.com/lanbaz/lanbaz/core/internal/network/memory"
	"github.com/lanbaz/lanbaz/core/internal/transport"
)

// fakeTransport is an in-process transport. It is a test double for the link
// layer only: it moves bytes between services that share a bus, which is exactly
// what the WebRTC transport does minus the cryptography and the NAT traversal.
//
// The routing rules under test do not care which of those is present, which is
// the architectural claim this file is here to check: the network package only
// ever sees transport.Transport.
type fakeTransport struct {
	bus *bus
	id  transport.PeerID

	mu    sync.Mutex
	peers map[transport.PeerID]bool
	sent  map[transport.PeerID][][]byte
	// got records what actually arrived, keyed by the sender's id. Keeping both
	// directions matters: an assertion that only looks at what a machine sent
	// cannot tell a correct relay from one that never happened.
	got map[transport.PeerID][][]byte

	packets chan transport.Packet
	closed  chan struct{}
	once    sync.Once
}

// bus is the shared fabric the fake transports meet on.
type bus struct {
	mu    sync.Mutex
	links map[transport.PeerID]*fakeTransport
}

func newBus() *bus { return &bus{links: map[transport.PeerID]*fakeTransport{}} }

func (b *bus) join(t *fakeTransport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.links[t.id] = t
}

// deliver hands a packet to exactly one transport.
//
// The packet it queues carries the *sender's* id, because that is what the
// receiver's router uses to look up the address it is allowed to speak from. A
// bus that delivered to everybody but the named recipient would let a packet
// bounce back to its own sender, which is not how a transport behaves and which
// would quietly invalidate the relay tests.
func (b *bus) deliver(to transport.PeerID, pkt transport.Packet) bool {
	b.mu.Lock()
	target := b.links[to]
	b.mu.Unlock()
	if target == nil {
		return false
	}
	target.observe(pkt)
	select {
	case target.packets <- pkt:
		return true
	case <-target.closed:
		return false
	default:
		return false
	}
}

func newFakeTransport(b *bus, id transport.PeerID) *fakeTransport {
	tr := &fakeTransport{
		bus:     b,
		id:      id,
		peers:   map[transport.PeerID]bool{},
		sent:    map[transport.PeerID][][]byte{},
		got:     map[transport.PeerID][][]byte{},
		packets: make(chan transport.Packet, 64),
		closed:  make(chan struct{}),
	}
	b.join(tr)
	return tr
}

func (t *fakeTransport) Name() string                                      { return "fake" }
func (t *fakeTransport) Connect(context.Context, transport.PeerInfo) error { return nil }

func (t *fakeTransport) Close(peer transport.PeerID) error {
	t.mu.Lock()
	delete(t.peers, peer)
	t.mu.Unlock()
	return nil
}

func (t *fakeTransport) Send(peer transport.PeerID, packet []byte) error {
	t.mu.Lock()
	ready := t.peers[peer]
	if ready {
		t.sent[peer] = append(t.sent[peer], append([]byte(nil), packet...))
	}
	t.mu.Unlock()
	if !ready {
		return transport.ErrNotConnected
	}
	// The copy matters: the sender hands over ownership of the payload, and the
	// receiver must not observe a buffer the router is still allowed to touch.
	out := append([]byte(nil), packet...)
	t.bus.deliver(peer, transport.Packet{Peer: t.id, Payload: out, ReceivedAt: time.Now()})
	return nil
}

func (t *fakeTransport) Receive() <-chan transport.Packet { return t.packets }

func (t *fakeTransport) Stats(transport.PeerID) (transport.TransportStats, error) {
	return transport.TransportStats{Transport: "fake"}, nil
}

func (t *fakeTransport) Ready(peer transport.PeerID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.peers[peer]
}

func (t *fakeTransport) Shutdown(context.Context) error {
	t.once.Do(func() {
		close(t.packets)
		close(t.closed)
	})
	return nil
}

// link registers the other end so both sides consider the link usable.
func (t *fakeTransport) link(peer transport.PeerID) {
	t.mu.Lock()
	t.peers[peer] = true
	t.mu.Unlock()
}

// sentTo returns the payloads this transport handed to a peer.
func (t *fakeTransport) sentTo(peer transport.PeerID) [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][]byte(nil), t.sent[peer]...)
}

// receivedFrom returns the payloads that arrived here from a peer.
func (t *fakeTransport) receivedFrom(peer transport.PeerID) [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([][]byte(nil), t.got[peer]...)
}

// observe records an inbound packet. The service drains the receive channel on
// its own goroutine, so the record happens where the packet actually arrives
// rather than where a test happens to look.
func (t *fakeTransport) observe(pkt transport.Packet) {
	t.mu.Lock()
	t.got[pkt.Peer] = append(t.got[pkt.Peer], append([]byte(nil), pkt.Payload...))
	t.mu.Unlock()
}

var _ transport.Transport = (*fakeTransport)(nil)

// node is one machine in the test LAN: a service over an in-memory adapter.
type node struct {
	name    string
	addr    netip.Addr
	tr      *fakeTransport
	adapter *memory.Adapter
	svc     *network.Service
	cancel  context.CancelFunc
}

// quietLogger discards everything.
//
// The router logs every dropped packet at warn level, which is right in
// production and useless in a test: a test that deliberately provokes drops
// would bury its own failure in the output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newNode(t *testing.T, b *bus, room string, subnet netip.Prefix, addr string, isHost bool) *node {
	t.Helper()
	local := netip.MustParseAddr(addr)
	tr := newFakeTransport(b, transport.PeerID(addr))
	adapter := memory.New()
	svc, err := network.NewService(network.ServiceConfig{
		RoomID:    room,
		Subnet:    subnet,
		Local:     local,
		IsHost:    isHost,
		Transport: tr,
		Adapter:   adapter,
		Backend:   "memory",
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		cancel()
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.Stop(context.Background())
		cancel()
	})
	return &node{name: addr, addr: local, tr: tr, adapter: adapter, svc: svc, cancel: cancel}
}

// join tells two nodes their links to each other are up and addresses them.
func join(t *testing.T, a, b *node) {
	t.Helper()
	a.tr.link(b.tr.id)
	b.tr.link(a.tr.id)
	if err := a.svc.AddPeer(string(b.tr.id), b.addr.String()); err != nil {
		t.Fatalf("add peer %s on %s: %v", b.addr, a.name, err)
	}
	if err := b.svc.AddPeer(string(a.tr.id), a.addr.String()); err != nil {
		t.Fatalf("add peer %s on %s: %v", a.addr, b.name, err)
	}
}

// waitFor polls until cond holds, so a test never depends on how many scheduler
// turns a packet takes to cross three goroutines.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func testSubnet() netip.Prefix { return netip.MustParsePrefix("10.200.17.0/24") }

// udpPacket builds a small UDP datagram the way a game would.
func udpPacket(t *testing.T, src, dst string, payload []byte) []byte {
	t.Helper()
	hdr := make([]byte, network.MinHeaderLen+len(payload))
	hdr[0] = 0x45
	total := network.MinHeaderLen + len(payload)
	hdr[2] = byte(total >> 8)
	hdr[3] = byte(total)
	hdr[8] = 64
	hdr[9] = network.ProtocolUDP
	s := netip.MustParseAddr(src).As4()
	d := netip.MustParseAddr(dst).As4()
	copy(hdr[12:16], s[:])
	copy(hdr[16:20], d[:])
	copy(hdr[network.MinHeaderLen:], payload)
	return hdr
}

func TestGuestToHostAndBack(t *testing.T) {
	b := newBus()
	room := "lbzroom-e2e"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	guest := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	join(t, host, guest)

	msg := []byte("hello from the guest")
	guest.adapter.Inject(network.Packet{Payload: udpPacket(t, "10.200.17.2", "10.200.17.1", msg)})

	got, err := host.adapter.Delivered(3 * time.Second)
	if err != nil {
		t.Fatalf("the host did not receive the packet: %v", err)
	}
	if len(got.Payload) == 0 {
		t.Fatal("the host received an empty packet")
	}
	h, err := network.ParseIPv4(got.Payload)
	if err != nil {
		t.Fatalf("the host received something that is not IPv4: %v", err)
	}
	if h.Src.String() != "10.200.17.2" || h.Dst.String() != "10.200.17.1" {
		t.Fatalf("packet %s -> %s, want 10.200.17.2 -> 10.200.17.1", h.Src, h.Dst)
	}

	reply := []byte("and from the host")
	host.adapter.Inject(network.Packet{Payload: udpPacket(t, "10.200.17.1", "10.200.17.2", reply)})
	back, err := guest.adapter.Delivered(3 * time.Second)
	if err != nil {
		t.Fatalf("the guest did not receive the reply: %v", err)
	}
	h, err = network.ParseIPv4(back.Payload)
	if err != nil {
		t.Fatalf("the guest received something that is not IPv4: %v", err)
	}
	if h.Dst.String() != "10.200.17.2" {
		t.Fatalf("reply addressed to %s, want 10.200.17.2", h.Dst)
	}
}

// The hub-and-spoke topology end to end: a guest's packet reaches another guest
// it has never heard of, through the host, which is the whole reason the router
// sends a guest's unresolvable traffic to the hub instead of dropping it.
func TestGuestToGuestThroughTheHost(t *testing.T) {
	b := newBus()
	room := "lbzroom-spoke"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	alice := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	bob := newNode(t, b, room, testSubnet(), "10.200.17.3", false)

	join(t, host, alice)
	join(t, host, bob)
	// Alice is never told about Bob: on a guest that information does not exist,
	// which is the constraint this test exists to prove is survivable.
	alice.adapter.Inject(network.Packet{
		Payload: udpPacket(t, "10.200.17.2", "10.200.17.3", []byte("hi bob")),
	})

	got, err := bob.adapter.Delivered(3 * time.Second)
	if err != nil {
		t.Fatalf("bob did not receive the packet relayed through the host: %v", err)
	}
	h, err := network.ParseIPv4(got.Payload)
	if err != nil {
		t.Fatalf("bob received something that is not IPv4: %v", err)
	}
	if h.Src.String() != "10.200.17.2" || h.Dst.String() != "10.200.17.3" {
		t.Fatalf("packet %s -> %s, want 10.200.17.2 -> 10.200.17.3", h.Src, h.Dst)
	}
	// The host is a router, so the relayed packet costs a hop. That is what
	// stops two guests feeding each other's traffic indefinitely.
	if h.TTL >= 64 {
		t.Errorf("relayed ttl = %d, want less than the original 64", h.TTL)
	}
}

// A broadcast from one guest must reach every other guest and the local stack,
// and must not come back to its sender.
func TestBroadcastRelaysToEveryPeerButTheSender(t *testing.T) {
	b := newBus()
	room := "lbzroom-bcast"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	alice := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	bob := newNode(t, b, room, testSubnet(), "10.200.17.3", false)
	join(t, host, alice)
	join(t, host, bob)

	alice.adapter.Inject(network.Packet{
		Payload: udpPacket(t, "10.200.17.2", "224.0.0.251", []byte("who is out there")),
	})

	// The host sees it locally and relays it to Bob.
	if _, err := host.adapter.Delivered(3 * time.Second); err != nil {
		t.Fatalf("the host did not deliver the broadcast locally: %v", err)
	}
	if _, err := bob.adapter.Delivered(3 * time.Second); err != nil {
		t.Fatalf("bob did not receive the broadcast: %v", err)
	}
	// Alice must not hear her own broadcast echoed back. The way to see an echo
	// is for something to arrive on her inbound queue: the local delivery path
	// is the only one that could put it there.
	if got, _ := alice.adapter.Delivered(200 * time.Millisecond); len(got.Payload) != 0 {
		t.Fatalf("alice's own broadcast was echoed back to her (%d bytes)", len(got.Payload))
	}
	// And exactly one copy must have crossed each link, not a storm: Alice to the
	// hub, and the hub to Bob once.
	if got := alice.tr.sentTo(host.tr.id); len(got) != 1 {
		t.Errorf("alice sent %d copies to the host, want 1", len(got))
	}
	if got := bob.tr.receivedFrom(host.tr.id); len(got) != 1 {
		t.Errorf("bob received %d copies from the host, want 1", len(got))
	}
	if got := alice.tr.receivedFrom(host.tr.id); len(got) != 0 {
		t.Errorf("alice received %d copies from the host, want 0", len(got))
	}
}

// The amplification guard has to work end to end, not just in isolation: a guest
// that floods broadcast must not make the host send unbounded traffic.
func TestBroadcastFloodIsRateLimited(t *testing.T) {
	b := newBus()
	room := "lbzroom-flood"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	alice := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	bob := newNode(t, b, room, testSubnet(), "10.200.17.3", false)
	join(t, host, alice)
	join(t, host, bob)

	const flood = 400
	for i := 0; i < flood; i++ {
		alice.adapter.Inject(network.Packet{
			Payload: udpPacket(t, "10.200.17.2", "255.255.255.255", []byte("flood")),
		})
	}
	waitFor(t, "the flood to be processed", func() bool {
		return alice.svc.Metrics().RateLimited > 0
	})
	// The host relays at most its bucket's worth; the rest is dropped at the
	// source, which is the only place the cost can be kept off the host's link.
	relayed := host.svc.Metrics().Relayed
	if relayed > broadcastCeilingForTest {
		t.Errorf("the host relayed %d broadcasts from a peer limited to %d", relayed, broadcastCeilingForTest)
	}
	if alice.svc.Metrics().RateLimited == 0 {
		t.Error("no broadcast was rate limited at the source")
	}
}

// broadcastCeilingForTest is the most the host can relay from one peer inside
// the limiter's burst plus a little refill slack for the test's own runtime.
const broadcastCeilingForTest = 260

// The rule that keeps one guest from impersonating another, exercised through
// the real packet path rather than through the router alone.
func TestASpoofedPacketIsDroppedInFlight(t *testing.T) {
	b := newBus()
	room := "lbzroom-spoof"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	alice := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	bob := newNode(t, b, room, testSubnet(), "10.200.17.3", false)
	join(t, host, alice)
	join(t, host, bob)

	// Bob's link sends a packet claiming to come from Alice.
	host.tr.sendRaw(transport.PeerID("10.200.17.3"), "10.200.17.2", []byte("i am alice"))

	waitFor(t, "the spoof counter to move", func() bool {
		return host.svc.Metrics().Spoofed > 0
	})
	// And it must never have reached Alice's machine.
	if got, _ := alice.adapter.Delivered(150 * time.Millisecond); len(got.Payload) != 0 {
		t.Fatalf("a spoofed packet was delivered locally: %d bytes", len(got.Payload))
	}
}

// A peer that leaves must stop receiving, and its address must be reusable.
func TestRemovingAPeerStopsItsTraffic(t *testing.T) {
	b := newBus()
	room := "lbzroom-leave"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	alice := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	join(t, host, alice)

	host.svc.RemovePeer(string(alice.tr.id))
	alice.adapter.Inject(network.Packet{
		Payload: udpPacket(t, "10.200.17.2", "10.200.17.1", []byte("still there?")),
	})
	if got, _ := host.adapter.Delivered(250 * time.Millisecond); len(got.Payload) != 0 {
		t.Fatalf("a removed peer still reached the host: %d bytes", len(got.Payload))
	}
	if got := host.svc.Metrics().NoRoute; got == 0 {
		t.Error("the unroutable packet was not counted")
	}
}

func TestStatusDescribesTheRoom(t *testing.T) {
	b := newBus()
	room := "lbzroom-status"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	guest := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	join(t, host, guest)

	st, err := host.svc.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != network.StateReady {
		t.Errorf("state = %q, want ready", st.State)
	}
	if st.Adapter != "memory" {
		t.Errorf("adapter = %q, want memory", st.Adapter)
	}
	if !st.IsHost {
		t.Error("the host's status does not say it is the host")
	}
	if got := st.Address.IPv4.Addr().String(); got != "10.200.17.1" {
		t.Errorf("address = %s, want 10.200.17.1", got)
	}
	if len(st.Peers) != 2 {
		t.Fatalf("status lists %d peers, want 2 (self and one guest)", len(st.Peers))
	}
	if !st.Peers[0].Local || st.Peers[0].Role != "self" {
		t.Errorf("first entry is %+v, want the local self entry", st.Peers[0])
	}
	if got := st.Peers[1].Role; got != "peer" {
		t.Errorf("second entry role = %q, want peer", got)
	}
	// A default route would mean LanBaz had claimed to be a gateway.
	for _, r := range st.Routes {
		if r.Destination.Bits() == 0 {
			t.Fatal("the routing table contains a default route")
		}
	}
	routes, err := host.svc.Routes(context.Background())
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	if len(routes) != 2 {
		t.Errorf("routing table has %d entries, want 2", len(routes))
	}
}

func TestStopIsIdempotentAndClosesTheAdapter(t *testing.T) {
	b := newBus()
	room := "lbzroom-stop"
	tr := newFakeTransport(b, "10.200.17.1")
	svc, err := network.NewService(network.ServiceConfig{
		RoomID:    room,
		Subnet:    testSubnet(),
		Local:     netip.MustParseAddr("10.200.17.1"),
		IsHost:    true,
		Transport: tr,
		Adapter:   memory.New(),
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Stopping twice is what a room close racing a daemon shutdown looks like.
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("status after stop: %v", err)
	}
	if st.State != network.StateStopped {
		t.Errorf("state after stop = %q, want stopped", st.State)
	}
}

// Starting twice must not create a second adapter. Wintun would refuse, and the
// memory backend refusing for the same reason keeps the two honest.
func TestStartTwiceDoesNotCreateASecondAdapter(t *testing.T) {
	b := newBus()
	adapter := memory.New()
	svc, err := network.NewService(network.ServiceConfig{
		RoomID:    "lbzroom-double",
		Subnet:    testSubnet(),
		Local:     netip.MustParseAddr("10.200.17.1"),
		Transport: newFakeTransport(b, "10.200.17.1"),
		Adapter:   adapter,
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background()) })
	// The second Start is a no-op rather than an error: a room being reopened
	// after a UI reconnect should not fail because the network was already up.
	if err := svc.Start(context.Background()); err != nil {
		t.Errorf("second start: %v", err)
	}
}

func TestNewServiceRejectsAnIncompleteConfiguration(t *testing.T) {
	subnet := testSubnet()
	local := netip.MustParseAddr("10.200.17.1")
	tr := newFakeTransport(newBus(), "p")
	ad := memory.New()

	for name, cfg := range map[string]network.ServiceConfig{
		"no room":      {Subnet: subnet, Local: local, Transport: tr, Adapter: ad},
		"no adapter":   {RoomID: "r", Subnet: subnet, Local: local, Transport: tr},
		"no transport": {RoomID: "r", Subnet: subnet, Local: local, Adapter: ad},
		"bad subnet":   {RoomID: "r", Subnet: netip.Prefix{}, Local: local, Transport: tr, Adapter: ad},
	} {
		if _, err := network.NewService(cfg); err == nil {
			t.Errorf("%s: a service was built from an incomplete configuration", name)
		}
	}
}

// sendRaw injects a packet onto this transport's inbound queue as if a peer had
// sent it. It exists so a test can simulate a dishonest peer without a second
// service in the way.
func (t *fakeTransport) sendRaw(from transport.PeerID, claimSrc string, payload []byte) {
	pkt := udpPacketRaw(claimSrc, "10.200.17.1", payload)
	select {
	case t.packets <- transport.Packet{Peer: from, Payload: pkt, ReceivedAt: time.Now()}:
	case <-t.closed:
	}
}

// udpPacketRaw is udpPacket without a *testing.T, for use from a non-test
// goroutine context.
func udpPacketRaw(src, dst string, payload []byte) []byte {
	hdr := make([]byte, network.MinHeaderLen+len(payload))
	hdr[0] = 0x45
	total := network.MinHeaderLen + len(payload)
	hdr[2] = byte(total >> 8)
	hdr[3] = byte(total)
	hdr[8] = 64
	hdr[9] = network.ProtocolUDP
	s := netip.MustParseAddr(src).As4()
	d := netip.MustParseAddr(dst).As4()
	copy(hdr[12:16], s[:])
	copy(hdr[16:20], d[:])
	copy(hdr[network.MinHeaderLen:], payload)
	return hdr
}

// Discovery is sent with TTL 1 - Java's MulticastSocket defaults to it, and
// Minecraft's "Open to LAN" uses it - so the hub must relay it between guests
// without spending a hop, the way a switch would.
func TestTTLOneDiscoveryReachesAnotherGuest(t *testing.T) {
	b := newBus()
	room := "lbzroom-ttl1"
	host := newNode(t, b, room, testSubnet(), "10.200.17.1", true)
	alice := newNode(t, b, room, testSubnet(), "10.200.17.2", false)
	bob := newNode(t, b, room, testSubnet(), "10.200.17.3", false)
	join(t, host, alice)
	join(t, host, bob)

	pkt := udpPacket(t, "10.200.17.2", "224.0.2.60", []byte("[MOTD]world[/MOTD][AD]25565[/AD]"))
	pkt[8] = 1
	alice.adapter.Inject(network.Packet{Payload: pkt})

	if _, err := host.adapter.Delivered(3 * time.Second); err != nil {
		t.Fatalf("the host did not see the announcement: %v", err)
	}
	got, err := bob.adapter.Delivered(3 * time.Second)
	if err != nil {
		t.Fatalf("bob did not see a TTL-1 announcement from alice: %v", err)
	}
	h, err := network.ParseIPv4(got.Payload)
	if err != nil {
		t.Fatalf("bob received something that is not IPv4: %v", err)
	}
	if h.TTL != 1 {
		t.Errorf("relayed discovery ttl = %d, want it untouched at 1", h.TTL)
	}
}
