package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap declares every key binding used by the TUI. Task 1 wires the
// navigation and quit bindings; later tasks wire the remaining actions
// without changing this file's structure.
type keyMap struct {
	Up   key.Binding
	Down key.Binding
	Quit key.Binding

	Connect       key.Binding
	ConnectAll    key.Binding
	Disconnect    key.Binding
	DisconnectAll key.Binding
	Add           key.Binding
	Edit          key.Binding
	Delete        key.Binding
	New           key.Binding
	Login         key.Binding
	Logout        key.Binding
	Import        key.Binding
	Export        key.Binding
	GoTop         key.Binding
	GoBottom      key.Binding
	Search        key.Binding
	Tab           key.Binding
	Help          key.Binding
	Escape        key.Binding
	Service       key.Binding
}

// keys is the single instance of keyMap used throughout the TUI.
var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),

	Connect: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "connect"),
	),
	ConnectAll: key.NewBinding(
		key.WithKeys("C"),
		key.WithHelp("C", "connect all"),
	),
	Disconnect: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "disconnect"),
	),
	DisconnectAll: key.NewBinding(
		key.WithKeys("D"),
		key.WithHelp("D", "disconnect all"),
	),
	Add: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "add"),
	),
	Edit: key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "edit"),
	),
	Delete: key.NewBinding(
		key.WithKeys("x"),
		key.WithHelp("x", "delete"),
	),
	New: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new"),
	),
	Login: key.NewBinding(
		key.WithKeys("L"),
		key.WithHelp("L", "login"),
	),
	Logout: key.NewBinding(
		key.WithKeys("O"),
		key.WithHelp("O", "logout"),
	),
	Import: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "import"),
	),
	Export: key.NewBinding(
		key.WithKeys("E"),
		key.WithHelp("E", "export"),
	),
	GoTop: key.NewBinding(
		key.WithKeys("g"),
		key.WithHelp("g", "top"),
	),
	GoBottom: key.NewBinding(
		key.WithKeys("G"),
		key.WithHelp("G", "bottom"),
	),
	Search: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "search"),
	),
	Tab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch pane"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	),
	Service: key.NewBinding(
		key.WithKeys("S"),
		key.WithHelp("S", "service"),
	),
}

// ShortHelp implements the bubbles/help.KeyMap interface (also mirrored as
// screens.Bindings so internal/tui/screens/help.go can render this keyMap
// without importing the tui package). Added for Task 5; do not change any
// existing field or binding above this point.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Connect, k.Add, k.Edit, k.Delete, k.Help, k.Quit}
}

// FullHelp implements the bubbles/help.KeyMap interface.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.GoTop, k.GoBottom, k.Search, k.Tab},
		{k.Connect, k.ConnectAll, k.Disconnect, k.DisconnectAll},
		{k.Add, k.Edit, k.Delete, k.New},
		{k.Login, k.Logout, k.Import, k.Export},
		{k.Help, k.Escape, k.Quit, k.Service},
	}
}
