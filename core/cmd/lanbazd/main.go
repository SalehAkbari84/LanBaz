// Command lanbazd is the LanBaz daemon: it owns the local control API, the
// state file and (from Phase 1 on) the virtual LAN and peer connections.
//
// It runs perfectly well without any UI: `lanbazd` alone is a complete, testable
// server. The Tauri shell is one client among others, and lanbazctl is another.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lanbaz/lanbaz/core/internal/config"
	"github.com/lanbaz/lanbaz/core/internal/daemon"
	"github.com/lanbaz/lanbaz/core/internal/logging"
	"github.com/lanbaz/lanbaz/core/internal/security"
)

// Injected at build time:
//
//	go build -ldflags "-X main.version=1.0.0 -X main.commit=$(git rev-parse HEAD) -X main.buildTime=$(date -u +%FT%TZ)"
//
// They must stay uninitialised: -X only overrides a string variable that is
// empty or set to a constant, so initialising them from DefaultBuildInfo made
// every release report the development version.
var version, commit, buildTime string

func main() {
	if version == "" {
		version = daemon.DefaultBuildInfo.Version
	}
	if commit == "" {
		commit = daemon.DefaultBuildInfo.Commit
	}
	if buildTime == "" {
		buildTime = daemon.DefaultBuildInfo.BuildTime
	}
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("lanbazd", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		configPath = fs.String("config", "", "path to a JSON config file")
		listenHost = fs.String("listen-host", "", "control api host (loopback only, default 127.0.0.1)")
		listenPort = fs.Int("listen-port", -1, "control api port (default 0 = dynamic)")
		logLevel   = fs.String("log-level", "", "trace|debug|info|warn|error")
		stateDir   = fs.String("state-dir", "", "directory for daemon.json and daemon.log")
		apiToken   = fs.String("api-token", "", "use a fixed api token instead of generating one")
		noLogFile  = fs.Bool("no-log-file", false, "do not write daemon.log into the state dir")
		// The network flags exist because the two common failures - no driver and
		// no administrator rights - have completely different fixes, and a user
		// who cannot tell which one they have is stuck.
		netBackend = fs.String("network-backend", "",
			"virtual adapter backend: auto|wintun|memory|off (default auto)")
		netMTU = fs.Int("network-mtu", -1,
			"virtual interface MTU (0 uses the default of 1200)")
		// Set by the desktop shell only. It records this process as a sidecar in
		// the state file, which lets the shell clean up after a forced exit.
		managedByShell = fs.Bool("managed-by-shell", false,
			"mark this process as started by the LanBaz desktop shell (set by the shell)")
		showVer = fs.Bool("version", false, "print version information and exit")
	)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "lanbazd - LanBaz control plane daemon\n\n")
		fmt.Fprintf(os.Stderr, "Usage: lanbazd [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nThe control api is bound to loopback only and authenticates\n")
		fmt.Fprintf(os.Stderr, "every client with the token written to <state-dir>/daemon.json.\n")
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if *showVer {
		fmt.Printf("lanbazd %s (commit %s, built %s)\n", version, commit, buildTime)
		return 0
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}
	cfg.Override("listen-host", *listenHost)
	cfg.Override("listen-port", portFlag(*listenPort))
	cfg.Override("log-level", *logLevel)
	cfg.Override("state-dir", *stateDir)
	cfg.Override("api-token", *apiToken)
	cfg.Override("network-backend", *netBackend)
	cfg.Override("network-mtu", mtuFlag(*netMTU))
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 2
	}

	// The redactor is wired before the first log line so the api token can
	// never leak through a stray %v.
	redactor := security.NewRedactor()
	redactor.Register(cfg.APIToken)

	logFile := cfg.LogFile()
	ring := logging.NewRing(3000)
	logger, err := logging.New(logging.Options{
		Ring:      ring,
		Level:     cfg.LogLevel,
		StateDir:  cfg.StateDir,
		Redactor:  redactor,
		LogToFile: !*noLogFile,
		Stderr:    os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging error: %v\n", err)
		return 2
	}
	defer func() {
		if err := logger.Close(); err != nil {
			slog.Warn("closing log file failed", "error", err)
		}
	}()

	token := cfg.APIToken
	if token == "" {
		token, err = security.GenerateToken()
		if err != nil {
			logger.Error("cannot generate api token", "error", err)
			return 1
		}
		redactor.Register(token)
	}

	d, err := daemon.New(daemon.Options{
		LogRing:        ring,
		Config:         cfg,
		Token:          token,
		Logger:         logger.Logger,
		BuildInfo:      daemon.BuildInfo{Version: version, Commit: commit, BuildTime: buildTime},
		LogToFile:      !*noLogFile,
		ManagedByShell: *managedByShell,
	})
	if err != nil {
		logger.Error("cannot create daemon", "error", err)
		return 1
	}

	// Ctrl+C and SIGTERM trigger the same graceful path as daemon.shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := d.Run(ctx); err != nil {
		logger.Error("daemon stopped with an error", "error", err, "log_file", logFile)
		return 1
	}
	return 0
}

// portFlag converts the -1 sentinel (flag not set) into an empty string so that
// Override does not overwrite the config file value.
func portFlag(p int) string {
	if p < 0 {
		return ""
	}
	return fmt.Sprintf("%d", p)
}

// mtuFlag renders an MTU override. A negative value means the flag was not
// given, which is different from an explicit 0 meaning "use the default", so the
// two spellings must not collapse into each other.
func mtuFlag(m int) string {
	if m < 0 {
		return ""
	}
	return fmt.Sprintf("%d", m)
}
