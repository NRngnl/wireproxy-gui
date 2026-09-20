//go:build linux

// This file provides the linux Installer backend, driving a per-user
// systemd instance (systemctl --user) as described in
// docs/svcinstall-action-plan.md.
package svcinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/profilejson"
)

// unitFileName is the systemd --user unit name installed and managed by
// linuxInstaller.
const unitFileName = "wireproxy-daemon.service"

// linuxInstaller is the Installer backend for linux, using
// `systemctl --user` against a per-user unit file.
type linuxInstaller struct{}

func newPlatformInstaller() Installer {
	return linuxInstaller{}
}

// runCommand is the sole seam through which linuxInstaller touches the
// OS to invoke systemctl. Tests swap this with a fake that records
// invocations and returns canned output/errors, so no test ever talks
// to a real systemd.
var runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// unitDir returns the per-user systemd unit directory
// (~/.config/systemd/user).
func unitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// unitPath returns the absolute path to the managed unit file.
func unitPath() (string, error) {
	dir, err := unitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, unitFileName), nil
}

func (linuxInstaller) Supported() bool { return true }

func (linuxInstaller) Status(ctx context.Context) (Status, error) {
	path, err := unitPath()
	if err != nil {
		return Status{Detail: err.Error()}, err
	}

	status := Status{}
	if _, statErr := os.Stat(path); statErr == nil {
		status.Installed = true
		status.UnitPath = path
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Status{Detail: statErr.Error()}, statErr
	}

	if !status.Installed {
		status.Detail = "not installed"
		return status, nil
	}

	enabled, enabledErr := systemctlQueryState(ctx, "is-enabled")
	if enabledErr != nil {
		return Status{Detail: enabledErr.Error()}, enabledErr
	}
	status.Enabled = enabled

	running, runningErr := systemctlQueryState(ctx, "is-active")
	if runningErr != nil {
		return Status{Detail: runningErr.Error()}, runningErr
	}
	status.Running = running

	switch {
	case status.Enabled && status.Running:
		status.Detail = "installed, enabled, and running"
	case status.Enabled && !status.Running:
		status.Detail = "installed and enabled, but not running"
	case !status.Enabled && status.Running:
		status.Detail = "installed and running, but not enabled at login"
	default:
		status.Detail = "installed, but not enabled or running"
	}
	return status, nil
}

// systemctlQueryState runs `systemctl --user <subcommand> wireproxy-daemon.service`
// for a boolean-outcome subcommand (is-enabled, is-active). systemctl's
// own convention is a non-zero exit for a negative-but-successful query
// (e.g. "disabled", "inactive", "not-found"), so a non-zero exit is only
// escalated to a Go error when it looks like the systemctl invocation
// itself failed (e.g. the binary was not found on $PATH).
func systemctlQueryState(ctx context.Context, subcommand string) (bool, error) {
	out, err := runCommand(ctx, "systemctl", "--user", subcommand, unitFileName)
	trimmed := strings.TrimSpace(string(out))
	if err == nil {
		return trimmed == "enabled" || trimmed == "active", nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A recognized negative-but-successful query result: not an
		// error, just false.
		switch trimmed {
		case "disabled", "inactive", "failed", "not-found", "unknown", "":
			return false, nil
		}
		return false, nil
	}

	// Something other than a nonzero exit (binary not found, context
	// cancellation, etc.) is a genuine invocation failure.
	return false, fmt.Errorf("systemctl --user %s %s: %w", subcommand, unitFileName, err)
}

func (linuxInstaller) Install(ctx context.Context, opts Options) error {
	storePath := opts.StorePath
	if storePath == "" {
		resolved, err := profilejson.DefaultStorePath()
		if err != nil {
			return err
		}
		storePath = resolved
	}

	if err := checkLockBeforeInstall(storePath, opts.Force); err != nil {
		return err
	}

	daemonBinaryPath, err := resolveDaemonBinaryPath(opts.DaemonBinaryPath)
	if err != nil {
		return err
	}

	rendered, err := RenderSystemdUnit(RenderInput{
		DaemonBinaryPath: daemonBinaryPath,
		StorePath:        storePath,
	})
	if err != nil {
		return err
	}

	dir, err := unitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("svcinstall: creating unit directory %q: %w", dir, err)
	}

	path := filepath.Join(dir, unitFileName)
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return fmt.Errorf("svcinstall: writing unit file %q: %w", path, err)
	}

	if _, err := runCommand(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("svcinstall: systemctl --user daemon-reload: %w", err)
	}
	if _, err := runCommand(ctx, "systemctl", "--user", "enable", "--now", unitFileName); err != nil {
		return fmt.Errorf("svcinstall: systemctl --user enable --now %s: %w", unitFileName, err)
	}

	return nil
}

func (linuxInstaller) Uninstall(ctx context.Context) error {
	path, err := unitPath()
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		return nil
	} else if statErr != nil {
		return statErr
	}

	if err := systemctlTolerant(ctx, "stop"); err != nil {
		return err
	}
	if err := systemctlTolerant(ctx, "disable"); err != nil {
		return err
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("svcinstall: removing unit file %q: %w", path, err)
	}

	if _, err := runCommand(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("svcinstall: systemctl --user daemon-reload: %w", err)
	}

	return nil
}

// systemctlTolerant runs `systemctl --user <subcommand> wireproxy-daemon.service`
// for a mutating subcommand (stop, disable) during Uninstall, tolerating
// "already stopped"/"already disabled" outcomes as success. Per
// systemctl(1)/systemd.service(5), `systemctl stop` on a unit that is
// already inactive, and `systemctl disable` on a unit that is already
// disabled, both exit 0 (these are idempotent state-setting operations,
// unlike the boolean-query subcommands is-enabled/is-active, which use
// their exit code to report the queried state itself). A nonzero exit
// here therefore indicates a genuine invocation failure (systemctl not
// found, permission denied, unit file invalid, etc.) and is propagated.
func systemctlTolerant(ctx context.Context, subcommand string) error {
	if _, err := runCommand(ctx, "systemctl", "--user", subcommand, unitFileName); err != nil {
		return fmt.Errorf("svcinstall: systemctl --user %s %s: %w", subcommand, unitFileName, err)
	}
	return nil
}
