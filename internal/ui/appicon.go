package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

//go:embed appicon.png
var appIconPNG []byte

var appIcon = fyne.NewStaticResource("appicon.png", appIconPNG)

//go:embed trayicon.svg
var trayIconSVG []byte

// macTrayIcon is a monochrome template image: macOS tints it to match the menu bar
// (light, dark, or tinted), like the system's own status items.
var macTrayIcon = theme.NewThemedResource(fyne.NewStaticResource("trayicon.svg", trayIconSVG))
