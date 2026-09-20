package screens

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Confirm is a minimal, reusable yes/no confirmation prompt used for
// destructive or disruptive actions (delete profile, logout, quit while
// connected). It does not depend on bubbles/huh: pressing 'y' or 'Y'
// confirms, any other key (including esc) cancels.
type Confirm struct {
	// Message is the prompt text shown to the user, e.g.
	// "Delete profile \"foo\"? (y/N)".
	Message string

	// resolved and confirmed record the outcome once a key has been
	// handled; Resolved reports whether HandleKey has produced an answer.
	resolved  bool
	confirmed bool
}

// NewConfirm constructs a Confirm prompt with the given message.
func NewConfirm(message string) *Confirm {
	return &Confirm{Message: message}
}

// HandleKey processes a key press and returns whether the prompt has been
// resolved and, if so, whether it was confirmed. Once resolved, the
// Confirm's state is fixed; callers should discard it after reading the
// result.
func (c *Confirm) HandleKey(msg tea.KeyMsg) (resolved bool, confirmed bool) {
	switch msg.String() {
	case "y", "Y":
		c.resolved = true
		c.confirmed = true
	default:
		c.resolved = true
		c.confirmed = false
	}
	return c.resolved, c.confirmed
}

// Resolved reports whether a key has already been handled.
func (c *Confirm) Resolved() bool {
	return c.resolved
}

// Confirmed reports the resolved answer; only meaningful once Resolved()
// is true.
func (c *Confirm) Confirmed() bool {
	return c.confirmed
}

// View renders the confirmation prompt.
func (c *Confirm) View() string {
	return c.Message + " (y/N)"
}
