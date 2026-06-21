package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type nativeTheme struct {
	fallback fyne.Theme
}

func newNativeTheme() fyne.Theme {
	return nativeTheme{fallback: theme.DefaultTheme()}
}

func applyAppTheme(app fyne.App) {
	app.Settings().SetTheme(newNativeTheme())
}

func (t nativeTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	if variant == theme.VariantLight {
		if c, ok := nativeLightColors[name]; ok {
			return c
		}
	} else {
		if c, ok := nativeDarkColors[name]; ok {
			return c
		}
	}
	return t.fallback.Color(name, variant)
}

func (t nativeTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.fallback.Font(style)
}

func (t nativeTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.fallback.Icon(name)
}

func (t nativeTheme) Size(name fyne.ThemeSizeName) float32 {
	return t.fallback.Size(name)
}

var nativeDarkColors = map[fyne.ThemeColorName]color.Color{
	theme.ColorNameBackground:          color.NRGBA{R: 0x1c, G: 0x1c, B: 0x1e, A: 0xff},
	theme.ColorNameButton:              color.NRGBA{R: 0x2c, G: 0x2c, B: 0x2e, A: 0xff},
	theme.ColorNameDisabledButton:      color.NRGBA{R: 0x2c, G: 0x2c, B: 0x2e, A: 0xff},
	theme.ColorNameDisabled:            color.NRGBA{R: 0x63, G: 0x63, B: 0x66, A: 0xff},
	theme.ColorNameError:               color.NRGBA{R: 0xff, G: 0x45, B: 0x3a, A: 0xff},
	theme.ColorNameFocus:               color.NRGBA{R: 0x0a, G: 0x84, B: 0xff, A: 0x73},
	theme.ColorNameForeground:          color.NRGBA{R: 0xf2, G: 0xf2, B: 0xf7, A: 0xff},
	theme.ColorNameForegroundOnError:   color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameForegroundOnPrimary: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameForegroundOnSuccess: color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff},
	theme.ColorNameForegroundOnWarning: color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff},
	theme.ColorNameHeaderBackground:    color.NRGBA{R: 0x24, G: 0x24, B: 0x26, A: 0xff},
	theme.ColorNameHover:               color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x12},
	theme.ColorNameHyperlink:           color.NRGBA{R: 0x64, G: 0xd2, B: 0xff, A: 0xff},
	theme.ColorNameInputBackground:     color.NRGBA{R: 0x2c, G: 0x2c, B: 0x2e, A: 0xff},
	theme.ColorNameInputBorder:         color.NRGBA{R: 0x3a, G: 0x3a, B: 0x3c, A: 0xff},
	theme.ColorNameMenuBackground:      color.NRGBA{R: 0x2c, G: 0x2c, B: 0x2e, A: 0xff},
	theme.ColorNameOverlayBackground:   color.NRGBA{R: 0x28, G: 0x28, B: 0x2a, A: 0xff},
	theme.ColorNamePlaceHolder:         color.NRGBA{R: 0x8e, G: 0x8e, B: 0x93, A: 0xff},
	theme.ColorNamePressed:             color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x30},
	theme.ColorNamePrimary:             color.NRGBA{R: 0x0a, G: 0x84, B: 0xff, A: 0xff},
	theme.ColorNameScrollBar:           color.NRGBA{R: 0xeb, G: 0xeb, B: 0xf5, A: 0x66},
	theme.ColorNameScrollBarBackground: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x12},
	theme.ColorNameSelection:           color.NRGBA{R: 0x0a, G: 0x84, B: 0xff, A: 0x45},
	theme.ColorNameSeparator:           color.NRGBA{R: 0x3a, G: 0x3a, B: 0x3c, A: 0xff},
	theme.ColorNameShadow:              color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x55},
	theme.ColorNameSuccess:             color.NRGBA{R: 0x30, G: 0xd1, B: 0x58, A: 0xff},
	theme.ColorNameWarning:             color.NRGBA{R: 0xff, G: 0xd6, B: 0x0a, A: 0xff},
}

var nativeLightColors = map[fyne.ThemeColorName]color.Color{
	theme.ColorNameBackground:          color.NRGBA{R: 0xf5, G: 0xf5, B: 0xf7, A: 0xff},
	theme.ColorNameButton:              color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameDisabledButton:      color.NRGBA{R: 0xe5, G: 0xe5, B: 0xea, A: 0xff},
	theme.ColorNameDisabled:            color.NRGBA{R: 0x8e, G: 0x8e, B: 0x93, A: 0xff},
	theme.ColorNameError:               color.NRGBA{R: 0xff, G: 0x3b, B: 0x30, A: 0xff},
	theme.ColorNameFocus:               color.NRGBA{R: 0x00, G: 0x7a, B: 0xff, A: 0x55},
	theme.ColorNameForeground:          color.NRGBA{R: 0x1d, G: 0x1d, B: 0x1f, A: 0xff},
	theme.ColorNameForegroundOnError:   color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameForegroundOnPrimary: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameForegroundOnSuccess: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameForegroundOnWarning: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameHeaderBackground:    color.NRGBA{R: 0xec, G: 0xec, B: 0xef, A: 0xff},
	theme.ColorNameHover:               color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x0c},
	theme.ColorNameHyperlink:           color.NRGBA{R: 0x00, G: 0x7a, B: 0xff, A: 0xff},
	theme.ColorNameInputBackground:     color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameInputBorder:         color.NRGBA{R: 0xd1, G: 0xd1, B: 0xd6, A: 0xff},
	theme.ColorNameMenuBackground:      color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNameOverlayBackground:   color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	theme.ColorNamePlaceHolder:         color.NRGBA{R: 0x8e, G: 0x8e, B: 0x93, A: 0xff},
	theme.ColorNamePressed:             color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x1a},
	theme.ColorNamePrimary:             color.NRGBA{R: 0x00, G: 0x7a, B: 0xff, A: 0xff},
	theme.ColorNameScrollBar:           color.NRGBA{R: 0x3c, G: 0x3c, B: 0x43, A: 0x4d},
	theme.ColorNameScrollBarBackground: color.NRGBA{R: 0x3c, G: 0x3c, B: 0x43, A: 0x12},
	theme.ColorNameSelection:           color.NRGBA{R: 0x00, G: 0x7a, B: 0xff, A: 0x33},
	theme.ColorNameSeparator:           color.NRGBA{R: 0xd1, G: 0xd1, B: 0xd6, A: 0xff},
	theme.ColorNameShadow:              color.NRGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x26},
	theme.ColorNameSuccess:             color.NRGBA{R: 0x34, G: 0xc7, B: 0x59, A: 0xff},
	theme.ColorNameWarning:             color.NRGBA{R: 0xff, G: 0x95, B: 0x00, A: 0xff},
}
