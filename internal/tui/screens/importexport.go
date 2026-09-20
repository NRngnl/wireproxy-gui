package screens

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// PathPrompt is a small textinput-based component for prompting the user
// for a file path, used by both the import and export flows.
type PathPrompt struct {
	// Message is the prompt label, e.g. "Import from file:" or
	// "Export to file:".
	Message string

	Input textinput.Model
}

// NewPathPrompt constructs a PathPrompt with the given message and initial
// path value (e.g. a suggested export filename).
func NewPathPrompt(message, initialValue string) *PathPrompt {
	ti := textinput.New()
	ti.Placeholder = "/path/to/file.json"
	ti.SetValue(initialValue)
	ti.Focus()

	return &PathPrompt{
		Message: message,
		Input:   ti,
	}
}

// Update forwards a message to the underlying textinput.Model.
func (p *PathPrompt) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.Input, cmd = p.Input.Update(msg)
	return cmd
}

// Submit returns the trimmed path currently entered in the prompt.
func (p *PathPrompt) Submit() (path string) {
	return p.Input.Value()
}

// View renders the prompt.
func (p *PathPrompt) View() string {
	return p.Message + "\n" + p.Input.View()
}

// importSource is the minimal Application surface ImportFromPath needs;
// internal/application.Service satisfies it via its Import method.
type importSource interface {
	Import(fileName string, data []byte) ([]profile.Profile, error)
}

// exportSource is the minimal Application surface ExportToPath needs;
// internal/application.Service satisfies it via its Export method.
type exportSource interface {
	Export(profileIDs []string) ([]byte, error)
}

// ImportFromPath reads path from disk and hands its bytes to
// app.Import(fileName, data), returning the resulting imported profiles.
// fileName is the base name of path, matching the semantics of a file
// picked from a desktop dialog.
func ImportFromPath(app importSource, path string) ([]profile.Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read import file: %w", err)
	}
	return app.Import(filepath.Base(path), data)
}

// ExportToPath calls app.Export(profileIDs) and writes the resulting bytes
// to path. A nil profileIDs slice exports all profiles (see
// application.Service.Export).
func ExportToPath(app exportSource, path string, profileIDs []string) error {
	data, err := app.Export(profileIDs)
	if err != nil {
		return fmt.Errorf("export profiles: %w", err)
	}
	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		return fmt.Errorf("write export file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod export file: %w", err)
	}
	return nil
}
