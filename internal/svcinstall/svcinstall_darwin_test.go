//go:build darwin

package svcinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/lock"
)

// recordedCall captures one fake runCommand invocation for assertions.
type recordedCall struct {
	name string
	args []string
}

// swapRunCommand installs a fake runCommand for the duration of a test,
// restoring the original via t.Cleanup, and returns a pointer to the
// slice of calls recorded so far (appended to as the fake is invoked).
func swapRunCommand(t *testing.T, fn func(ctx context.Context, name string, args ...string) ([]byte, error)) *[]recordedCall {
	t.Helper()
	orig := runCommand
	t.Cleanup(func() { runCommand = orig })

	calls := &[]recordedCall{}
	runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, recordedCall{name: name, args: append([]string(nil), args...)})
		return fn(ctx, name, args...)
	}
	return calls
}

// setFakeHome redirects os.UserHomeDir() (via $HOME on darwin) to a
// fresh temp directory for the duration of a test.
func setFakeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func expectedPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdPlistFileName)
}

func expectedLogDir(home string) string {
	return filepath.Join(home, "Library", "Logs", "wireproxy-daemon")
}

func TestDarwinInstall_FreshTarget_WritesPlistAndCreatesLogDir(t *testing.T) {
	home := setFakeHome(t)
	calls := swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	})

	inst := darwinInstaller{}
	storePath := filepath.Join(t.TempDir(), "profiles.json")
	err := inst.Install(context.Background(), Options{
		StorePath:        storePath,
		DaemonBinaryPath: filepath.Join(t.TempDir(), "wireproxy-daemon"),
	})
	if err != nil {
		t.Fatalf("Install() = %v, want nil", err)
	}

	plistPath := expectedPlistPath(home)
	if _, statErr := os.Stat(plistPath); statErr != nil {
		t.Fatalf("expected plist at %s: %v", plistPath, statErr)
	}
	if _, statErr := os.Stat(expectedLogDir(home)); statErr != nil {
		t.Fatalf("expected log dir %s to be created: %v", expectedLogDir(home), statErr)
	}

	if len(*calls) != 1 || (*calls)[0].name != "launchctl" || (*calls)[0].args[0] != "load" {
		t.Fatalf("expected a single launchctl load call on a fresh install, got %+v", *calls)
	}
}

func TestDarwinInstall_FreshTarget_DoesNotUnloadFirst(t *testing.T) {
	setFakeHome(t)
	calls := swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	})

	inst := darwinInstaller{}
	err := inst.Install(context.Background(), Options{
		StorePath:        filepath.Join(t.TempDir(), "profiles.json"),
		DaemonBinaryPath: filepath.Join(t.TempDir(), "wireproxy-daemon"),
	})
	if err != nil {
		t.Fatalf("Install() = %v, want nil", err)
	}

	for _, c := range *calls {
		if len(c.args) > 0 && c.args[0] == "unload" {
			t.Fatalf("fresh install should not call unload, got calls %+v", *calls)
		}
	}
}

func TestDarwinInstall_AlreadyInstalledTarget_UnloadsThenLoads(t *testing.T) {
	home := setFakeHome(t)

	// Pre-create the plist file so Install sees an already-installed
	// target.
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(expectedPlistPath(home), []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	calls := swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	})

	inst := darwinInstaller{}
	err := inst.Install(context.Background(), Options{
		StorePath:        filepath.Join(t.TempDir(), "profiles.json"),
		DaemonBinaryPath: filepath.Join(t.TempDir(), "wireproxy-daemon"),
	})
	if err != nil {
		t.Fatalf("Install() = %v, want nil", err)
	}

	if len(*calls) != 2 {
		t.Fatalf("expected exactly 2 launchctl calls (unload, load), got %+v", *calls)
	}
	if (*calls)[0].args[0] != "unload" {
		t.Fatalf("expected first call to be unload, got %+v", (*calls)[0])
	}
	if (*calls)[1].args[0] != "load" {
		t.Fatalf("expected second call to be load, got %+v", (*calls)[1])
	}
}

func TestDarwinUninstall_NotInstalled_NoOpNoCalls(t *testing.T) {
	setFakeHome(t)
	calls := swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("runCommand should not be invoked when nothing is installed")
		return nil, nil
	})

	inst := darwinInstaller{}
	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() = %v, want nil", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("expected zero runCommand calls, got %+v", *calls)
	}
}

func TestDarwinUninstall_Installed_UnloadsThenRemoves_KeepsLogDir(t *testing.T) {
	home := setFakeHome(t)

	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plistPath := expectedPlistPath(home)
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logDirPath := expectedLogDir(home)
	if err := os.MkdirAll(logDirPath, 0o755); err != nil {
		t.Fatalf("MkdirAll log dir: %v", err)
	}
	logFile := filepath.Join(logDirPath, "stdout.log")
	if err := os.WriteFile(logFile, []byte("some log data"), 0o644); err != nil {
		t.Fatalf("WriteFile log: %v", err)
	}

	calls := swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	})

	inst := darwinInstaller{}
	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() = %v, want nil", err)
	}

	if len(*calls) != 1 || (*calls)[0].args[0] != "unload" {
		t.Fatalf("expected a single unload call, got %+v", *calls)
	}

	if _, statErr := os.Stat(plistPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected plist to be removed, stat err = %v", statErr)
	}

	if _, statErr := os.Stat(logFile); statErr != nil {
		t.Fatalf("expected log file to survive uninstall, stat err = %v", statErr)
	}
}

func TestDarwinUninstall_ToleratesAlreadyUnloaded(t *testing.T) {
	home := setFakeHome(t)

	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plistPath := expectedPlistPath(home)
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Could not find specified service"), errors.New("exit status 113")
	})

	inst := darwinInstaller{}
	if err := inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() = %v, want nil (already-unloaded job tolerated)", err)
	}
	if _, statErr := os.Stat(plistPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected plist to be removed even when unload reports already-unloaded, stat err = %v", statErr)
	}
}

func TestDarwinStatus_LoadedAndRunning(t *testing.T) {
	home := setFakeHome(t)
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(expectedPlistPath(home), []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{
	"Label" = "com.github.nrngnl.wireproxy-daemon";
	"LastExitStatus" = 0;
	"PID" = 4242;
};`), nil
	})

	st, err := darwinInstaller{}.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() = %v, want nil", err)
	}
	if !st.Installed {
		t.Error("expected Installed = true")
	}
	if !st.Enabled {
		t.Error("expected Enabled = true (loaded)")
	}
	if !st.Running {
		t.Error("expected Running = true (has PID)")
	}
}

func TestDarwinStatus_LoadedNotRunning(t *testing.T) {
	home := setFakeHome(t)
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(expectedPlistPath(home), []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{
	"Label" = "com.github.nrngnl.wireproxy-daemon";
	"LastExitStatus" = 0;
};`), nil
	})

	st, err := darwinInstaller{}.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() = %v, want nil", err)
	}
	if !st.Installed {
		t.Error("expected Installed = true")
	}
	if !st.Enabled {
		t.Error("expected Enabled = true (loaded)")
	}
	if st.Running {
		t.Error("expected Running = false (no PID key)")
	}
}

func TestDarwinStatus_NotLoadedAtAll(t *testing.T) {
	home := setFakeHome(t)
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(expectedPlistPath(home), []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("Could not find specified service"), errors.New("exit status 113")
	})

	st, err := darwinInstaller{}.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() = %v, want nil", err)
	}
	if !st.Installed {
		t.Error("expected Installed = true (plist file exists on disk)")
	}
	if st.Enabled {
		t.Error("expected Enabled = false (not loaded)")
	}
	if st.Running {
		t.Error("expected Running = false (not loaded)")
	}
}

func TestDarwinStatus_NotInstalled(t *testing.T) {
	setFakeHome(t)
	swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("runCommand should not be invoked when the plist is absent")
		return nil, nil
	})

	st, err := darwinInstaller{}.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() = %v, want nil", err)
	}
	if st.Installed {
		t.Error("expected Installed = false")
	}
}

func TestDarwinInstall_ErrLockHeld_NoRunCommandInvoked(t *testing.T) {
	setFakeHome(t)
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")

	l, ok, err := lock.TryLock(storePath + ".lock")
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !ok {
		t.Fatal("expected to acquire lock in test setup")
	}
	defer l.Release()

	calls := swapRunCommand(t, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("runCommand should not be invoked when the lock is held and Force is false")
		return nil, nil
	})

	inst := darwinInstaller{}
	err = inst.Install(context.Background(), Options{
		StorePath:        storePath,
		DaemonBinaryPath: filepath.Join(t.TempDir(), "wireproxy-daemon"),
	})
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("Install() = %v, want ErrLockHeld", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("expected zero runCommand calls, got %+v", *calls)
	}
}
