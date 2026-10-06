//go:build windows

package main

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/logging"
	pion "github.com/pion/webrtc/v4"

	"github.com/lanbaz/lanbaz/core/internal/settings"
	lbrtc "github.com/lanbaz/lanbaz/core/internal/transport/webrtc"
)

// The two-PC test: the same kind of link LanBaz builds, but with every
// candidate pair's traffic counted, so the result says *which direction* is
// blocked instead of just "could not connect".

const codePrefix = "LBZDOC1:"

type peerCode struct {
	Role string `json:"role"` // "invite" or "reply"
	SDP  string `json:"sdp"`
	Name string `json:"name"`
}

func encodeCode(c peerCode) string {
	raw, _ := json.Marshal(c)
	var b bytes.Buffer
	w, _ := flate.NewWriter(&b, flate.BestCompression)
	_, _ = w.Write(raw)
	_ = w.Close()
	return codePrefix + base64.RawURLEncoding.EncodeToString(b.Bytes())
}

func decodeCode(s string) (peerCode, error) {
	s = strings.Join(strings.Fields(s), "")
	i := strings.Index(s, codePrefix)
	if i < 0 {
		return peerCode{}, fmt.Errorf("this is not a doctor code (it must start with %s)", codePrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[i+len(codePrefix):])
	if err != nil {
		return peerCode{}, fmt.Errorf("the code is damaged: %v", err)
	}
	data, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return peerCode{}, fmt.Errorf("the code is damaged: %v", err)
	}
	var c peerCode
	err = json.Unmarshal(data, &c)
	return c, err
}

func setClipboard(s string) error {
	f, err := os.CreateTemp("", "lanbaz-doctor-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, _ = f.WriteString(s)
	_ = f.Close()
	_, err = ps(`Get-Content -Raw -LiteralPath '`+f.Name()+`' | Set-Clipboard`, 0)
	return err
}

func getClipboard() string {
	out, _ := ps(`Get-Clipboard -Raw`, 0)
	return out
}

// readCode asks the user to copy a code and press Enter, then reads it from
// the clipboard (a console line cannot hold a code this long reliably).
func readCode(in *bufio.Reader, what, role string) peerCode {
	for {
		fmt.Printf("\n>>> Copy the %s you received (Ctrl+C on the whole text), then press Enter here...", what)
		_, _ = in.ReadString('\n')
		c, err := decodeCode(getClipboard())
		if err != nil {
			fmt.Println("    ", err, "- try again.")
			continue
		}
		if c.Role != role {
			fmt.Printf("     this is a %s, but a %s is needed - try again.\n", c.Role, role)
			continue
		}
		return c
	}
}

func runPeer(r *report, mode, turn string, wait time.Duration) {
	r.section("6. Two-PC test (" + mode + ")")
	name, _ := os.Hostname()
	r.info("this PC: %s", name)

	dir := filepath.Dir(r.path)
	iceLog, _ := os.Create(filepath.Join(dir, fmt.Sprintf("peer-%s-ice-%s.log", mode, time.Now().Format("150405"))))
	if iceLog != nil {
		defer iceLog.Close()
		r.info("detailed ICE log: %s", iceLog.Name())
	}

	se := pion.SettingEngine{}
	se.SetInterfaceFilter(func(n string) bool {
		return !strings.HasPrefix(n, "LanBaz") && !strings.HasPrefix(n, "vEthernet")
	})
	se.SetICETimeouts(5*time.Second, 0, time.Second)
	stopKeep := make(chan struct{})
	defer close(stopKeep)
	if n, err := lbrtc.KeepAliveNet(settings.DefaultSTUN, stopKeep); err == nil {
		se.SetNet(n)
	}
	se.SetICEMaxBindingRequests(65535)
	if iceLog != nil {
		lf := logging.NewDefaultLoggerFactory()
		lf.Writer = iceLog
		lf.DefaultLogLevel = logging.LogLevelInfo
		lf.ScopeLevels["ice"] = logging.LogLevelTrace
		se.LoggerFactory = lf
	}
	api := pion.NewAPI(pion.WithSettingEngine(se))

	var servers []pion.ICEServer
	for _, s := range settings.DefaultSTUN {
		servers = append(servers, pion.ICEServer{URLs: []string{s}})
	}
	if turn != "" {
		p := strings.SplitN(turn, "|", 3)
		for len(p) < 3 {
			p = append(p, "")
		}
		servers = append(servers, pion.ICEServer{URLs: []string{p[0]}, Username: p[1], Credential: p[2]})
		r.info("TURN relay: %s", p[0])
	}
	pc, err := api.NewPeerConnection(pion.Configuration{ICEServers: servers})
	if err != nil {
		r.fail("%v", err)
		return
	}
	defer pc.Close()

	var (
		mu        sync.Mutex
		connected time.Time
		rtts      []time.Duration
		events    []string
	)
	start := time.Now()
	logEvent := func(format string, a ...any) {
		line := fmt.Sprintf("+%5.1fs  ", time.Since(start).Seconds()) + fmt.Sprintf(format, a...)
		mu.Lock()
		events = append(events, line)
		mu.Unlock()
		fmt.Println("    " + line)
	}
	pc.OnICEConnectionStateChange(func(s pion.ICEConnectionState) {
		logEvent("ICE %s", s)
		if s == pion.ICEConnectionStateConnected {
			mu.Lock()
			if connected.IsZero() {
				connected = time.Now()
			}
			mu.Unlock()
		}
	})
	pc.OnConnectionStateChange(func(s pion.PeerConnectionState) { logEvent("link %s", s) })
	useChannel := func(dc *pion.DataChannel) {
		dc.OnOpen(func() {
			logEvent("data channel open - the two PCs are CONNECTED")
			go func() {
				for i := 0; i < 20; i++ {
					if dc.ReadyState() != pion.DataChannelStateOpen {
						return
					}
					_ = dc.SendText("ping " + strconv.FormatInt(time.Now().UnixNano(), 10))
					time.Sleep(500 * time.Millisecond)
				}
			}()
		})
		dc.OnMessage(func(m pion.DataChannelMessage) {
			s := string(m.Data)
			switch {
			case strings.HasPrefix(s, "ping "):
				_ = dc.SendText("pong " + strings.TrimPrefix(s, "ping "))
			case strings.HasPrefix(s, "pong "):
				if ns, err := strconv.ParseInt(strings.TrimPrefix(s, "pong "), 10, 64); err == nil {
					mu.Lock()
					rtts = append(rtts, time.Since(time.Unix(0, ns)))
					mu.Unlock()
				}
			}
		})
	}

	in := bufio.NewReader(os.Stdin)
	var remote peerCode
	if mode == "host" {
		dc, err := pc.CreateDataChannel("doctor", nil)
		if err != nil {
			r.fail("%v", err)
			return
		}
		useChannel(dc)
		code, err := makeLocal(pc, true)
		if err != nil {
			r.fail("%v", err)
			return
		}
		code.Name = name
		share(r, dir, "invite", encodeCode(code))
		remote = readCode(in, "REPLY from your friend", "reply")
		if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: remote.SDP}); err != nil {
			r.fail("the reply is not usable: %v", err)
			return
		}
		logEvent("reply from %s applied; testing paths", remote.Name)
	} else {
		pc.OnDataChannel(useChannel)
		remote = readCode(in, "INVITE from your friend", "invite")
		if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: remote.SDP}); err != nil {
			r.fail("the invite is not usable: %v", err)
			return
		}
		code, err := makeLocal(pc, false)
		if err != nil {
			r.fail("%v", err)
			return
		}
		code.Name = name
		share(r, dir, "reply", encodeCode(code))
		logEvent("reply created; waiting for %s to apply it", remote.Name)
	}
	r.info("friend's PC: %s", remote.Name)
	r.info("friend's addresses (from the code):")
	r.block(candidateLines(remote.SDP))

	// Watch.
	deadline := time.Now().Add(wait)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		mu.Lock()
		done := !connected.IsZero() && time.Since(connected) > 12*time.Second
		mu.Unlock()
		if done {
			break
		}
		select {
		case <-tick.C:
			fmt.Println("    paths so far:")
			fmt.Println(indent(pairTable(pc), "      "))
		case <-time.After(time.Second):
		}
	}

	r.info("timeline:")
	mu.Lock()
	r.block(strings.Join(events, "\n"))
	mu.Unlock()
	r.info("every path tried (sent/answered = this PC's checks; recv = the friend's checks that ARRIVED here):")
	r.block(pairTable(pc))
	verdict(r, pc, &mu, connected, rtts)
}

// makeLocal creates the offer or answer and waits for candidates.
func makeLocal(pc *pion.PeerConnection, offer bool) (peerCode, error) {
	var (
		sd  pion.SessionDescription
		err error
	)
	if offer {
		sd, err = pc.CreateOffer(nil)
	} else {
		sd, err = pc.CreateAnswer(nil)
	}
	if err != nil {
		return peerCode{}, err
	}
	done := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(sd); err != nil {
		return peerCode{}, err
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	role := "reply"
	if offer {
		role = "invite"
	}
	return peerCode{Role: role, SDP: pc.LocalDescription().SDP}, nil
}

func share(r *report, dir, what, code string) {
	file := filepath.Join(dir, "doctor-"+what+".txt")
	_ = os.WriteFile(file, []byte(code), 0o644)
	clip := "it is on your clipboard"
	if err := setClipboard(code); err != nil {
		clip = "copy it from the file"
	}
	fmt.Println()
	fmt.Println("=================================================================")
	fmt.Printf(" Your %s (%d characters) is ready: %s.\n", strings.ToUpper(what), len(code), clip)
	fmt.Println(" Saved too:", file)
	fmt.Println(" Send it to your friend now (Telegram, Discord, ...).")
	fmt.Println("=================================================================")
	r.info("%s created (%d characters), saved to %s", what, len(code), file)
	r.info("this PC's addresses in the %s:", what)
	if c, err := decodeCode(code); err == nil {
		r.block(candidateLines(c.SDP))
	}
}

// candidateLines lists the candidates in an SDP, one per line.
func candidateLines(sdp string) string {
	var out []string
	for _, l := range strings.Split(sdp, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "a=candidate:") {
			continue
		}
		f := strings.Fields(l)
		if len(f) >= 8 {
			out = append(out, fmt.Sprintf("%-6s %s %s:%s", f[7], f[2], f[4], f[5]))
		}
	}
	if len(out) == 0 {
		return "(no candidates)"
	}
	return strings.Join(out, "\n")
}

type pairRow struct {
	local, remote                  string
	state                          string
	nominated                      bool
	sent, answered, recv, respSent uint64
	rtt                            float64
}

func pairs(pc *pion.PeerConnection) []pairRow {
	st := pc.GetStats()
	cands := map[string]string{}
	for _, s := range st {
		if c, ok := s.(pion.ICECandidateStats); ok {
			cands[c.ID] = fmt.Sprintf("%s %s:%d", c.CandidateType, c.IP, c.Port)
		}
	}
	var rows []pairRow
	for _, s := range st {
		p, ok := s.(pion.ICECandidatePairStats)
		if !ok {
			continue
		}
		rows = append(rows, pairRow{
			local: cands[p.LocalCandidateID], remote: cands[p.RemoteCandidateID],
			state: string(p.State), nominated: p.Nominated,
			sent: p.RequestsSent, answered: p.ResponsesReceived, recv: p.RequestsReceived, respSent: p.ResponsesSent,
			rtt: p.CurrentRoundTripTime,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].recv+rows[i].answered != rows[j].recv+rows[j].answered {
			return rows[i].recv+rows[i].answered > rows[j].recv+rows[j].answered
		}
		return rows[i].local+rows[i].remote < rows[j].local+rows[j].remote
	})
	return rows
}

func pairTable(pc *pion.PeerConnection) string {
	rows := pairs(pc)
	if len(rows) == 0 {
		return "(no paths yet - the other side's code has not been applied)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-30s %-30s %-11s %5s %5s %5s\n", "this PC", "friend", "state", "sent", "answ", "recv")
	for i, p := range rows {
		if i == 25 {
			fmt.Fprintf(&b, "... %d more\n", len(rows)-25)
			break
		}
		mark := ""
		if p.nominated {
			mark = " *selected*"
		}
		fmt.Fprintf(&b, "%-30s %-30s %-11s %5d %5d %5d%s\n", p.local, p.remote, p.state, p.sent, p.answered, p.recv, mark)
	}
	return b.String()
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+pre)
}

func verdict(r *report, pc *pion.PeerConnection, mu *sync.Mutex, connected time.Time, rtts []time.Duration) {
	r.section("Verdict")
	mu.Lock()
	ok := !connected.IsZero()
	rt := append([]time.Duration(nil), rtts...)
	mu.Unlock()
	rows := pairs(pc)
	var sent, answered, recv uint64
	for _, p := range rows {
		sent += p.sent
		answered += p.answered
		recv += p.recv
	}
	if ok {
		for _, p := range rows {
			if p.nominated && p.state == "succeeded" {
				r.ok("CONNECTED through %s  <->  %s", p.local, p.remote)
			}
		}
		if len(rt) > 0 {
			sort.Slice(rt, func(i, j int) bool { return rt[i] < rt[j] })
			r.ok("%d round trips: min %s, median %s, max %s", len(rt), rt[0].Round(time.Millisecond),
				rt[len(rt)/2].Round(time.Millisecond), rt[len(rt)-1].Round(time.Millisecond))
		}
		r.ok("the internet path between the two PCs WORKS; if LanBaz still fails, the problem is in LanBaz itself - send both reports")
		return
	}
	switch {
	case len(rows) == 0:
		r.fail("no path was ever tried: the friend's code was never applied on this side")
	case recv == 0 && answered == 0:
		r.fail("NOTHING from the friend ever arrived here (%d checks sent, 0 answered, 0 received). Either the friend did not run the test at the same time, or UDP from the friend is dropped before it reaches this PC (this PC's NAT/firewall), or the friend's packets never leave their network", sent)
	case recv > 0 && answered == 0:
		r.fail("the friend's checks DO arrive here (%d), but none of this PC's %d checks were answered: packets from this PC do not get through to the friend - the FRIEND's NAT/firewall drops them (or they leave from a different public address)", recv, sent)
	case answered > 0:
		r.fail("checks went both ways (%d answered, %d received) but the link never became usable - likely a DTLS/firewall problem after ICE; send the ICE log", answered, recv)
	}
	r.info("compare with the friend's report: the side whose 'recv' stays 0 is the side whose network blocks incoming packets")
}
