package ui

import (
	"errors"

	"github.com/ncruces/zenity"
)

var errNativeFileDialogUnavailable = errors.New("native file picker is unavailable; install zenity, matedialog, or qarma on Unix-like systems")

type nativeProfileFileDialog struct{}

func (nativeProfileFileDialog) OpenProfilePath() (string, error) {
	err := ensureNativeFileDialogAvailable()
	if err != nil {
		return "", err
	}
	path, err := zenity.SelectFile(
		zenity.Title(tr("Import profiles")),
		zenity.Modal(),
		profileImportFilters(),
	)
	return selectedPath(path, err)
}

func (nativeProfileFileDialog) SaveProfilesPath(fileName string) (string, error) {
	err := ensureNativeFileDialogAvailable()
	if err != nil {
		return "", err
	}
	path, err := zenity.SelectFileSave(
		zenity.Title(tr("Export profiles")),
		zenity.Modal(),
		zenity.Filename(fileName),
		zenity.ConfirmOverwrite(),
		profileExportFilters(),
	)
	return selectedPath(path, err)
}

func profileImportFilters() zenity.FileFilters {
	return zenity.FileFilters{
		{Name: tr("WireGuard or JSON profiles"), Patterns: []string{"*.conf", "*.json"}, CaseFold: true},
	}
}

func profileExportFilters() zenity.FileFilters {
	return zenity.FileFilters{
		{Name: tr("JSON profiles"), Patterns: []string{"*.json"}, CaseFold: true},
	}
}

func ensureNativeFileDialogAvailable() error {
	if zenity.IsAvailable() {
		return nil
	}
	return errNativeFileDialogUnavailable
}

func selectedPath(path string, err error) (string, error) {
	if errors.Is(err, zenity.ErrCanceled) {
		return "", nil
	}
	return path, err
}
