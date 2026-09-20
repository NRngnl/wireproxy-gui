package svcinstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// daemonBinaryName returns the platform-appropriate wireproxy-daemon
// executable filename ("wireproxy-daemon" everywhere except windows,
// where it is "wireproxy-daemon.exe").
func daemonBinaryName() string {
	if runtime.GOOS == "windows" {
		return "wireproxy-daemon.exe"
	}
	return "wireproxy-daemon"
}

// The following package-level function variables are the sole seam
// through which resolveDaemonBinaryPath touches the OS. Tests swap
// these out (restoring the original via t.Cleanup) so every resolution
// branch is exercised without needing a real wireproxy-daemon binary or
// real $PATH manipulation.
var (
	osExecutable         = os.Executable
	filepathEvalSymlinks = filepath.EvalSymlinks
	osStat               = os.Stat
	execLookPath         = exec.LookPath
)

// resolveDaemonBinaryPath implements §7 of
// docs/svcinstall-action-plan.md: locate the wireproxy-daemon binary
// to embed into the generated unit/plist. Resolution order, first hit
// wins:
//
//  1. override, if non-empty (Options.DaemonBinaryPath from the
//     caller). Resolved via filepath.EvalSymlinks for consistency with
//     the other branches, so every returned path is a stable real path.
//  2. A sibling of the calling process's own binary
//     (os.Executable + filepath.EvalSymlinks to defeat a
//     symlink-wrapper launcher, then filepath.Dir), named
//     wireproxy-daemon (wireproxy-daemon.exe on windows). If it exists
//     and is a regular file, filepath.EvalSymlinks is applied to the
//     candidate itself too (e.g. Homebrew-style layouts where the
//     sibling is itself a symlink) before returning it.
//  3. $PATH via exec.LookPath("wireproxy-daemon"), also resolved via
//     filepath.EvalSymlinks.
//  4. Otherwise, ("", error) where errors.Is(err,
//     ErrDaemonBinaryNotFound) is true and the error message names the
//     sibling path that was attempted (if any) and notes that $PATH was
//     searched, so the failure is actionable for a user.
func resolveDaemonBinaryPath(override string) (string, error) {
	if override != "" {
		if resolved, err := filepathEvalSymlinks(override); err == nil {
			return resolved, nil
		}
		return override, nil
	}

	name := daemonBinaryName()
	var siblingAttempted string

	if self, err := osExecutable(); err == nil {
		if realSelf, err := filepathEvalSymlinks(self); err == nil {
			self = realSelf
		}
		sibling := filepath.Join(filepath.Dir(self), name)
		siblingAttempted = sibling
		if info, err := osStat(sibling); err == nil && info.Mode().IsRegular() {
			if resolved, err := filepathEvalSymlinks(sibling); err == nil {
				return resolved, nil
			}
			return sibling, nil
		}
	}

	if found, err := execLookPath(name); err == nil {
		if resolved, err := filepathEvalSymlinks(found); err == nil {
			return resolved, nil
		}
		return found, nil
	}

	if siblingAttempted != "" {
		return "", fmt.Errorf("%w (tried sibling path %q and searched $PATH)", ErrDaemonBinaryNotFound, siblingAttempted)
	}
	return "", fmt.Errorf("%w (could not determine the running executable's own path, so no sibling path was tried; searched $PATH)", ErrDaemonBinaryNotFound)
}
