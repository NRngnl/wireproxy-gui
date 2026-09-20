//go:build darwin

// This file provides the darwin (macOS) Installer backend: a per-user
// launchd LaunchAgent registered under ~/Library/LaunchAgents.
package svcinstall

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/profilejson"
)

// launchdLabel is the launchd job label used for this LaunchAgent. It
// must match packaging/launchd/com.github.nrngnl.wireproxy-daemon.plist's
// Label key and the embedded plist template's Label key.
const launchdLabel = "com.github.nrngnl.wireproxy-daemon"

// launchdPlistFileName is the filename written into
// ~/Library/LaunchAgents/.
const launchdPlistFileName = launchdLabel + ".plist"

// darwinInstaller is the launchd-backed Installer for macOS.
type darwinInstaller struct{}

func newPlatformInstaller() Installer {
	return darwinInstaller{}
}

// runCommand is the sole seam through which darwinInstaller touches the
// OS via os/exec. Tests swap this package-level variable (restoring the
// original via t.Cleanup, or scoping it per-test) so every branch is
// exercised without a real launchd session.
var runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// launchAgentsDir returns the per-user LaunchAgents directory
// (~/Library/LaunchAgents).
func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

// logDir returns the per-user log directory
// (~/Library/Logs/wireproxy-daemon) that the rendered plist's
// StandardOutPath/StandardErrorPath point into. launchd does not create
// missing parent directories for those paths itself, so Install must
// create this directory explicitly for logging to work.
func logDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "wireproxy-daemon"), nil
}

func plistPath() (string, error) {
	dir, err := launchAgentsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, launchdPlistFileName), nil
}

func (darwinInstaller) Supported() bool { return true }

// pidPattern matches the `"PID" = <digits>;` line that `launchctl list
// <label>` prints (plist-dictionary-style single-item output) when the
// job is currently running. When the job is loaded but not currently
// running, that single-item output omits the "PID" key entirely (unlike
// the bare, no-argument `launchctl list` table form, which prints a
// literal "-" placeholder in its PID column instead — that table form
// is not what this code parses).
var pidPattern = regexp.MustCompile(`"PID"\s*=\s*(\d+);`)

// Status inspects the on-disk plist and, if present, asks launchd about
// the job's live state via `launchctl list <label>`.
//
// launchctl list <label> was chosen over `launchctl print
// gui/$(id -u)/<label>` because it: (a) is available on considerably
// older macOS releases (print was added later, in the
// launchctl-rewrite era), and (b) prints a simple, well-documented
// plist-dictionary body for a single job that's straightforward to
// regex for a "PID" key, whereas `print`'s output is a much larger,
// less stable free-form dump not intended for scripting. A non-zero
// exit from `launchctl list <label>` means the job is not currently
// loaded into launchd at all (this has been empirically observed and is
// consistent with widely documented launchctl behavior: "Could not find
// specified service" on stderr with a non-zero exit for an unknown/
// unloaded label).
//
// launchd has no separate "enabled but not running" distinction the way
// systemd --user does (there is no persistent on-disk "enabled" bit
// independent of the loaded-in-launchd state): a LaunchAgent is either
// not loaded, loaded-and-not-running, or loaded-and-running. For this
// backend, Enabled is therefore treated as synonymous with "the plist
// is currently loaded into launchd" (launchctl list <label> succeeds at
// all), regardless of whether it happens to be running at the moment.
// This is a real semantic difference from the systemd --user backend's
// Enabled (a persistent, independent "start at login" registration) and
// callers (GUI/TUI) should not assume the two fields mean exactly the
// same thing across platforms.
func (darwinInstaller) Status(ctx context.Context) (Status, error) {
	path, err := plistPath()
	if err != nil {
		return Status{}, err
	}

	st := Status{}
	if _, statErr := os.Stat(path); statErr == nil {
		st.Installed = true
		st.UnitPath = path
	} else if !os.IsNotExist(statErr) {
		return Status{}, statErr
	}

	if !st.Installed {
		st.Detail = "not installed"
		return st, nil
	}

	out, err := runCommand(ctx, "launchctl", "list", launchdLabel)
	if err != nil {
		st.Enabled = false
		st.Running = false
		st.Detail = "installed but not loaded into launchd"
		return st, nil
	}

	st.Enabled = true
	if m := pidPattern.FindSubmatch(out); m != nil {
		if pid, convErr := strconv.Atoi(string(m[1])); convErr == nil && pid > 0 {
			st.Running = true
			st.Detail = fmt.Sprintf("loaded and running (pid %d)", pid)
		}
	}
	if !st.Running {
		st.Detail = "loaded but not running"
	}
	return st, nil
}

// Install renders the launchd plist and (re)loads it. Idempotent:
// re-running after a successful install re-writes the plist (picking up
// e.g. a new binary path) and reloads it so launchd picks up the
// change — `launchctl load` on an already-loaded label can silently
// no-op, so an already-loaded job is unloaded first.
func (darwinInstaller) Install(ctx context.Context, opts Options) error {
	storePath := opts.StorePath
	if storePath == "" {
		defaultPath, err := profilejson.DefaultStorePath()
		if err != nil {
			return err
		}
		storePath = defaultPath
	}

	if err := checkLockBeforeInstall(storePath, opts.Force); err != nil {
		return err
	}

	binaryPath, err := resolveDaemonBinaryPath(opts.DaemonBinaryPath)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	rendered, err := RenderLaunchdPlist(RenderInput{
		DaemonBinaryPath: binaryPath,
		StorePath:        storePath,
		HomeDir:          home,
	})
	if err != nil {
		return err
	}

	agentsDir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}

	logsDir, err := logDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}

	path := filepath.Join(agentsDir, launchdPlistFileName)

	alreadyLoaded := false
	if _, statErr := os.Stat(path); statErr == nil {
		alreadyLoaded = true
	}

	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		return err
	}

	if alreadyLoaded {
		// A re-install (plist already present) must unload the
		// previously-loaded job first: `launchctl load` on an
		// already-loaded label can silently no-op and not pick up a
		// changed binary path. Tolerate "not actually loaded" failures
		// (e.g. the plist file existed on disk but the job had already
		// been unloaded out of band) since that leaves us in exactly
		// the state we want (unloaded) anyway; propagate any other
		// genuine failure such as launchctl not being found.
		if out, unloadErr := runCommand(ctx, "launchctl", "unload", path); unloadErr != nil && !isNotLoadedOutput(out) {
			return fmt.Errorf("launchctl unload %s: %w (output: %s)", path, unloadErr, strings.TrimSpace(string(out)))
		}
	}

	if out, err := runCommand(ctx, "launchctl", "load", path); err != nil {
		return fmt.Errorf("launchctl load %s: %w (output: %s)", path, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// Uninstall unloads the job (tolerating an already-unloaded job) and
// removes the plist file. It never removes the log directory or its
// contents (Non-Goal: uninstalling the service is not "delete my
// logs"). It is idempotent: called with no plist present, it returns
// nil without invoking launchctl at all.
func (darwinInstaller) Uninstall(ctx context.Context) error {
	path, err := plistPath()
	if err != nil {
		return err
	}

	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return statErr
	}

	out, err := runCommand(ctx, "launchctl", "unload", path)
	if err != nil && !isNotLoadedOutput(out) {
		return fmt.Errorf("launchctl unload %s: %w (output: %s)", path, err, strings.TrimSpace(string(out)))
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// notLoadedPhrases are substrings observed (and widely documented) in
// launchctl's stderr/combined output when asked to unload a label that
// is not currently loaded into launchd at all. launchctl's exit code in
// this case is non-zero, but this is the expected "nothing to do" case
// for an idempotent Uninstall, not a genuine invocation failure, so it
// is tolerated as success.
var notLoadedPhrases = []string{
	"could not find specified service",
	"no such process",
	"not loaded",
}

// isNotLoadedOutput reports whether combined launchctl output matches a
// known "job was not loaded" phrase.
func isNotLoadedOutput(out []byte) bool {
	if out == nil {
		return false
	}
	lower := strings.ToLower(string(out))
	for _, phrase := range notLoadedPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}
