package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// defaultShutdownTimeout is used when Options.ShutdownTimeout is zero.
const defaultShutdownTimeout = 10 * time.Second

// Application is the narrow port the daemon needs from the application
// service. It intentionally excludes mutating use-case methods (connect,
// save, import, etc.) that the headless daemon does not drive directly.
type Application interface {
	AutoConnect() error
	Changes() <-chan application.Change
	Shutdown(context.Context) error
	Profiles() []profile.Profile
	Status(profileID string) application.Status
	Logs(profileID string) []application.LogEntry
	Load() error
}

// Options configures Run.
type Options struct {
	// ShutdownTimeout bounds core.Shutdown when the run loop is asked to
	// stop. If zero, defaultShutdownTimeout (10s) is used.
	ShutdownTimeout time.Duration
}

// Run executes the headless daemon loop until ctx is canceled or a signal
// requests shutdown, then returns the result of core.Shutdown.
//
// loadErr is the error (possibly nil) returned by the composition root's
// core.Load() call; it is logged as a non-fatal warning so the daemon can
// still run with whatever profiles were successfully loaded, matching the
// GUI's behavior.
func Run(ctx context.Context, core Application, loadErr error, logger *slog.Logger, opts Options) error {
	if loadErr != nil {
		logger.Warn("failed to load profile store", "error", loadErr)
	}

	if err := core.AutoConnect(); err != nil {
		logger.Warn("auto-connect failed", "error", err)
	}

	shutdownTimeout := opts.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultShutdownTimeout
	}

	// signal.NotifyContext fires once and then stops relaying further
	// signals, so it is used only for the terminating signals. SIGHUP is
	// handled by a separate persistent channel below since it must keep
	// triggering reload notifications for the lifetime of the process.
	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()

	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	defer signal.Stop(hangup)

	changes := core.Changes()

	for {
		select {
		case change, ok := <-changes:
			if !ok {
				changes = nil
				continue
			}
			logger.Info("application change",
				"profile_id", change.ProfileID,
				"operation", string(change.Operation),
				"runtime_event", string(change.RuntimeEvent),
				"error", errString(change.Err),
			)
		case <-hangup:
			logger.Info("reload requested (SIGHUP)")
			if err := core.Load(); err != nil {
				logger.Warn("reload: failed to load profile store", "error", err)
			} else {
				logger.Info("reload: profile store reloaded")
			}
			if err := core.AutoConnect(); err != nil {
				logger.Warn("reload: auto-connect failed", "error", err)
			} else {
				logger.Info("reload: auto-connect complete")
			}
		case <-signalCtx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			err := core.Shutdown(shutdownCtx)
			cancel()
			if err != nil {
				logger.Error("shutdown failed", "error", err)
			} else {
				logger.Info("shutdown complete")
			}
			return err
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
