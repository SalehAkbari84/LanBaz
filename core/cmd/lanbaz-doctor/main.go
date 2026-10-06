//go:build windows

// Command lanbaz-doctor tests every layer LanBaz depends on, one at a time,
// and writes a report that says which layer fails:
//
//  1. this PC: rights, driver, adapters, leftovers, firewall, other VPNs
//  2. the Wintun data path: a real adapter, packets out, packets in (firewall)
//  3. the TAP adapters for Classic LAN rooms (read-only)
//  4. the internet: DNS, STUN servers, NAT type, port behaviour, ICE gathering
//  5. WebRTC on this PC: a full link between two local endpoints
//  6. (separate command) a real two-PC test that shows, per path, whether
//     packets from the friend ever arrive: lanbaz-doctor peer host|join
//
// Run it from an administrator window; layer 2 needs it.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const version = "1.1"

func main() {
	peerMode := ""
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "peer" {
		peerMode, args = args[1], args[2:]
	}
	fs := flag.NewFlagSet("lanbaz-doctor", flag.ExitOnError)
	out := fs.String("out", "", "report file (default %LOCALAPPDATA%\\LanBaz\\doctor\\report-<time>.txt)")
	skipAdapter := fs.Bool("skip-adapter", false, "skip the Wintun data path test (layer 2)")
	turn := fs.String("turn", "", "peer test: TURN server as url|user|pass")
	wait := fs.Duration("wait", 120*time.Second, "peer test: how long to watch the connection")
	noPause := fs.Bool("no-pause", false, "do not wait for Enter before exiting")
	_ = fs.Parse(args)

	r, err := newReport(*out, peerMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot write the report:", err)
		os.Exit(1)
	}
	defer r.close()
	r.title(fmt.Sprintf("LanBaz doctor %s — %s", version, time.Now().Format("2006-01-02 15:04:05 -0700")))

	switch peerMode {
	case "":
		runAll(r, *skipAdapter)
	case "host", "join":
		runPeer(r, peerMode, *turn, *wait)
	default:
		fmt.Fprintln(os.Stderr, "usage: lanbaz-doctor [flags] | lanbaz-doctor peer host|join [flags]")
		os.Exit(2)
	}
	r.summary()
	fmt.Println()
	fmt.Println("Report saved to:", r.path)
	if !*noPause {
		fmt.Println("Press Enter to close.")
		_, _ = fmt.Scanln()
	}
}

func runAll(r *report, skipAdapter bool) {
	admin := layerSystem(r)
	if skipAdapter {
		r.section("2. Wintun virtual adapter (data path)")
		r.info("skipped (-skip-adapter)")
	} else {
		layerWintun(r, admin)
	}
	layerTAP(r)
	layerInternet(r)
	layerLocalWebRTC(r)
}

// report writes to the console and to a file at once and keeps score.
type report struct {
	path     string
	f        *os.File
	w        io.Writer
	fails    []string
	warns    []string
	sectionN string
}

func newReport(path, mode string) (*report, error) {
	if path == "" {
		dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "LanBaz", "doctor")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		name := "report"
		if mode != "" {
			name = "peer-" + mode
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%s.txt", name, time.Now().Format("20060102-150405")))
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &report{path: path, f: f, w: io.MultiWriter(os.Stdout, f)}, nil
}

func (r *report) close() { _ = r.f.Close() }

func (r *report) title(s string) {
	fmt.Fprintln(r.w, strings.Repeat("=", 78))
	fmt.Fprintln(r.w, s)
	fmt.Fprintln(r.w, strings.Repeat("=", 78))
}

func (r *report) section(s string) {
	r.sectionN = s
	fmt.Fprintln(r.w)
	fmt.Fprintln(r.w, "## "+s)
}

func (r *report) ok(format string, a ...any) {
	fmt.Fprintf(r.w, "  [ OK ] %s\n", fmt.Sprintf(format, a...))
}
func (r *report) info(format string, a ...any) {
	fmt.Fprintf(r.w, "  [ .. ] %s\n", fmt.Sprintf(format, a...))
}

func (r *report) warn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.warns = append(r.warns, r.sectionN+": "+msg)
	fmt.Fprintf(r.w, "  [WARN] %s\n", msg)
}

func (r *report) fail(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.fails = append(r.fails, r.sectionN+": "+msg)
	fmt.Fprintf(r.w, "  [FAIL] %s\n", msg)
}

// block prints multi-line raw output, indented.
func (r *report) block(s string) {
	s = strings.TrimRight(s, "\r\n ")
	if s == "" {
		return
	}
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		fmt.Fprintln(r.w, "         "+strings.TrimRight(line, " "))
	}
}

func (r *report) summary() {
	r.section("Summary")
	if len(r.fails) == 0 && len(r.warns) == 0 {
		r.ok("every check passed")
		return
	}
	for _, f := range r.fails {
		fmt.Fprintln(r.w, "  FAIL  "+f)
	}
	for _, w := range r.warns {
		fmt.Fprintln(r.w, "  WARN  "+w)
	}
}
