package svcinstall

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// swapSeams overrides the package-level OS seams for the duration of a
// test and restores the originals via t.Cleanup.
func swapSeams(t *testing.T, executable func() (string, error), evalSymlinks func(string) (string, error), stat func(string) (fs.FileInfo, error), lookPath func(string) (string, error)) {
	t.Helper()
	origExecutable := osExecutable
	origEvalSymlinks := filepathEvalSymlinks
	origStat := osStat
	origLookPath := execLookPath
	t.Cleanup(func() {
		osExecutable = origExecutable
		filepathEvalSymlinks = origEvalSymlinks
		osStat = origStat
		execLookPath = origLookPath
	})
	osExecutable = executable
	filepathEvalSymlinks = evalSymlinks
	osStat = stat
	execLookPath = lookPath
}

// fakeFileInfo is a minimal fs.FileInfo for a regular file, sufficient
// for the Mode().IsRegular() check in resolveDaemonBinaryPath.
type fakeFileInfo struct {
	name string
	dir  bool
}

func (f fakeFileInfo) Name() string { return f.name }
func (f fakeFileInfo) Size() int64  { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir
	}
	return 0
}
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

func alwaysFailExecutable() (string, error) {
	return "", errors.New("no executable in this fake environment")
}

func identityEvalSymlinks(p string) (string, error) {
	return p, nil
}

func alwaysFailStat(string) (fs.FileInfo, error) {
	return nil, fs.ErrNotExist
}

func alwaysFailLookPath(string) (string, error) {
	return "", exec.ErrNotFound
}

// 1. Override provided and non-empty -> returned as-is, symlink-resolved
// for consistency with the other resolution branches (see binpath.go's
// doc comment on resolveDaemonBinaryPath).
func TestResolveDaemonBinaryPath_Override(t *testing.T) {
	swapSeams(t, alwaysFailExecutable, identityEvalSymlinks, alwaysFailStat, alwaysFailLookPath)

	got, err := resolveDaemonBinaryPath("/custom/path/wireproxy-daemon")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/custom/path/wireproxy-daemon" {
		t.Fatalf("got %q, want %q", got, "/custom/path/wireproxy-daemon")
	}
}

// 2. No override; osExecutable succeeds; a sibling wireproxy-daemon
// exists next to it -> returns the sibling path (symlink-resolved).
func TestResolveDaemonBinaryPath_Sibling(t *testing.T) {
	name := daemonBinaryName()
	wantSibling := filepath.Join("/opt/wireproxy-gui", name)

	swapSeams(t,
		func() (string, error) { return "/opt/wireproxy-gui/wireproxy-gui", nil },
		identityEvalSymlinks,
		func(p string) (fs.FileInfo, error) {
			if p == wantSibling {
				return fakeFileInfo{name: name}, nil
			}
			return nil, fs.ErrNotExist
		},
		alwaysFailLookPath,
	)

	got, err := resolveDaemonBinaryPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantSibling {
		t.Fatalf("got %q, want %q", got, wantSibling)
	}
}

// 3. No override; osExecutable succeeds; no sibling exists; execLookPath
// succeeds -> returns the PATH-found path.
func TestResolveDaemonBinaryPath_Path(t *testing.T) {
	name := daemonBinaryName()
	wantPathHit := "/usr/local/bin/" + name

	swapSeams(t,
		func() (string, error) { return "/opt/wireproxy-gui/wireproxy-gui", nil },
		identityEvalSymlinks,
		alwaysFailStat,
		func(file string) (string, error) {
			if file == name {
				return wantPathHit, nil
			}
			return "", exec.ErrNotFound
		},
	)

	got, err := resolveDaemonBinaryPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantPathHit {
		t.Fatalf("got %q, want %q", got, wantPathHit)
	}
}

// 4. No override; nothing found anywhere -> ("", err) with
// errors.Is(err, ErrDaemonBinaryNotFound), and the message mentions both
// the attempted sibling path and that PATH was searched.
func TestResolveDaemonBinaryPath_NotFound(t *testing.T) {
	swapSeams(t,
		func() (string, error) { return "/opt/wireproxy-gui/wireproxy-gui", nil },
		identityEvalSymlinks,
		alwaysFailStat,
		alwaysFailLookPath,
	)

	got, err := resolveDaemonBinaryPath("")
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
	if !errors.Is(err, ErrDaemonBinaryNotFound) {
		t.Fatalf("errors.Is(err, ErrDaemonBinaryNotFound) = false, err = %v", err)
	}
	name := daemonBinaryName()
	wantSibling := filepath.Join("/opt/wireproxy-gui", name)
	msg := err.Error()
	if !strings.Contains(msg, wantSibling) {
		t.Errorf("error message %q does not mention attempted sibling path %q", msg, wantSibling)
	}
	if !strings.Contains(strings.ToUpper(msg), "PATH") {
		t.Errorf("error message %q does not mention PATH being searched", msg)
	}
}

// 4b. Same as above but osExecutable itself fails, so there is no
// sibling path to attempt; the error must still satisfy errors.Is and
// must not falsely claim a sibling path was tried.
func TestResolveDaemonBinaryPath_NotFound_NoSelfExecutable(t *testing.T) {
	swapSeams(t, alwaysFailExecutable, identityEvalSymlinks, alwaysFailStat, alwaysFailLookPath)

	got, err := resolveDaemonBinaryPath("")
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
	if !errors.Is(err, ErrDaemonBinaryNotFound) {
		t.Fatalf("errors.Is(err, ErrDaemonBinaryNotFound) = false, err = %v", err)
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "PATH") {
		t.Errorf("error message %q does not mention PATH being searched", err.Error())
	}
}

// 5. A real filesystem symlink case: resolveDaemonBinaryPath must
// return the resolved real path, not the symlink path, when the
// sibling-lookup candidate is itself a symlink. Uses the real
// filepath.EvalSymlinks (not mocked) to test actual end-to-end
// resolution behavior on a real filesystem.
func TestResolveDaemonBinaryPath_SymlinkResolution(t *testing.T) {
	dir := t.TempDir()
	name := daemonBinaryName()

	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	realDaemon := filepath.Join(realDir, name)
	if err := os.WriteFile(realDaemon, []byte("fake daemon binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// binDir is where the "self" executable and the sibling symlink
	// live; the symlink named wireproxy-daemon points at the real
	// binary in a different directory.
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	selfPath := filepath.Join(binDir, "wireproxy-gui")
	if err := os.WriteFile(selfPath, []byte("fake self binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	siblingSymlink := filepath.Join(binDir, name)
	if err := os.Symlink(realDaemon, siblingSymlink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	wantReal, err := filepath.EvalSymlinks(realDaemon)
	if err != nil {
		t.Fatalf("EvalSymlinks(realDaemon): %v", err)
	}

	swapSeams(t,
		func() (string, error) { return selfPath, nil },
		filepath.EvalSymlinks, // real seam: this test exercises actual symlink resolution.
		os.Stat,
		alwaysFailLookPath,
	)

	got, err := resolveDaemonBinaryPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantReal {
		t.Fatalf("got %q, want resolved real path %q", got, wantReal)
	}
	if got == siblingSymlink {
		t.Fatalf("resolveDaemonBinaryPath returned the symlink path %q instead of resolving it", got)
	}
}

// 5b. Same real-symlink resolution behavior, but via the override
// branch: an override pointing at a symlink is resolved to the real
// path.
func TestResolveDaemonBinaryPath_SymlinkResolution_Override(t *testing.T) {
	dir := t.TempDir()

	realFile := filepath.Join(dir, "real-wireproxy-daemon")
	if err := os.WriteFile(realFile, []byte("fake daemon binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	symlinkPath := filepath.Join(dir, "wireproxy-daemon-link")
	if err := os.Symlink(realFile, symlinkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	wantReal, err := filepath.EvalSymlinks(realFile)
	if err != nil {
		t.Fatalf("EvalSymlinks(realFile): %v", err)
	}

	swapSeams(t, alwaysFailExecutable, filepath.EvalSymlinks, os.Stat, alwaysFailLookPath)

	got, err := resolveDaemonBinaryPath(symlinkPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wantReal {
		t.Fatalf("got %q, want resolved real path %q", got, wantReal)
	}
}
