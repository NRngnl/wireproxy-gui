package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed appicon.png
var appIconPNG []byte

var appIcon = fyne.NewStaticResource("appicon.png", appIconPNG)
