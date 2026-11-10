//go:build !darwin && !js

package ui

// Other platforms drop the taskbar entry when the window is hidden to the tray.
const dockIconSupported = false

func setDockIconVisible(bool) {}
