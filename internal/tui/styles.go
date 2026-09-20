package tui

import "github.com/charmbracelet/lipgloss"

// Status colors mirror the hex values used for the tray icons in
// internal/ui/app.go, so the TUI and desktop GUI read as the same product.
const (
	colorConnected     = lipgloss.Color("#34c759")
	colorDisconnected  = lipgloss.Color("#8e8e93")
	colorConnecting    = lipgloss.Color("#ff9500")
	colorDisconnecting = lipgloss.Color("#ff9f0a")
	colorError         = lipgloss.Color("#ff3b30")
)

// Lipgloss picks up NO_COLOR and terminal capability detection natively via
// termenv; styles here intentionally avoid forcing a color profile.
var (
	appStyle = lipgloss.NewStyle().
			Padding(0, 1)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Padding(0, 1)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder())

	sidebarStyle = borderStyle

	statusBarStyle = lipgloss.NewStyle().
			Padding(0, 1)

	errorBannerStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#ffffff")).
				Background(colorError).
				Padding(0, 1)

	emptyStateStyle = lipgloss.NewStyle().
			Padding(1, 2).
			Foreground(lipgloss.Color("240"))

	statusConnectedStyle    = lipgloss.NewStyle().Foreground(colorConnected)
	statusDisconnectedStyle = lipgloss.NewStyle().Foreground(colorDisconnected)
	statusConnectingStyle   = lipgloss.NewStyle().Foreground(colorConnecting)
	statusErrorStyle        = lipgloss.NewStyle().Foreground(colorError)

	selectedItemStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#ffffff"))
)

// Detail pane and log viewport styles added for Task 4.
var (
	detailStyle = borderStyle.Copy()

	logViewportStyle = borderStyle.Copy()

	detailLabelStyle = lipgloss.NewStyle().
				Bold(true)

	toastStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ffffff")).
			Background(colorError).
			Padding(0, 1)
)
