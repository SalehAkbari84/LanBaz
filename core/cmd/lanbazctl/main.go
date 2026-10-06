// Command lanbazctl is the LanBaz command line client.
//
// It discovers a running daemon through the state file in the state directory,
// authenticates with the token stored there and speaks the same control API as
// the desktop UI. The command table is the extension point: adding a subcommand
// in a later phase means adding one entry to commands() and one function.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/config"
	"github.com/lanbaz/lanbaz/core/internal/security"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// command is one CLI verb. Adding a subcommand in a later phase means adding an
// entry here and nothing else.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, env *env, args []string) error
}

// commands is the CLI verb table. It is a function rather than a package level
// variable because the entries reference the run functions, and a var
// initialized from functions that read the var is an initialization cycle in Go.
func commands() []command {
	return []command{
		{name: "daemon", summary: "daemon lifecycle commands", run: runDaemon},
		{name: "help", summary: "show this help", run: runHelp},
		{name: "network", summary: "inspect the virtual LAN", run: runNetwork},
		{name: "peer", summary: "list, inspect, ping and kick peers", run: runPeer},
		{name: "room", summary: "create, join and leave rooms", run: runRoom},
		{name: "version", summary: "print the lanbazctl version", run: runVersion},
	}
}

// newFlagSet builds a subcommand flag set that reports errors through the normal
// channel rather than exiting, so a bad flag produces a BAD_REQUEST the CLI can
// render as JSON instead of a usage dump on stderr.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

type env struct {
	stateDir string
	asJSON   bool
	timeout  time.Duration
}

func run(args []string) int {
	fs := flag.NewFlagSet("lanbazctl", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "directory containing daemon.json")
	asJSON := fs.Bool("json", false, "print machine readable json")
	timeout := fs.Duration("timeout", 5*time.Second, "api call timeout")
	fs.Usage = func() { printUsage(os.Stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	e := &env{
		stateDir: *stateDir,
		asJSON:   *asJSON,
		timeout:  *timeout,
	}
	if e.stateDir == "" {
		e.stateDir = config.DefaultStateDir()
	}

	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(os.Stderr)
		return 2
	}

	name := rest[0]
	for _, c := range commands() {
		if c.name == name {
			budget := e.timeout + 5*time.Second
			if longRunning(name) && budget < roomCallTimeout {
				budget = roomCallTimeout
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if err := c.run(ctx, e, rest[1:]); err != nil {
				reportError(e, err)
				return exitCodeFor(err)
			}
			return 0
		}
	}

	fmt.Fprintf(os.Stderr, "lanbazctl: unknown command %q\n\n", name)
	printUsage(os.Stderr)
	return 2
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "lanbazctl - LanBaz control client")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: lanbazctl [global flags] <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --state-dir <dir>   directory containing daemon.json (default: platform state dir)")
	fmt.Fprintln(w, "  --json              print machine readable json")
	fmt.Fprintln(w, "  --timeout <dur>     api call timeout (default 5s)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  lanbazctl daemon status")
	fmt.Fprintln(w, "  lanbazctl --json daemon status")
	fmt.Fprintln(w, "  lanbazctl daemon shutdown")
	fmt.Fprintln(w, "  lanbazctl room create --name \"Match Night\"")
	fmt.Fprintln(w, "  lanbazctl room join LBZ-xxxx-xxxx-...")
	fmt.Fprintln(w, "  lanbazctl room accept LBZ-yyyy-yyyy-...")
	fmt.Fprintln(w, "  lanbazctl peer ping <peer-id>")
	fmt.Fprintln(w, "  lanbazctl network status")
	fmt.Fprintln(w, "  lanbazctl network peers --room lbzroom-xxxxxxxx")
}

// ---------------------------------------------------------------- commands --

func runHelp(ctx context.Context, e *env, args []string) error {
	printUsage(os.Stdout)
	return nil
}

func runVersion(ctx context.Context, e *env, args []string) error {
	if e.asJSON {
		return printJSON(map[string]string{"lanbazctl": "0.1.0-dev", "protocol": protocol.VersionString})
	}
	fmt.Printf("lanbazctl %s (protocol %s)\n", "0.1.0-dev", protocol.VersionString)
	return nil
}

func runDaemon(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return protocol.NewError(protocol.CodeBadRequest, "usage: lanbazctl daemon <status|version|shutdown>")
	}
	switch args[0] {
	case "status":
		return daemonStatus(ctx, e)
	case "version":
		return daemonVersion(ctx, e)
	case "shutdown":
		return daemonShutdown(ctx, e)
	default:
		return protocol.NewErrorf(protocol.CodeBadRequest,
			"unknown daemon subcommand %q (want status|version|shutdown)", args[0])
	}
}

func daemonStatus(ctx context.Context, e *env) error {
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var st protocol.DaemonStatus
	if err := client.Call(ctx, protocol.MethodDaemonStatus, nil, &st); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(st)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "State:\t%s\n", st.State)
	fmt.Fprintf(w, "Version:\t%s (%s)\n", st.Version, st.Commit)
	fmt.Fprintf(w, "Protocol:\t%d\n", st.ProtocolVersion)
	fmt.Fprintf(w, "PID:\t%d\n", st.PID)
	fmt.Fprintf(w, "Uptime:\t%s\n", time.Duration(st.UptimeSeconds*float64(time.Second)).Round(time.Second))
	fmt.Fprintf(w, "API:\t%s%s\n", st.APIListen, protocol.APIPath)
	fmt.Fprintf(w, "State dir:\t%s\n", st.StateDir)
	fmt.Fprintf(w, "Log level:\t%s\n", st.LogLevel)
	fmt.Fprintf(w, "Clients:\t%d\n", st.ActiveClients)
	fmt.Fprintf(w, "Rooms:\t%d\n", st.RoomCount)
	fmt.Fprintf(w, "Peers:\t%d\n", st.PeerCount)
	return w.Flush()
}

func daemonVersion(ctx context.Context, e *env) error {
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var v protocol.DaemonVersion
	if err := client.Call(ctx, protocol.MethodDaemonVersion, nil, &v); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(v)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Daemon:\t%s\n", v.Daemon)
	fmt.Fprintf(w, "Version:\t%s\n", v.Version)
	fmt.Fprintf(w, "Commit:\t%s\n", v.Commit)
	fmt.Fprintf(w, "Built:\t%s\n", v.BuildTime)
	fmt.Fprintf(w, "Go:\t%s\n", v.GoVersion)
	fmt.Fprintf(w, "Protocol:\t%d (%s)\n", v.ProtocolVersion, protocol.VersionString)
	fmt.Fprintf(w, "Platform:\t%s\n", v.Platform)
	fmt.Fprintf(w, "Phase:\t%s\n", v.Phase)
	return w.Flush()
}

func daemonShutdown(ctx context.Context, e *env) error {
	client, err := dial(ctx, e)
	if err != nil {
		return err
	}
	defer client.Close()

	var resp protocol.ShutdownResponse
	if err := client.Call(ctx, protocol.MethodDaemonShutdown, protocol.ShutdownRequest{
		Reason: "lanbazctl shutdown",
	}, &resp); err != nil {
		return err
	}
	if e.asJSON {
		return printJSON(resp)
	}
	if resp.Accepted {
		fmt.Printf("daemon accepted shutdown, stopping within %dms\n", resp.GracePeriod)
	} else {
		fmt.Println("daemon did not accept the shutdown request")
	}
	return nil
}

// ----------------------------------------------------------------- helpers --

func dial(ctx context.Context, e *env) (*protocol.Client, error) {
	state, err := security.ReadStateFile(e.stateDir)
	if err != nil {
		return nil, err
	}
	if !security.ProcessAlive(state.PID) {
		return nil, protocol.NewErrorf(protocol.CodeNotFound,
			"state file in %s references pid %d which is not running; delete %s if it is stale",
			filepath.Join(e.stateDir, security.StateFileName), state.PID,
			filepath.Join(e.stateDir, security.StateFileName))
	}
	callCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	return protocol.Dial(callCtx, protocol.DialOptions{
		Addr:       net_JoinHostPort(state),
		Token:      state.APIToken,
		Timeout:    e.timeout,
		ClientName: "lanbazctl",
	})
}

// net_JoinHostPort prefers the recorded listen address so that a daemon bound
// to ::1 is reached on the right stack.
func net_JoinHostPort(state security.State) string {
	listen := strings.TrimSpace(state.APIListen)
	if listen != "" {
		return listen
	}
	return fmt.Sprintf("127.0.0.1:%d", state.APIPort)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func reportError(e *env, err error) {
	if e.asJSON {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"success": false,
			"error":   map[string]string{"code": errorCode(err), "message": err.Error()},
		})
		return
	}
	fmt.Fprintf(os.Stderr, "lanbazctl: %s\n", err)
}

func errorCode(err error) string {
	var apiErr *protocol.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return protocol.CodeInternal
}

func exitCodeFor(err error) int {
	switch errorCode(err) {
	case protocol.CodeNotFound, protocol.CodeUnauthorized:
		return 1
	case protocol.CodeBadRequest, protocol.CodeConfigInvalid:
		return 2
	default:
		return 1
	}
}

// sortedCommandNames is used by the help output and by tests that assert the
// command surface.
func sortedCommandNames() []string {
	names := make([]string, 0, len(commands()))
	for _, c := range commands() {
		names = append(names, c.name)
	}
	sort.Strings(names)
	return names
}

var _ = sortedCommandNames
