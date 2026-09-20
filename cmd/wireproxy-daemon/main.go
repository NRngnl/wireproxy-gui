// Command wireproxy-daemon is the headless composition root: it wires the
// same application.Service used by the GUI to a daemon.Run loop instead of
// a Fyne UI, with structured logging suitable for systemd/journald.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/daemon"
	"github.com/NRngnl/wireproxy-gui/internal/lock"
	"github.com/NRngnl/wireproxy-gui/internal/profilejson"
	"github.com/NRngnl/wireproxy-gui/internal/runner"
	"github.com/NRngnl/wireproxy-gui/internal/tailscale"
	"github.com/NRngnl/wireproxy-gui/internal/wireproxy"
)

func main() {
	defaultStorePath, err := profilejson.DefaultStorePath()
	if err != nil {
		defaultStorePath = filepath.Join(".", "profiles.json")
	}

	storePath := flag.String("store", defaultStorePath, "path to the profile store JSON file")
	logFile := flag.String("log-file", "", "path to write logs to (default: stdout)")
	jsonLogs := flag.Bool("json-logs", false, "emit logs as JSON lines instead of text")
	flag.Parse()

	logWriter := os.Stdout
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wireproxy-daemon: failed to open log file %q: %v\n", *logFile, err)
			os.Exit(1)
		}
		defer f.Close()
		logger := daemon.NewLogger(f, *jsonLogs)
		runDaemon(*storePath, logger)
		return
	}

	logger := daemon.NewLogger(logWriter, *jsonLogs)
	runDaemon(*storePath, logger)
}

func runDaemon(storePath string, logger *slog.Logger) {
	if err := os.MkdirAll(filepath.Dir(storePath), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "wireproxy-daemon: failed to create profile store directory for %q: %v\n", storePath, err)
		os.Exit(1)
	}

	lockHandle, held, err := lock.TryLock(storePath + ".lock")
	if err != nil {
		fmt.Fprintf(os.Stderr, "wireproxy-daemon: failed to acquire lock for %q: %v\n", storePath, err)
		os.Exit(1)
	}
	if !held {
		fmt.Fprintf(os.Stderr, "wireproxy-daemon: another daemon instance already holds the lock for %s — only one daemon may run per profile store\n", storePath)
		os.Exit(1)
	}
	defer lockHandle.Release()

	runtime := runner.New(wireproxy.NewRunner(), tailscale.NewRunner())
	core := application.New(profilejson.NewRepository(storePath), profilejson.Codec{}, runtime)
	loadErr := core.Load()

	// daemon.Run owns signal handling (SIGTERM/SIGINT/SIGHUP) internally via
	// signal.NotifyContext / signal.Notify, so main passes a plain base
	// context and does not install its own signal handlers.
	ctx := context.Background()

	if err := daemon.Run(ctx, core, loadErr, logger, daemon.Options{}); err != nil {
		fmt.Fprintf(os.Stderr, "wireproxy-daemon: %v\n", err)
		os.Exit(1)
	}
}
