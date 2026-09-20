// Package screens contains the individual screen/pane implementations used
// by the TUI root model (internal/tui).
package screens

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// Status colors mirror the tray icon hex values in internal/ui/app.go.
var (
	colorConnected    = lipgloss.Color("#34c759")
	colorDisconnected = lipgloss.Color("#8e8e93")
	colorConnecting   = lipgloss.Color("#ff9500")
	colorError        = lipgloss.Color("#ff3b30")
)

var (
	styleConnected    = lipgloss.NewStyle().Foreground(colorConnected)
	styleDisconnected = lipgloss.NewStyle().Foreground(colorDisconnected)
	styleConnecting   = lipgloss.NewStyle().Foreground(colorConnecting)
	styleError        = lipgloss.NewStyle().Foreground(colorError)

	styleNormalTitle = lipgloss.NewStyle().PaddingLeft(1)
	styleNormalDesc  = lipgloss.NewStyle().PaddingLeft(1).Foreground(lipgloss.Color("240"))
	styleSelected    = lipgloss.NewStyle().PaddingLeft(0).Bold(true).Foreground(lipgloss.Color("#ffffff"))
)

// StatusGlyph returns the status glyph and rendering style for a profile's
// application.Status, matching the color vocabulary used by the tray icons
// in internal/ui/app.go: green connected/running, gray
// disconnected/stopped, amber connecting/starting/disconnecting/stopping,
// red error.
func StatusGlyph(status application.Status) (string, lipgloss.Style) {
	switch status {
	case application.StatusRunning:
		return "●", styleConnected
	case application.StatusStopped:
		return "○", styleDisconnected
	case application.StatusStarting, application.StatusStopping:
		return "◐", styleConnecting
	case application.StatusError:
		return "!", styleError
	default:
		return "○", styleDisconnected
	}
}

// ProfileItem adapts a profile.Profile plus its current status into a
// list.Item for the profile sidebar.
type ProfileItem struct {
	Profile profile.Profile
	Status  application.Status
}

// FilterValue implements list.Item.
func (i ProfileItem) FilterValue() string {
	return i.Profile.Name
}

// Title returns the display title for the item: a status glyph followed by
// the profile name.
func (i ProfileItem) Title() string {
	glyph, style := StatusGlyph(i.Status)
	return style.Render(glyph) + " " + i.Profile.Name
}

// Description returns the SOCKS5 bind address shown under the title.
func (i ProfileItem) Description() string {
	return i.Profile.BindAddress()
}

// ID returns the profile ID, used by the root model to track selection.
func (i ProfileItem) ID() string {
	return i.Profile.ID
}

// listDelegate renders ProfileItem values in the bubbles/list widget.
type listDelegate struct{}

func (d listDelegate) Height() int                         { return 2 }
func (d listDelegate) Spacing() int                        { return 1 }
func (d listDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

// Render implements list.ItemDelegate.
func (d listDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(ProfileItem)
	if !ok {
		return
	}

	title := item.Title()
	desc := styleNormalDesc.Render(item.Description())

	if index == m.Index() {
		title = styleSelected.Render("> " + strings.TrimPrefix(title, " "))
	} else {
		title = styleNormalTitle.Render(title)
	}

	fmt.Fprintf(w, "%s\n%s", title, desc)
}

// NewList constructs a bubbles/list.Model configured for the profile
// sidebar, populated from the given profiles and a status lookup function.
func NewList(profiles []profile.Profile, statusOf func(string) application.Status, width, height int) list.Model {
	items := make([]list.Item, 0, len(profiles))
	for _, item := range profiles {
		items = append(items, ProfileItem{Profile: item, Status: statusOf(item.ID)})
	}

	l := list.New(items, listDelegate{}, width, height)
	l.Title = "Profiles"
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	return l
}
