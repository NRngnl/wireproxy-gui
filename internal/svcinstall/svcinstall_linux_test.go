//go:build linux

package svcinstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/lock"
)

// fakeCall records one invocation of runCommand.
type fakeCall struct {
	name string
	args []string
}

// fakeRunner builds a runCommand replacement that records every
// invocation (in order) and returns a canned response looked up by the
// joined command line, falling back to a default response.
type fakeRunner struct {
	calls    []fakeCall
	handlers map[string]func() ([]byte, error)
	// defaultOut/defaultErr are returned when no handler key matches.
	defaultOut []byte
	defaultErr error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{handlers: make(map[string]func() ([]byte, error))}
}

func (f *fakeRunner) key(name string, args ...string) string {
	return name + " " + strings.Join(args, " ")
}

func (f *fakeRunner) on(name string, out []byte, err error, args ...string) {
	f.handlers[f.key(name, args...)] = func() ([]byte, error) { return out, err }
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	if h, ok := f.handlers[f.key(name, args...)]; ok {
		return h()
	}
	return f.defaultOut, f.defaultErr
}

func (f *fakeRunner) install(t *testing.T) {
	t.Helper()
	orig := runCommand
	runCommand = f.run
	t.Cleanup(func() { runCommand = orig })
}

// exitError builds an *exec.ExitError with the given exit code, the way
// systemctl's boolean-query subcommands report their answer.
func exitError(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit "+itoa(code))
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v (%T)", err, err)
	}
	return exitErr
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf []byte
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Skipf("os.UserHomeDir() does not respect $HOME on this platform (got %q, err %v); skipping", got, err)
	}
	return home
}

func TestInstallCallsDaemonReloadBeforeEnable(t *testing.T) {
	home := setHome(t)
	fr := newFakeRunner()
	fr.install(t)

	daemonPath := filepath.Join(t.TempDir(), "wireproxy-daemon")
	if err := os.WriteFile(daemonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	installer := linuxInstaller{}
	storePath := filepath.Join(t.TempDir(), "profiles.json")
	err := installer.Install(context.Background(), Options{
		StorePath:        storePath,
		DaemonBinaryPath: daemonPath,
	})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	if len(fr.calls) != 2 {
		t.Fatalf("expected 2 systemctl calls, got %d: %+v", len(fr.calls), fr.calls)
	}
	if got := fr.calls[0]; got.name != "systemctl" || strings.Join(got.args, " ") != "--user daemon-reload" {
		t.Fatalf("call[0] = %+v, want systemctl --user daemon-reload", got)
	}
	if got := fr.calls[1]; got.name != "systemctl" || strings.Join(got.args, " ") != "--user enable --now "+unitFileName {
		t.Fatalf("call[1] = %+v, want systemctl --user enable --now %s", got, unitFileName)
	}

	wantPath := filepath.Join(home, ".config", "systemd", "user", unitFileName)
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected unit file at %q: %v", wantPath, err)
	}
}

func TestUninstallNoUnitFileIsNoOp(t *testing.T) {
	setHome(t)
	fr := newFakeRunner()
	fr.install(t)

	installer := linuxInstaller{}
	if err := installer.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error = %v, want nil", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected 0 systemctl calls for no-op uninstall, got %d: %+v", len(fr.calls), fr.calls)
	}
}

func TestUninstallInstalledStopsDisablesRemovesReloads(t *testing.T) {
	home := setHome(t)
	fr := newFakeRunner()
	fr.install(t)

	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, unitFileName)
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	installer := linuxInstaller{}
	if err := installer.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}

	if len(fr.calls) != 3 {
		t.Fatalf("expected 3 systemctl calls, got %d: %+v", len(fr.calls), fr.calls)
	}
	wantOrder := []string{
		"--user stop " + unitFileName,
		"--user disable " + unitFileName,
		"--user daemon-reload",
	}
	for i, want := range wantOrder {
		if got := strings.Join(fr.calls[i].args, " "); got != want {
			t.Fatalf("call[%d] args = %q, want %q", i, got, want)
		}
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected unit file to be removed, stat err = %v", err)
	}
}

func TestStatusMapsEnabledActive(t *testing.T) {
	home := setHome(t)
	fr := newFakeRunner()
	fr.on("systemctl", []byte("enabled\n"), nil, "--user", "is-enabled", unitFileName)
	fr.on("systemctl", []byte("active\n"), nil, "--user", "is-active", unitFileName)
	fr.install(t)

	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, unitFileName)
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	installer := linuxInstaller{}
	status, err := installer.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || !status.Enabled || !status.Running {
		t.Fatalf("Status() = %+v, want Installed/Enabled/Running all true", status)
	}
}

func TestStatusMapsDisabledInactive(t *testing.T) {
	home := setHome(t)
	fr := newFakeRunner()
	fr.on("systemctl", []byte("disabled\n"), exitError(t, 1), "--user", "is-enabled", unitFileName)
	fr.on("systemctl", []byte("inactive\n"), exitError(t, 3), "--user", "is-active", unitFileName)
	fr.install(t)

	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, unitFileName)
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	installer := linuxInstaller{}
	status, err := installer.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed {
		t.Fatal("Status().Installed = false, want true")
	}
	if status.Enabled {
		t.Fatal("Status().Enabled = true, want false")
	}
	if status.Running {
		t.Fatal("Status().Running = true, want false")
	}
}

func TestStatusSystemctlNotFoundIsError(t *testing.T) {
	home := setHome(t)
	fr := newFakeRunner()
	fr.defaultErr = errors.New("exec: \"systemctl\": executable file not found in $PATH")
	fr.install(t)

	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, unitFileName)
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	installer := linuxInstaller{}
	_, err := installer.Status(context.Background())
	if err == nil {
		t.Fatal("Status() error = nil, want a real invocation error when systemctl is not found")
	}
}

func TestInstallReturnsErrLockHeldWithoutInvokingSystemctl(t *testing.T) {
	setHome(t)
	fr := newFakeRunner()
	fr.install(t)

	storePath := filepath.Join(t.TempDir(), "profiles.json")
	heldLock, ok, err := lock.TryLock(storePath + ".lock")
	if err != nil || !ok {
		t.Fatalf("TryLock() ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = heldLock.Release() })

	daemonPath := filepath.Join(t.TempDir(), "wireproxy-daemon")
	if err := os.WriteFile(daemonPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	installer := linuxInstaller{}
	err = installer.Install(context.Background(), Options{
		StorePath:        storePath,
		DaemonBinaryPath: daemonPath,
		Force:            false,
	})
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Install() error = %v, want ErrLockHeld", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("expected 0 systemctl calls when lock is held, got %d: %+v", len(fr.calls), fr.calls)
	}
}
