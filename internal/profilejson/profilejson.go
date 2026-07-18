// Package profilejson implements JSON persistence and profile transfer adapters.
package profilejson

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

const storeFileName = "profiles.json"

type bundle struct {
	Version  int             `json:"version"`
	Profiles []storedProfile `json:"profiles"`
}

type storedTailscaleConfig struct {
	Hostname               string `json:"hostname,omitempty"`
	AuthKey                string `json:"auth_key,omitempty"`
	Authenticated          bool   `json:"authenticated,omitempty"`
	ControlURL             string `json:"control_url,omitempty"`
	ExitNode               string `json:"exit_node,omitempty"`
	AutoExitNode           bool   `json:"auto_exit_node,omitempty"`
	ExitNodeAllowLANAccess bool   `json:"exit_node_allow_lan_access,omitempty"`
	Ephemeral              bool   `json:"ephemeral,omitempty"`
}

type storedProfile struct {
	ID              string                 `json:"id"`
	Kind            profile.BackendKind    `json:"kind,omitempty"`
	Name            string                 `json:"name"`
	WireGuardConfig string                 `json:"wireguard_config,omitempty"`
	TailscaleConfig *storedTailscaleConfig `json:"tailscale_config,omitempty"`
	SocksHost       string                 `json:"socks_host"`
	SocksPort       int                    `json:"socks_port"`
	AutoStart       bool                   `json:"auto_start"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

type Repository struct {
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

func NewRepository(path string) *Repository {
	return &Repository{path: path}
}

func (r *Repository) Path() string {
	return r.path
}

func (r *Repository) Load() ([]profile.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var stored bundle
	err = json.Unmarshal(data, &stored)
	if err != nil {
		return nil, fmt.Errorf("load profiles: %w", err)
	}
	profiles := toDomainProfiles(stored.Profiles)
	for index := range profiles {
		profiles[index].Normalize()
	}
	return profiles, nil
}

func (r *Repository) Save(profiles []profile.Profile) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	normalized := append([]profile.Profile(nil), profiles...)
	for index := range normalized {
		normalized[index].Normalize()
	}

	data, err := EncodeBundle(normalized)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Dir(r.path), 0o700)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(r.path), storeFileName+".*.tmp")
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
	return os.Rename(tmpPath, r.path)
}

type Codec struct{}

func (Codec) DecodeImport(fileName string, data []byte) ([]profile.Profile, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, application.ErrImportFileEmpty
	}

	if looksLikeJSON(trimmed) {
		var stored bundle
		err := json.Unmarshal(data, &stored)
		if err == nil {
			if len(stored.Profiles) > 0 {
				return importProfilesOrEmpty(toDomainProfiles(stored.Profiles))
			}

			var record storedProfile
			err = json.Unmarshal(data, &record)
			item := record.toDomain()
			if err == nil && hasImportProfileContent(item) {
				return []profile.Profile{item}, nil
			}
			return nil, application.ErrImportProfilesEmpty
		}

		var records []storedProfile
		err = json.Unmarshal(data, &records)
		if err == nil {
			if len(records) > 0 {
				return importProfilesOrEmpty(toDomainProfiles(records))
			}
			return nil, application.ErrImportProfilesEmpty
		}
		if json.Valid(data) {
			return nil, application.ErrImportProfilesEmpty
		}
		return nil, fmt.Errorf("%w: %w", application.ErrImportJSONInvalid, err)
	}

	name := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	if strings.TrimSpace(name) == "" || name == "." {
		name = "Imported profile"
	}
	item := profile.New(name, trimmed, profile.DefaultSocksPort)
	err := item.Validate()
	if err != nil {
		return nil, fmt.Errorf("import WireGuard config: %w", err)
	}
	return []profile.Profile{item}, nil
}

func (Codec) EncodeExport(profiles []profile.Profile) ([]byte, error) {
	exported := append([]profile.Profile(nil), profiles...)
	for index := range exported {
		exported[index].Normalize()
		if exported[index].IsTailscale() {
			exported[index].TailscaleConfig.Authenticated = false
		}
	}
	return EncodeBundle(exported)
}

func EncodeBundle(profiles []profile.Profile) ([]byte, error) {
	stored := bundle{Version: 1, Profiles: fromDomainProfiles(profiles)}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func fromDomainProfiles(profiles []profile.Profile) []storedProfile {
	stored := make([]storedProfile, 0, len(profiles))
	for _, item := range profiles {
		stored = append(stored, fromDomainProfile(item))
	}
	return stored
}

func fromDomainProfile(item profile.Profile) storedProfile {
	stored := storedProfile{
		ID:              item.ID,
		Kind:            item.Kind,
		Name:            item.Name,
		WireGuardConfig: item.WireGuardConfig,
		SocksHost:       item.SocksHost,
		SocksPort:       item.SocksPort,
		AutoStart:       item.AutoStart,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
	}
	if item.IsTailscale() || item.TailscaleConfig != (profile.TailscaleConfig{}) {
		stored.TailscaleConfig = &storedTailscaleConfig{
			Hostname:               item.TailscaleConfig.Hostname,
			AuthKey:                item.TailscaleConfig.AuthKey,
			Authenticated:          item.TailscaleConfig.Authenticated,
			ControlURL:             item.TailscaleConfig.ControlURL,
			ExitNode:               item.TailscaleConfig.ExitNode,
			AutoExitNode:           item.TailscaleConfig.AutoExitNode,
			ExitNodeAllowLANAccess: item.TailscaleConfig.ExitNodeAllowLANAccess,
			Ephemeral:              item.TailscaleConfig.Ephemeral,
		}
	}
	return stored
}

func toDomainProfiles(stored []storedProfile) []profile.Profile {
	profiles := make([]profile.Profile, 0, len(stored))
	for _, item := range stored {
		profiles = append(profiles, item.toDomain())
	}
	return profiles
}

func (item storedProfile) toDomain() profile.Profile {
	domain := profile.Profile{
		ID:              item.ID,
		Kind:            item.Kind,
		Name:            item.Name,
		WireGuardConfig: item.WireGuardConfig,
		SocksHost:       item.SocksHost,
		SocksPort:       item.SocksPort,
		AutoStart:       item.AutoStart,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
	}
	if item.TailscaleConfig != nil {
		domain.TailscaleConfig = profile.TailscaleConfig{
			Hostname:               item.TailscaleConfig.Hostname,
			AuthKey:                item.TailscaleConfig.AuthKey,
			Authenticated:          item.TailscaleConfig.Authenticated,
			ControlURL:             item.TailscaleConfig.ControlURL,
			ExitNode:               item.TailscaleConfig.ExitNode,
			AutoExitNode:           item.TailscaleConfig.AutoExitNode,
			ExitNodeAllowLANAccess: item.TailscaleConfig.ExitNodeAllowLANAccess,
			Ephemeral:              item.TailscaleConfig.Ephemeral,
		}
	}
	return domain
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

func importProfilesOrEmpty(profiles []profile.Profile) ([]profile.Profile, error) {
	filtered := make([]profile.Profile, 0, len(profiles))
	for _, item := range profiles {
		if hasImportProfileContent(item) {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return nil, application.ErrImportProfilesEmpty
	}
	return filtered, nil
}

func hasImportProfileContent(item profile.Profile) bool {
	if strings.TrimSpace(item.Name) != "" || strings.TrimSpace(item.WireGuardConfig) != "" {
		return true
	}
	item.TailscaleConfig.Normalize()
	item.TailscaleConfig.Authenticated = false
	return item.IsTailscale() && (item.TailscaleConfig != profile.TailscaleConfig{})
}
