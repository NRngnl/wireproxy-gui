// Package tui implements a terminal user interface frontend for the
// Wireproxy GUI application core. It mirrors the composition role of
// internal/ui but renders with Bubble Tea instead of Fyne.
package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// shutdownWaitTimeout bounds how long Run waits for core.Shutdown to finish
// stopping running profiles once the Bubble Tea program loop exits,
// matching internal/ui/app.go's shutdown() pattern.
const shutdownWaitTimeout = 5 * time.Second

// Application is the inbound port the TUI depends on. It mirrors
// ui.Application's method set so internal/application.Service satisfies both
// presentation adapters without modification.
type Application interface {
	Changes() <-chan application.Change
	Profiles() []profile.Profile
	Profile(string) (profile.Profile, bool)
	Status(string) application.Status
	Logs(string) []application.LogEntry
	RuntimeLocked(string) bool
	Add(string) (profile.Profile, error)
	Save(profile.Profile) (application.SaveResult, error)
	Delete(string) error
	Import(string, []byte) ([]profile.Profile, error)
	Export([]string) ([]byte, error)
	Connect(string) error
	Login(string) error
	ConnectAll() error
	AutoConnect() error
	Disconnect(string) bool
	DisconnectFromTray(string) bool
	DisconnectAll()
	ExitNodes(context.Context, string) ([]connection.ExitNode, error)
	Logout(context.Context, string) error
	Shutdown(context.Context) error
}

// changeMsg wraps an application.Change delivered from core.Changes() so it
// can flow through the Bubble Tea message loop.
type changeMsg struct {
	change application.Change
}

// Run constructs and runs the Bubble Tea program for the terminal UI. It
// blocks until the program exits and returns any error the program loop
// surfaces.
func Run(core Application, loadErr error) error {
	return run(core, loadErr, tea.WithAltScreen())
}

// run is the testable core of Run: it accepts explicit tea.ProgramOptions so
// tests can supply non-TTY input/output and avoid blocking on the real
// terminal, while Run's public signature stays unchanged for callers.
// Regardless of whether program.Run() itself errors, run always attempts
// core.Shutdown before returning, matching internal/ui/app.go's shutdown()
// pattern: run.Run()'s error takes priority as the returned error when both
// fail, but Shutdown is never skipped just because Run() errored.
func run(core Application, loadErr error, opts ...tea.ProgramOption) error {
	model := newModel(core, loadErr)
	program := tea.NewProgram(model, opts...)
	model.program = program

	changes := core.Changes()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for change := range changes {
			program.Send(changeMsg{change: change})
		}
	}()

	_, runErr := program.Run()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownWaitTimeout)
	shutdownErr := core.Shutdown(shutdownCtx)
	cancel()

	if runErr != nil {
		return runErr
	}
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	return nil
}
