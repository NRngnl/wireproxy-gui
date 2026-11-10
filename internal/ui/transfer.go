package ui

import "os"

func exportProfilesToPath(data []byte, path string) error {
	err := os.WriteFile(path, data, 0o600)
	if err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
