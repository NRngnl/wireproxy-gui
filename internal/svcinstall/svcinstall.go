package svcinstall

import (
	"context"
	"errors"
)

// ErrUnsupported is returned by every method on an Installer for a
// platform without an automated backend (v1: everything except linux
// and darwin).
var ErrUnsupported = errors.New("svcinstall: automated service install is not supported on this OS yet")

// ErrDaemonBinaryNotFound is returned by Install when the wireproxy-daemon
// binary cannot be located by any of the resolution strategies in
// binpath.go.
var ErrDaemonBinaryNotFound = errors.New("svcinstall: could not locate the wireproxy-daemon binary; build or install it first (see docs/daemon.md)")

// ErrLockHeld is returned by Install when the profile store's advisory
// lock (internal/lock) is currently held and Options.Force is false.
// Installing a service that will contend for the same lock at next
// login is confusing to discover only after a reboot, so Install warns
// up front instead.
var ErrLockHeld = errors.New("svcinstall: a wireproxy-daemon (or another process) already holds the profile store lock")

// Status is the machine-checkable outcome of Installer.Status. Frontends
// render it directly; they do not parse service-manager output
// themselves.
type Status struct {
	// Installed reports whether the unit/plist file exists on disk.
	Installed bool
	// Enabled reports whether the service is registered to start at
	// login (systemctl --user is-enabled / the launchd equivalent).
	Enabled bool
	// Running reports whether the service manager currently reports the
	// service as running.
	Running bool
	// UnitPath is the absolute path to the installed unit/plist file,
	// or "" if Installed is false.
	UnitPath string
	// Detail is a human-readable, one-line explanation for display
	// (e.g. "not supported on this OS", or a summarized error from the
	// underlying service manager).
	Detail string
}

// Options customizes Install. The zero value is the common case: no
// path overrides, do not force past a held lock.
type Options struct {
	// StorePath overrides the profile store path baked into the
	// generated unit/plist's --store flag. Defaults to
	// profilejson.DefaultStorePath() when empty.
	StorePath string
	// DaemonBinaryPath overrides binary-path auto-resolution (see
	// binpath.go). Primarily for tests; no UI field exposes this in v1.
	DaemonBinaryPath string
	// Force proceeds with Install even when the profile store lock is
	// currently held, instead of returning ErrLockHeld.
	Force bool
}

// Installer is the platform-agnostic surface both internal/ui and
// internal/tui call. Implementations must be stateless and safe for
// concurrent use.
type Installer interface {
	// Supported reports whether this OS/build has a working backend, so
	// callers can hide or disable the action instead of offering
	// something that always fails.
	Supported() bool

	// Status inspects current install/enable/running state without
	// mutating anything. Implementations must stay fast (a bounded,
	// context-respecting subprocess call plus a file stat, not a slow
	// scan) since callers may invoke it on every menu-open.
	Status(ctx context.Context) (Status, error)

	// Install renders the embedded unit/plist template with the
	// resolved daemon binary path and store path, writes it to the
	// per-user service-manager location, then enables and starts it.
	// Idempotent: re-running after a successful install re-writes the
	// file (picking up e.g. a new binary path after an app move) and
	// re-enables/restarts. Returns ErrLockHeld (without installing) if
	// the profile-store lock is currently held and Options.Force is
	// false.
	Install(ctx context.Context, opts Options) error

	// Uninstall stops the service if running, disables it if enabled,
	// and removes the unit/plist file. A no-op success (not an error)
	// when nothing is installed.
	Uninstall(ctx context.Context) error
}

// New returns the Installer for the current GOOS. On an unsupported OS
// it returns a stub whose Supported reports false and whose Install and
// Uninstall return ErrUnsupported, so callers never need their own
// build tags.
func New() Installer {
	return newPlatformInstaller()
}
