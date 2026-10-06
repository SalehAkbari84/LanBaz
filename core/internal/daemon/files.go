package daemon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// File transfer between players.
//
// The offer and the answer travel as room signalling; the bytes go over TCP
// between the two daemons on the room's own addresses (10.200.x), so they ride
// the same tunnel as the games - through NAT, the VPN bypass and the relay -
// and get TCP's flow control for free. The sender listens on its room address
// for one receiver holding a one-time token; the receiver dials only the
// address the room knows for that player. A broken connection resumes from
// the bytes already on disk, and the receiver checks a SHA-256 of the whole
// file before the file gets its real name.

const (
	maxFileSize      = 64 << 30
	maxOutgoing      = 3
	offerTTL         = 10 * time.Minute
	gameRateLimit    = 4 << 20 // bytes/s while a game is running
	fileChunk        = 64 << 10
	progressEvery    = 250 * time.Millisecond
	receiveRetries   = 5
	partSuffix       = ".lanbaz-part"
	fileDialTimeout  = 10 * time.Second
	fileAckTimeout   = 2 * time.Minute
	fileHandshakeMax = 512
)

// The network calls, replaceable in tests where the room runs on an in-memory
// adapter rather than a real 10.200.x interface.
var (
	fileListen = func(ip netip.Addr) (net.Listener, error) {
		return net.Listen("tcp4", netip.AddrPortFrom(ip, 0).String())
	}
	fileDial = func(ctx context.Context, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: fileDialTimeout}
		return d.DialContext(ctx, "tcp4", addr)
	}
	fileRemoteOK = func(subnet netip.Prefix, a netip.Addr) bool {
		return !subnet.IsValid() || subnet.Contains(a.Unmap())
	}
)

type fileMsg struct {
	T     string `json:"t"` // offer, decline, cancel
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Addr  string `json:"addr,omitempty"`
	Token string `json:"token,omitempty"`
}

type handshake struct {
	Token  string `json:"token"`
	Offset int64  `json:"offset"`
}

type transfer struct {
	info   protocol.FileTransfer
	path   string // local file: the source, or the destination once done
	addr   string
	token  string
	ln     net.Listener
	cancel context.CancelFunc
	done   atomic.Int64
}

type fileSvc struct {
	d   *Daemon
	log *slog.Logger
	dir string // where received files go

	mu sync.Mutex
	t  map[string]*transfer
}

func newFileSvc(d *Daemon) *fileSvc {
	dir := ""
	if home, err := os.UserHomeDir(); err == nil {
		dir = filepath.Join(home, "Downloads", "LanBaz")
	}
	return &fileSvc{d: d, log: d.log.With("component", "files"), dir: dir, t: map[string]*transfer{}}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// publish sends the transfer's current state to the UI.
func (f *fileSvc) publish(t *transfer) {
	f.mu.Lock()
	info := t.info
	f.mu.Unlock()
	info.Done = t.done.Load()
	f.d.api.PublishEvent(protocol.EventFileUpdate, info)
}

func (f *fileSvc) setState(t *transfer, state, errMsg string) {
	f.mu.Lock()
	t.info.State, t.info.Error = state, errMsg
	f.mu.Unlock()
	f.publish(t)
}

func (f *fileSvc) list() []protocol.FileTransfer {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.FileTransfer, 0, len(f.t))
	for _, t := range f.t {
		info := t.info
		info.Done = t.done.Load()
		out = append(out, info)
	}
	return out
}

func (f *fileSvc) peerName(r *room.Room, id string) string {
	for _, p := range r.Summary().Peers {
		if string(p.PeerID) == id {
			if p.DisplayName != "" {
				return p.DisplayName
			}
		}
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ------------------------------------------------------------------ sending --

func (f *fileSvc) offer(ctx context.Context, req protocol.FileOfferRequest) (protocol.FileTransfer, error) {
	rooms := f.d.Rooms()
	if rooms == nil {
		return protocol.FileTransfer{}, protocol.NewError(protocol.CodeUnsupportedVersion, "files: no room service")
	}
	r, err := rooms.Get(req.RoomID)
	if err != nil {
		return protocol.FileTransfer{}, err
	}
	st, err := os.Stat(req.Path)
	if err != nil || !st.Mode().IsRegular() {
		return protocol.FileTransfer{}, protocol.NewError(protocol.CodeBadRequest, "files: pick a file (not a folder) that exists")
	}
	if st.Size() == 0 || st.Size() > maxFileSize {
		return protocol.FileTransfer{}, protocol.NewError(protocol.CodeBadRequest, "files: the file must be between 1 byte and 64 GB")
	}
	f.mu.Lock()
	n := 0
	for _, t := range f.t {
		if t.info.Direction == "out" && (t.info.State == "offered" || t.info.State == "transferring") {
			n++
		}
	}
	f.mu.Unlock()
	if n >= maxOutgoing {
		return protocol.FileTransfer{}, protocol.NewError(protocol.CodeBadRequest, "files: wait for one of your 3 transfers to finish")
	}
	local, err := netip.ParseAddr(r.Summary().LocalAddress)
	if err != nil {
		return protocol.FileTransfer{}, protocol.NewError(protocol.CodeBadRequest, "files: the room's network is not up yet")
	}
	ln, err := fileListen(local)
	if err != nil {
		return protocol.FileTransfer{}, fmt.Errorf("files: listen on the room address: %w", err)
	}
	advertised := netip.AddrPortFrom(local, uint16(ln.Addr().(*net.TCPAddr).Port)).String()
	tctx, cancel := context.WithCancel(context.Background())
	t := &transfer{
		path: req.Path, addr: advertised, token: randomHex(16), ln: ln, cancel: cancel,
		info: protocol.FileTransfer{
			ID: randomHex(8), RoomID: req.RoomID, PeerID: protocol.PeerID(req.PeerID), PeerName: f.peerName(r, req.PeerID),
			Name: filepath.Base(req.Path), Size: st.Size(), Direction: "out", State: "offered", Path: req.Path,
		},
	}
	f.mu.Lock()
	f.t[t.info.ID] = t
	f.mu.Unlock()

	msg, _ := json.Marshal(fileMsg{T: "offer", ID: t.info.ID, Name: t.info.Name, Size: t.info.Size, Addr: t.addr, Token: t.token})
	if err := rooms.SendSignal(req.RoomID, room.SignalFile, req.PeerID, msg); err != nil {
		cancel()
		_ = ln.Close()
		f.setState(t, "failed", err.Error())
		return t.info, err
	}
	f.log.Info("file offered", "file", t.info.Name, "size", t.info.Size, "to", t.info.PeerName)
	f.publish(t)
	go f.serve(tctx, t, r.Summary().Subnet)
	// The offer, not the transfer, expires: a big file may take hours.
	go func() {
		select {
		case <-tctx.Done():
		case <-time.After(offerTTL):
			f.mu.Lock()
			waiting := t.info.State == "offered"
			f.mu.Unlock()
			if waiting {
				f.setState(t, "expired", "the other player did not take the file in time")
				cancel()
			}
		}
	}()
	return t.info, nil
}

// serve accepts the receiver until the file is delivered, the offer expires
// or either side cancels. Each connection may resume where the last one broke.
func (f *fileSvc) serve(ctx context.Context, t *transfer, subnet string) {
	defer t.cancel()
	go func() {
		<-ctx.Done()
		_ = t.ln.Close()
	}()
	pfx, _ := netip.ParsePrefix(subnet)
	for {
		conn, err := t.ln.Accept()
		if err != nil {
			f.mu.Lock()
			state := t.info.State
			f.mu.Unlock()
			if state == "offered" || state == "transferring" {
				f.setState(t, "failed", "the transfer stopped")
			}
			return
		}
		ap, _ := netip.ParseAddrPort(conn.RemoteAddr().String())
		if !fileRemoteOK(pfx, ap.Addr()) {
			_ = conn.Close()
			continue
		}
		ok, err := f.send(ctx, t, conn)
		_ = conn.Close()
		if ok {
			f.setState(t, "done", "")
			f.log.Info("file sent", "file", t.info.Name, "to", t.info.PeerName)
			return
		}
		if err != nil {
			f.log.Debug("file connection ended; the receiver may resume", "file", t.info.Name, "error", err)
		}
	}
}

func (f *fileSvc) send(ctx context.Context, t *transfer, conn net.Conn) (bool, error) {
	_ = conn.SetReadDeadline(time.Now().Add(fileDialTimeout))
	line, err := bufio.NewReaderSize(conn, fileHandshakeMax).ReadSlice('\n')
	if err != nil {
		return false, err
	}
	var hs handshake
	if json.Unmarshal(line, &hs) != nil || subtle.ConstantTimeCompare([]byte(hs.Token), []byte(t.token)) != 1 {
		return false, errors.New("bad handshake")
	}
	if hs.Offset < 0 || hs.Offset > t.info.Size {
		return false, errors.New("bad offset")
	}
	_ = conn.SetReadDeadline(time.Time{})
	src, err := os.Open(t.path)
	if err != nil {
		f.setState(t, "failed", err.Error())
		return false, err
	}
	defer src.Close()
	h := sha256.New()
	// The receiver verifies the whole file, so the part it already has is
	// hashed here too.
	if _, err := io.CopyN(h, src, hs.Offset); err != nil {
		return false, err
	}
	t.done.Store(hs.Offset)
	f.setState(t, "transferring", "")
	if err := f.pump(ctx, t, io.TeeReader(io.LimitReader(src, t.info.Size-hs.Offset), h), conn); err != nil {
		return false, err
	}
	if _, err := conn.Write(h.Sum(nil)); err != nil {
		return false, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(fileAckTimeout))
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return false, err
	}
	if ack[0] != 'K' {
		f.setState(t, "failed", "the received copy did not match; send it again")
		return false, errors.New("receiver rejected the checksum")
	}
	return true, nil
}

// pump copies with progress events, and slows down while a game is running so
// a big file never costs anybody a lag spike.
func (f *fileSvc) pump(ctx context.Context, t *transfer, src io.Reader, dst io.Writer) error {
	buf := make([]byte, fileChunk)
	var (
		last      = time.Now()
		lastBytes = t.done.Load()
		windowAt  = time.Now()
		windowN   int64
	)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			done := t.done.Add(int64(n))
			if f.d.gameRunning.Load() {
				windowN += int64(n)
				want := time.Duration(windowN) * time.Second / gameRateLimit
				if el := time.Since(windowAt); want > el {
					time.Sleep(want - el)
				}
				if time.Since(windowAt) > 2*time.Second {
					windowAt, windowN = time.Now(), 0
				}
			} else {
				windowAt, windowN = time.Now(), 0
			}
			if el := time.Since(last); el >= progressEvery {
				f.mu.Lock()
				t.info.Rate = int64(float64(done-lastBytes) / el.Seconds())
				f.mu.Unlock()
				last, lastBytes = time.Now(), done
				f.publish(t)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// ---------------------------------------------------------------- receiving --

func (f *fileSvc) onSignal(s protocol.VoiceSignal) {
	var m fileMsg
	if json.Unmarshal(s.Data, &m) != nil || m.ID == "" || len(m.ID) > 32 {
		return
	}
	switch m.T {
	case "offer":
		f.onOffer(s, m)
	case "decline", "cancel":
		f.mu.Lock()
		t, ok := f.t[m.ID]
		f.mu.Unlock()
		if !ok || string(t.info.PeerID) != string(s.From) {
			return
		}
		if t.cancel != nil {
			t.cancel()
		}
		state := "canceled"
		if m.T == "decline" {
			state = "declined"
		}
		f.setState(t, state, "")
	}
}

func (f *fileSvc) onOffer(s protocol.VoiceSignal, m fileMsg) {
	rooms := f.d.Rooms()
	if rooms == nil {
		return
	}
	r, err := rooms.Get(s.RoomID)
	if err != nil {
		return
	}
	// Dial only the address this room knows for the sender: an offer must not
	// be able to point the daemon at an arbitrary host.
	ap, err := netip.ParseAddrPort(m.Addr)
	if err != nil {
		return
	}
	known := false
	for _, p := range r.Summary().Peers {
		if p.PeerID == s.From && p.VirtualAddress == ap.Addr().String() {
			known = true
		}
	}
	if !known || m.Size <= 0 || m.Size > maxFileSize || len(m.Token) != 32 {
		f.log.Warn("ignored a file offer that did not match the sender's room address", "from", string(s.From))
		return
	}
	t := &transfer{
		addr: m.Addr, token: m.Token,
		info: protocol.FileTransfer{
			ID: m.ID, RoomID: s.RoomID, PeerID: s.From, PeerName: f.peerName(r, string(s.From)),
			Name: safeFileName(m.Name), Size: m.Size, Direction: "in", State: "incoming",
		},
	}
	f.mu.Lock()
	if _, dup := f.t[m.ID]; dup {
		f.mu.Unlock()
		return
	}
	f.t[m.ID] = t
	f.mu.Unlock()
	f.log.Info("file offered to you", "file", t.info.Name, "size", t.info.Size, "from", t.info.PeerName)
	f.publish(t)
}

func (f *fileSvc) respond(req protocol.FileRespondRequest) error {
	f.mu.Lock()
	t, ok := f.t[req.ID]
	f.mu.Unlock()
	if !ok || t.info.Direction != "in" || t.info.State != "incoming" {
		return protocol.NewError(protocol.CodeNotFound, "files: that offer is gone")
	}
	if !req.Accept {
		f.signal(t, "decline")
		f.setState(t, "declined", "")
		return nil
	}
	if f.dir == "" {
		return protocol.NewError(protocol.CodeInternal, "files: no Downloads folder")
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	t.cancel = cancel
	f.mu.Unlock()
	f.setState(t, "transferring", "")
	go f.receive(ctx, t)
	return nil
}

func (f *fileSvc) signal(t *transfer, kind string) {
	if rooms := f.d.Rooms(); rooms != nil {
		msg, _ := json.Marshal(fileMsg{T: kind, ID: t.info.ID})
		_ = rooms.SendSignal(t.info.RoomID, room.SignalFile, string(t.info.PeerID), msg)
	}
}

func (f *fileSvc) receive(ctx context.Context, t *transfer) {
	defer t.cancel()
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		f.setState(t, "failed", err.Error())
		return
	}
	part := filepath.Join(f.dir, t.info.ID+partSuffix)
	var err error
	for attempt := 0; attempt < receiveRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		if ctx.Err() != nil {
			_ = os.Remove(part)
			f.setState(t, "canceled", "")
			return
		}
		var final string
		final, err = f.fetch(ctx, t, part)
		if err == nil {
			f.mu.Lock()
			t.path, t.info.Path = final, final
			f.mu.Unlock()
			f.setState(t, "done", "")
			f.log.Info("file received", "file", filepath.Base(final), "from", t.info.PeerName)
			return
		}
		if errors.Is(err, errCorrupt) {
			break
		}
		f.log.Debug("file download interrupted; resuming", "file", t.info.Name, "error", err)
	}
	if ctx.Err() != nil {
		_ = os.Remove(part)
		f.setState(t, "canceled", "")
		return
	}
	f.setState(t, "failed", err.Error())
}

var errCorrupt = errors.New("the file arrived damaged (checksum mismatch); ask for it again")

func (f *fileSvc) fetch(ctx context.Context, t *transfer, part string) (string, error) {
	out, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer out.Close()
	st, err := out.Stat()
	if err != nil {
		return "", err
	}
	offset := st.Size()
	if offset > t.info.Size {
		offset = 0
		if err := out.Truncate(0); err != nil {
			return "", err
		}
	}
	h := sha256.New()
	if _, err := io.CopyN(h, out, offset); err != nil {
		return "", err
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	t.done.Store(offset)

	conn, err := fileDial(ctx, t.addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	hs, _ := json.Marshal(handshake{Token: t.token, Offset: offset})
	if _, err := conn.Write(append(hs, '\n')); err != nil {
		return "", err
	}
	if err := f.pump(ctx, t, io.LimitReader(conn, t.info.Size-offset), io.MultiWriter(out, h)); err != nil {
		return "", err
	}
	if t.done.Load() != t.info.Size {
		return "", io.ErrUnexpectedEOF
	}
	var sum [sha256.Size]byte
	_ = conn.SetReadDeadline(time.Now().Add(fileAckTimeout))
	if _, err := io.ReadFull(conn, sum[:]); err != nil {
		return "", err
	}
	if !equalSum(h, sum[:]) {
		_, _ = conn.Write([]byte{'X'})
		_ = out.Close()
		_ = os.Remove(part)
		return "", errCorrupt
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	final := uniquePath(filepath.Join(f.dir, t.info.Name))
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	_, _ = conn.Write([]byte{'K'})
	return final, nil
}

func equalSum(h hash.Hash, want []byte) bool {
	return subtle.ConstantTimeCompare(h.Sum(nil), want) == 1
}

func (f *fileSvc) cancelTransfer(id string) error {
	f.mu.Lock()
	t, ok := f.t[id]
	f.mu.Unlock()
	if !ok {
		return protocol.NewError(protocol.CodeNotFound, "files: no such transfer")
	}
	if t.info.State == "done" {
		return nil
	}
	if t.cancel != nil {
		t.cancel()
	}
	if t.ln != nil {
		_ = t.ln.Close()
	}
	f.signal(t, "cancel")
	f.setState(t, "canceled", "")
	return nil
}

// safeFileName keeps a received name from escaping the Downloads folder or
// colliding with a Windows device name.
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if r := []rune(name); len(r) > 120 {
		ext := filepath.Ext(name)
		name = string(r[:120-len([]rune(ext))]) + ext
	}
	base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	switch base {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		name = "_" + name
	}
	if strings.HasSuffix(strings.ToLower(name), partSuffix) {
		name = "file"
	}
	if name == "" {
		name = "file"
	}
	return name
}

// uniquePath adds " (2)", " (3)"... instead of overwriting.
func uniquePath(p string) string {
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(c); errors.Is(err, os.ErrNotExist) {
			return c
		}
	}
}

// register adds the file methods to the control API.
func (f *fileSvc) register() error {
	decode := func(raw json.RawMessage, v any) error {
		if err := json.Unmarshal(raw, v); err != nil {
			return protocol.NewErrorf(protocol.CodeBadRequest, "bad request: %v", err)
		}
		return nil
	}
	methods := map[string]func(context.Context, json.RawMessage) (any, error){
		protocol.MethodFileOffer: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FileOfferRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			info, err := f.offer(ctx, req)
			if err != nil {
				return nil, err
			}
			return info, nil
		},
		protocol.MethodFileRespond: func(_ context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FileRespondRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			return struct{}{}, f.respond(req)
		},
		protocol.MethodFileCancel: func(_ context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FileRespondRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			return struct{}{}, f.cancelTransfer(req.ID)
		},
		protocol.MethodFileList: func(context.Context, json.RawMessage) (any, error) {
			return f.list(), nil
		},
	}
	for name, fn := range methods {
		if err := f.d.api.Register(name, fn); err != nil {
			return err
		}
	}
	return nil
}
