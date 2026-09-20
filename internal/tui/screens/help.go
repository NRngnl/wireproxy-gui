package screens

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
)

// Bindings is the standard bubbles/help.KeyMap interface. The root tui
// package's keyMap type (internal/tui/keymap.go) satisfies this via
// additive ShortHelp()/FullHelp() methods, letting model.go pass its keys
// value into FullHelpView without screens importing the tui package (which
// would create an import cycle).
type Bindings interface {
	ShortHelp() []key.Binding
	FullHelp() [][]key.Binding
}

// FullHelpView renders the full keybinding help table for the given
// bindings, wrapped to width.
func FullHelpView(bindings Bindings, width int) string {
	h := help.New()
	h.Width = width
	h.ShowAll = true
	return h.FullHelpView(bindings.FullHelp())
}

// ShortHelpView renders the abbreviated single-line keybinding help for the
// given bindings, wrapped to width.
func ShortHelpView(bindings Bindings, width int) string {
	h := help.New()
	h.Width = width
	h.ShowAll = false
	return h.ShortHelpView(bindings.ShortHelp())
}
