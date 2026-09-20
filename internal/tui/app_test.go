package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// shutdownTrackingApplication wraps fakeApplication and records whether
// Shutdown was called, for asserting Run's shutdown behavior without
// depending on a real internal/application.Service.
type shutdownTrackingApplication struct {
	*fakeApplication

	shutdownCalled int
	shutdownErr    error
}

func (f *shutdownTrackingApplication) Shutdown(ctx context.Context) error {
	f.shutdownCalled++
	return f.shutdownErr
}

func newShutdownTrackingApplication() *shutdownTrackingApplication {
	return &shutdownTrackingApplication{fakeApplication: newFakeApplication(newTestProfile("alpha", 1080))}
}

// TestRunCallsShutdownOnQuit verifies that run() (the testable core of the
// exported Run) calls core.Shutdown once the Bubble Tea program loop exits
// after a normal quit.
func TestRunCallsShutdownOnQuit(t *testing.T) {
	app := newShutdownTrackingApplication()

	// Feed a single 'q' keypress, which the model's Update maps to
	// tea.Quit, so program.Run() returns promptly without a real TTY.
	input := strings.NewReader("q")

	done := make(chan error, 1)
	go func() {
		done <- run(app, nil, tea.WithInput(input), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithoutRenderer())
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("run() did not return within timeout")
	}

	if app.shutdownCalled != 1 {
		t.Fatalf("expected Shutdown to be called once, got %d calls", app.shutdownCalled)
	}
}

// TestRunCallsShutdownEvenWhenProgramRunErrors verifies that Shutdown is
// still attempted, and its error surfaced, in the (rare) case
// program.Run() itself succeeds but core.Shutdown fails.
func TestRunReturnsShutdownErrorWhenRunSucceeds(t *testing.T) {
	app := newShutdownTrackingApplication()
	app.shutdownErr = errors.New("shutdown boom")

	input := strings.NewReader("q")

	done := make(chan error, 1)
	go func() {
		done <- run(app, nil, tea.WithInput(input), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithoutRenderer())
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("run() did not return within timeout")
	}

	if app.shutdownCalled != 1 {
		t.Fatalf("expected Shutdown to be called once, got %d calls", app.shutdownCalled)
	}
	if err == nil {
		t.Fatalf("expected run() to surface the Shutdown error")
	}
	if !contains(err.Error(), "shutdown boom") {
		t.Fatalf("expected error to mention shutdown failure, got: %v", err)
	}
}
