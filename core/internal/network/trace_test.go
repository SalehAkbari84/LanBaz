package network

import (
	"bytes"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestTracerLogsEachFlowOnceAndWarnsOnSilence(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	now := time.Unix(1000, 0)
	tr := newTracer(log, now)
	subnet := netip.MustParsePrefix("10.200.5.0/24")
	src, bcast := netip.MustParseAddr("10.200.5.2"), netip.MustParseAddr("255.255.255.255")

	// Not right after the game starts, even in an old room...
	tr.checkSilence(now.Add(2*time.Minute), true)
	if buf.Len() != 0 {
		t.Fatalf("warned as soon as the game appeared:\n%s", buf.String())
	}
	// ...but once it has run a minute without any discovery, exactly once.
	tr.checkSilence(now.Add(3*time.Minute+time.Second), true)
	tr.checkSilence(now.Add(4*time.Minute), true)
	if c := strings.Count(buf.String(), "has not searched for"); c != 1 {
		t.Fatalf("silence warnings = %d\n%s", c, buf.String())
	}
	buf.Reset()

	pkt := buildUDP(src, bcast, 50000, 3074, []byte("hi"), 64)
	for i := 0; i < 5; i++ {
		tr.note(pkt, "out", "sent to the room", subnet, now)
	}
	tr.note(buildUDP(src, bcast, 137, 137, []byte("nb"), 64), "out", "sent to the room", subnet, now)
	out := buf.String()
	if c := strings.Count(out, "LAN discovery from this PC"); c != 1 || !strings.Contains(out, "broadcast UDP port 3074 sent to the room") {
		t.Fatalf("want one line for the 3074 broadcast, NetBIOS silent:\n%s", out)
	}
	if strings.Contains(out, "port 137") {
		t.Fatalf("NetBIOS noise was announced:\n%s", out)
	}
}

// A running match is announced once, a server-browser sweep is one line, and
// the 30 s summary carries the packet counts.
func TestTracerSweepAndSummary(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	now := time.Unix(1000, 0)
	tr := newTracer(log, now)
	subnet := netip.MustParsePrefix("10.200.5.0/24")
	me, host := netip.MustParseAddr("10.200.5.2"), netip.MustParseAddr("10.200.5.1")
	bcast := netip.MustParseAddr("255.255.255.255")

	for port := 27015; port <= 27040; port++ {
		tr.note(buildUDP(me, bcast, 50000, port, []byte("q"), 64), "out", "sent to the player", subnet, now)
	}
	for i := 0; i < 100; i++ {
		at := now.Add(time.Duration(i) * 500 * time.Millisecond) // 50 s of play
		tr.note(buildUDP(me, host, 27005, 27015, []byte("p"), 64), "out", "sent to the player", subnet, at)
		tr.note(buildUDP(host, me, 27015, 27005, []byte("p"), 64), "in", "given to the games on this PC", subnet, at)
	}
	out := buf.String()
	if c := strings.Count(out, "LAN discovery from this PC"); c != 1 {
		t.Fatalf("sweep logged %d times:\n%s", c, out)
	}
	if c := strings.Count(out, "game traffic from this PC"); c != 1 {
		t.Fatalf("match flow announced %d times:\n%s", c, out)
	}
	if !strings.Contains(out, "packets sent to the room") || !strings.Contains(out, "UDP 27015 ×") || !strings.Contains(out, "25 more ports") {
		t.Fatalf("no useful summary:\n%s", out)
	}
}
