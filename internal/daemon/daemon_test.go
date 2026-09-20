package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/daemon"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// syncBuffer wraps a bytes.Buffer with a mutex so it can be safely written
// to from the daemon.Run goroutine (via the slog logger) while a test
// goroutine concurrently polls its contents. Using a plain bytes.Buffer for
// this purpose is a data race under go test -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fakeApp struct {
	mu             sync.Mutex
	autoConnectN   int
	autoConnectErr error
	shutdownN      int
	shutdownErr    error
	loadN          int
	loadErr        error
	changes        chan application.Change
}

func newFakeApp() *fakeApp {
	return &fakeApp{changes: make(chan application.Change, 4)}
}

func (f *fakeApp) AutoConnect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.autoConnectN++
	return f.autoConnectErr
}

func (f *fakeApp) Changes() <-chan application.Change {
	return f.changes
}

func (f *fakeApp) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdownN++
	return f.shutdownErr
}

func (f *fakeApp) Profiles() []profile.Profile { return nil }

func (f *fakeApp) Status(profileID string) application.Status { return application.StatusStopped }

func (f *fakeApp) Logs(profileID string) []application.LogEntry { return nil }

func (f *fakeApp) Load() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadN++
	return f.loadErr
}

func (f *fakeApp) autoConnectCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.autoConnectN
}

func (f *fakeApp) shutdownCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdownN
}

func (f *fakeApp) loadCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadN
}

func TestRun_CallsAutoConnectOnce(t *testing.T) {
	app := newFakeApp()
	var buf bytes.Buffer
	logger := daemon.NewLogger(&buf, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, app, nil, logger, daemon.Options{ShutdownTimeout: 200 * time.Millisecond})
	}()

	// Give Run a moment to reach the select loop, then cancel to unblock.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return in time")
	}

	if got := app.autoConnectCalls(); got != 1 {
		t.Fatalf("AutoConnect calls = %d, want 1", got)
	}
}

func TestRun_CancelTriggersExactlyOneShutdown(t *testing.T) {
	app := newFakeApp()
	var buf bytes.Buffer
	logger := daemon.NewLogger(&buf, false)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, app, nil, logger, daemon.Options{ShutdownTimeout: 200 * time.Millisecond})
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return in time")
	}

	if got := app.shutdownCalls(); got != 1 {
		t.Fatalf("Shutdown calls = %d, want 1", got)
	}
}

func TestRun_ChangeProducesLogLine(t *testing.T) {
	app := newFakeApp()
	var buf syncBuffer
	logger := daemon.NewLogger(&buf, false)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, app, nil, logger, daemon.Options{ShutdownTimeout: 200 * time.Millisecond})
	}()

	app.changes <- application.Change{
		ProfileID: "profile-xyz",
		Operation: application.OperationConnect,
	}

	// Wait for the log line to be written.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "profile-xyz") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return in time")
	}

	out := buf.String()
	if !strings.Contains(out, "profile-xyz") {
		t.Fatalf("log output does not contain profile ID, got: %s", out)
	}
	if !strings.Contains(out, "connect") {
		t.Fatalf("log output does not contain operation, got: %s", out)
	}
}

func TestRun_LoadErrDoesNotFailRun(t *testing.T) {
	app := newFakeApp()
	var buf bytes.Buffer
	logger := daemon.NewLogger(&buf, false)

	ctx, cancel := context.WithCancel(context.Background())

	loadErr := errors.New("boom: failed to load store")

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, app, loadErr, logger, daemon.Options{ShutdownTimeout: 200 * time.Millisecond})
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error despite successful shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return in time")
	}

	if got := app.autoConnectCalls(); got != 1 {
		t.Fatalf("AutoConnect calls = %d, want 1 (loadErr should not block continuing)", got)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("expected loadErr to be logged, got: %s", buf.String())
	}
}

func TestRun_HangupTriggersReload(t *testing.T) {
	app := newFakeApp()
	var buf syncBuffer
	logger := daemon.NewLogger(&buf, false)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, app, nil, logger, daemon.Options{ShutdownTimeout: 200 * time.Millisecond})
	}()

	// Give Run a moment to reach the select loop and install its SIGHUP
	// handler before sending the signal.
	time.Sleep(50 * time.Millisecond)

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("failed to send SIGHUP to test process: %v", err)
	}

	// Wait for the reload-triggered AutoConnect (2nd call beyond the
	// initial startup call) and Load call to be observed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if app.loadCalls() >= 1 && app.autoConnectCalls() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return in time")
	}

	if got := app.loadCalls(); got != 1 {
		t.Fatalf("Load calls = %d, want 1 after SIGHUP", got)
	}
	// AutoConnect is called once on startup and once more from the SIGHUP
	// reload handler.
	if got := app.autoConnectCalls(); got != 2 {
		t.Fatalf("AutoConnect calls = %d, want 2 (1 startup + 1 reload)", got)
	}
	if !strings.Contains(buf.String(), "reload requested (SIGHUP)") {
		t.Fatalf("expected SIGHUP reload log line, got: %s", buf.String())
	}
}
