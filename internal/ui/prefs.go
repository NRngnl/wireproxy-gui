//go:build !js

package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type prefs struct {
	HideDock bool `json:"hideDockIcon"`
}

func prefsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "wireproxy-gui", "gui.json")
}

// legacyFynePrefsPath is where the Fyne build stored its hideDockIcon preference on macOS.
func legacyFynePrefsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Preferences", "fyne", "com.github.nrngnl.wireproxy-gui", "preferences.json")
}

func readPrefs(path string) (prefs, bool) {
	var p prefs
	data, err := os.ReadFile(path)
	if path == "" || err != nil || json.Unmarshal(data, &p) != nil {
		return prefs{}, false
	}
	return p, true
}

func loadPrefs() prefs {
	if p, ok := readPrefs(prefsPath()); ok {
		return p
	}
	p, ok := readPrefs(legacyFynePrefsPath())
	if ok {
		_ = p.save()
	}
	return p
}

func (p prefs) save() error {
	path := prefsPath()
	if path == "" {
		return os.ErrNotExist
	}
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
