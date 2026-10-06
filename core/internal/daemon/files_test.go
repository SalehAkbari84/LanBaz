package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// cutConn fails after a number of bytes, once, to force a resume.
type cutConn struct {
	net.Conn
	left int64
}

func (c *cutConn) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errors.New("cable pulled")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.Conn.Read(p)
	c.left -= int64(n)
	return n, err
}

// A file goes from one player to another over the room, survives a broken
// connection by resuming, and arrives byte for byte under its own name.
func TestFileTransferResumesAndVerifies(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real WebRTC links")
	}
	// The rooms run on in-memory adapters, so the transfer uses loopback while
	// keeping the room addresses in the offer.
	var cut atomic.Bool
	oldListen, oldDial, oldOK := fileListen, fileDial, fileRemoteOK
	fileListen = func(netip.Addr) (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }
	fileDial = func(ctx context.Context, addr string) (net.Conn, error) {
		ap := netip.MustParseAddrPort(addr)
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp4", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ap.Port()).String())
		if err == nil && !cut.Swap(true) {
			return &cutConn{Conn: c, left: 1 << 20}, nil
		}
		return c, err
	}
	fileRemoteOK = func(netip.Prefix, netip.Addr) bool { return true }
	t.Cleanup(func() { fileListen, fileDial, fileRemoteOK = oldListen, oldDial, oldOK })

	bus := &social.MemBus{}
	host, hostCfg := newTestDaemonBus(t, bus)
	guest, guestCfg := newTestDaemonBus(t, bus)
	hc := startDaemon(t, host, hostCfg)
	gc := startDaemon(t, guest, guestCfg)
	guest.files.dir = t.TempDir()
	hostPub, guestPub := befriend(t, hc, gc)
	if err := call(t, hc, protocol.MethodFriendsTrust, protocol.FriendTrustRequest{Pub: guestPub, Trusted: true}, nil); err != nil {
		t.Fatal(err)
	}
	var created protocol.RoomCreateResponse
	if err := call(t, hc, protocol.MethodRoomCreate, protocol.RoomCreateRequest{Name: "Files"}, &created); err != nil {
		t.Fatal(err)
	}
	if err := call(t, gc, protocol.MethodJoinRequest, protocol.JoinFriendRequest{Pub: hostPub, RoomID: created.Room.RoomID}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the guest joins", 60*time.Second, func() bool { return connectedTo(t, hc, string(guest.id)) })
	waitFor(t, "the room knows the guest's address", 30*time.Second, func() bool {
		var rooms []protocol.RoomSummary
		if call(t, hc, protocol.MethodRoomList, nil, &rooms) != nil || len(rooms) != 1 {
			return false
		}
		for _, p := range rooms[0].Peers {
			if string(p.PeerID) == string(guest.id) && p.VirtualAddress != "" {
				return true
			}
		}
		return false
	})
	// The guest must also know the host's address to accept its offer.
	waitFor(t, "the guest knows the host's address", 30*time.Second, func() bool {
		var rooms []protocol.RoomSummary
		if call(t, gc, protocol.MethodRoomList, nil, &rooms) != nil || len(rooms) != 1 {
			return false
		}
		for _, p := range rooms[0].Peers {
			if string(p.PeerID) == string(host.id) && p.VirtualAddress != "" {
				return true
			}
		}
		return false
	})

	content := make([]byte, 3<<20+123)
	_, _ = rand.Read(content)
	src := filepath.Join(t.TempDir(), "map pack.zip")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	updates := map[string]protocol.FileTransfer{}
	gc.On(protocol.EventFileUpdate, func(m protocol.Message) {
		var ft protocol.FileTransfer
		if json.Unmarshal(m.Payload, &ft) == nil {
			mu.Lock()
			updates[ft.ID] = ft
			mu.Unlock()
		}
	})
	var offered protocol.FileTransfer
	if err := call(t, hc, protocol.MethodFileOffer, protocol.FileOfferRequest{RoomID: created.Room.RoomID, PeerID: string(guest.id), Path: src}, &offered); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the guest is offered the file", 15*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return updates[offered.ID].State == "incoming"
	})
	if err := call(t, gc, protocol.MethodFileRespond, protocol.FileRespondRequest{ID: offered.ID, Accept: true}, nil); err != nil {
		t.Fatal(err)
	}
	var final protocol.FileTransfer
	waitFor(t, "the file arrives", 60*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		final = updates[offered.ID]
		return final.State == "done" || final.State == "failed"
	})
	if final.State != "done" {
		t.Fatalf("transfer ended %+v", final)
	}
	if !cut.Load() {
		t.Fatal("the connection was never cut, so resume was not tested")
	}
	got, err := os.ReadFile(final.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) || filepath.Base(final.Path) != "map pack.zip" {
		t.Fatalf("received %d bytes as %s", len(got), final.Path)
	}
	if left, _ := filepath.Glob(filepath.Join(guest.files.dir, "*"+partSuffix)); len(left) != 0 {
		t.Fatalf("partial files left behind: %v", left)
	}
}

func TestSafeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"map.zip":             "map.zip",
		`..\..\Windows\x.dll`: "x.dll",
		"../../etc/passwd":    "passwd",
		"CON.txt":             "_CON.txt",
		"a<b>c:d.exe":         "a_b_c_d.exe",
		"  .hidden. ":         "hidden",
		"":                    "file",
		"x" + partSuffix:      "file",
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFinishedTransfersArePruned(t *testing.T) {
	f := &fileSvc{t: map[string]*transfer{}}
	for i := 0; i < keepFinished+10; i++ {
		tr := &transfer{info: protocol.FileTransfer{ID: fmt.Sprint(i), State: "done"}, ended: time.Unix(int64(i), 0)}
		f.t[tr.info.ID] = tr
	}
	f.t["live"] = &transfer{info: protocol.FileTransfer{ID: "live", State: "transferring"}}
	f.pruneLocked()
	if len(f.t) != keepFinished+1 || f.t["live"] == nil || f.t["0"] != nil || f.t[fmt.Sprint(keepFinished+9)] == nil {
		t.Fatalf("kept %d transfers", len(f.t))
	}
}
