package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const storeFileName = "profiles.json"

type Bundle struct {
	Version  int       `json:"version"`
	Profiles []Profile `json:"profiles"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func DefaultStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wireproxy-gui", storeFileName), nil
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var bundle Bundle
	err = json.Unmarshal(data, &bundle)
	if err != nil {
		return nil, fmt.Errorf("load profiles: %w", err)
	}
	for i := range bundle.Profiles {
		bundle.Profiles[i].Normalize()
	}
	return bundle.Profiles, nil
}

func (s *Store) Save(profiles []Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := append([]Profile(nil), profiles...)
	for i := range normalized {
		normalized[i].Normalize()
	}

	data, err := EncodeBundle(normalized)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Dir(s.path), 0o700)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), storeFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, err = tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	err = tmp.Chmod(0o600)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	err = tmp.Close()
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func EncodeBundle(profiles []Profile) ([]byte, error) {
	bundle := Bundle{Version: 1, Profiles: profiles}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func DecodeImport(fileName string, data []byte) ([]Profile, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, ErrImportFileEmpty
	}

	if looksLikeJSON(trimmed) {
		var bundle Bundle
		err := json.Unmarshal(data, &bundle)
		if err == nil {
			if len(bundle.Profiles) > 0 {
				return importProfilesOrEmpty(bundle.Profiles)
			}

			var p Profile
			err = json.Unmarshal(data, &p)
			if err == nil && hasImportProfileContent(p) {
				return []Profile{p}, nil
			}
			return nil, ErrImportProfilesEmpty
		}

		var profiles []Profile
		err = json.Unmarshal(data, &profiles)
		if err == nil {
			if len(profiles) > 0 {
				return importProfilesOrEmpty(profiles)
			}
			return nil, ErrImportProfilesEmpty
		}
		if json.Valid(data) {
			return nil, ErrImportProfilesEmpty
		}
		return nil, fmt.Errorf("%w: %w", ErrImportJSONInvalid, err)
	}

	name := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	if strings.TrimSpace(name) == "" || name == "." {
		name = "Imported profile"
	}
	p := New(name, trimmed, DefaultSocksPort)
	err := p.Validate()
	if err != nil {
		return nil, fmt.Errorf("import WireGuard config: %w", err)
	}
	return []Profile{p}, nil
}

func looksLikeJSON(trimmed string) bool {
	if strings.HasPrefix(trimmed, "{") {
		return true
	}
	if !strings.HasPrefix(trimmed, "[") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "["))
	if rest == "" {
		return true
	}
	switch rest[0] {
	case '{', '[', ']', '"', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return true
	}
	return strings.HasPrefix(rest, "true") || strings.HasPrefix(rest, "false") || strings.HasPrefix(rest, "null")
}

func importProfilesOrEmpty(profiles []Profile) ([]Profile, error) {
	filtered := make([]Profile, 0, len(profiles))
	for _, p := range profiles {
		if hasImportProfileContent(p) {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return nil, ErrImportProfilesEmpty
	}
	return filtered, nil
}

func hasImportProfileContent(p Profile) bool {
	return strings.TrimSpace(p.Name) != "" || strings.TrimSpace(p.WireGuardConfig) != ""
}
