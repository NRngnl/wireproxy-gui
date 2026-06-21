package profile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSocksHost = "127.0.0.1"
	DefaultSocksPort = 1080
)

type BackendKind string

const (
	BackendWireGuard BackendKind = "wireguard"
	BackendTailscale BackendKind = "tailscale"
)

var (
	ErrProfileNameRequired    = errors.New("profile name is required")
	ErrSocksHostRequired      = errors.New("SOCKS5 host is required")
	ErrSocksPortNotNumber     = errors.New("SOCKS5 port must be a number")
	ErrSocksPortOutOfRange    = errors.New("SOCKS5 port must be between 1 and 65535")
	ErrBackendKindInvalid     = errors.New("profile backend must be WireGuard or Tailscale")
	ErrWireGuardConfigMissing = errors.New("WireGuard config is missing required fields")
	ErrWireGuardConfigEmpty   = errors.New("WireGuard config is required")
	ErrTailscaleExitNodeMode  = errors.New("Tailscale exit node must be automatic or a specific node, not both")
	ErrImportFileEmpty        = errors.New("import file is empty")
	ErrImportJSONInvalid      = errors.New("import JSON is invalid")
	ErrImportProfilesEmpty    = errors.New("import JSON does not contain any valid profiles")
	ErrDuplicateBindAddress   = errors.New("duplicate SOCKS5 bind address")
)

type TailscaleConfig struct {
	Hostname               string `json:"hostname,omitempty"`
	AuthKey                string `json:"auth_key,omitempty"`
	Authenticated          bool   `json:"authenticated,omitempty"`
	ControlURL             string `json:"control_url,omitempty"`
	ExitNode               string `json:"exit_node,omitempty"`
	AutoExitNode           bool   `json:"auto_exit_node,omitempty"`
	ExitNodeAllowLANAccess bool   `json:"exit_node_allow_lan_access,omitempty"`
	Ephemeral              bool   `json:"ephemeral,omitempty"`
}

type Profile struct {
	ID              string          `json:"id"`
	Kind            BackendKind     `json:"kind,omitempty"`
	Name            string          `json:"name"`
	WireGuardConfig string          `json:"wireguard_config,omitempty"`
	TailscaleConfig TailscaleConfig `json:"tailscale_config,omitempty"`
	SocksHost       string          `json:"socks_host"`
	SocksPort       int             `json:"socks_port"`
	AutoStart       bool            `json:"auto_start"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func New(name, wireGuardConfig string, socksPort int) Profile {
	now := time.Now().UTC()
	p := Profile{
		ID:              NewID(),
		Kind:            BackendWireGuard,
		Name:            strings.TrimSpace(name),
		WireGuardConfig: strings.TrimSpace(wireGuardConfig),
		SocksHost:       DefaultSocksHost,
		SocksPort:       socksPort,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	p.Normalize()
	return p
}

func NewTailscale(name string, socksPort int) Profile {
	now := time.Now().UTC()
	p := Profile{
		ID:        NewID(),
		Kind:      BackendTailscale,
		Name:      strings.TrimSpace(name),
		SocksHost: DefaultSocksHost,
		SocksPort: socksPort,
		CreatedAt: now,
		UpdatedAt: now,
	}
	p.Normalize()
	return p
}

func NewID() string {
	var b [8]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b[:])
}

func (p *Profile) Normalize() {
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" || unsafeProfileID(p.ID) {
		p.ID = NewID()
	}
	p.Kind = normalizeBackendKind(p.Kind)
	p.Name = strings.TrimSpace(p.Name)
	p.WireGuardConfig = strings.TrimSpace(p.WireGuardConfig)
	p.TailscaleConfig.Normalize()
	p.SocksHost = strings.TrimSpace(p.SocksHost)
	if p.SocksHost == "" {
		p.SocksHost = DefaultSocksHost
	}
	if p.SocksPort == 0 {
		p.SocksPort = DefaultSocksPort
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
}

func unsafeProfileID(id string) bool {
	return id == "." || id == ".." || strings.ContainsAny(id, `/\`)
}

func normalizeBackendKind(kind BackendKind) BackendKind {
	switch BackendKind(strings.ToLower(strings.TrimSpace(string(kind)))) {
	case "", BackendWireGuard:
		return BackendWireGuard
	case BackendTailscale:
		return BackendTailscale
	default:
		return kind
	}
}

func (c *TailscaleConfig) Normalize() {
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.AuthKey = strings.TrimSpace(c.AuthKey)
	if c.Authenticated {
		c.AuthKey = ""
	}
	c.ControlURL = strings.TrimSpace(c.ControlURL)
	c.ExitNode = strings.TrimSpace(c.ExitNode)
}

func (p *Profile) Touch() {
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
}

func (p Profile) Validate() error {
	var errs []error
	if strings.TrimSpace(p.Name) == "" {
		errs = append(errs, ErrProfileNameRequired)
	}
	if strings.TrimSpace(p.SocksHost) == "" {
		errs = append(errs, ErrSocksHostRequired)
	}
	if p.SocksPort < 1 || p.SocksPort > 65535 {
		errs = append(errs, ErrSocksPortOutOfRange)
	}
	switch normalizeBackendKind(p.Kind) {
	case BackendWireGuard:
		err := ValidateWireGuardConfig(p.WireGuardConfig)
		if err != nil {
			errs = append(errs, err)
		}
	case BackendTailscale:
		err := p.TailscaleConfig.Validate()
		if err != nil {
			errs = append(errs, err)
		}
	default:
		errs = append(errs, ErrBackendKindInvalid)
	}
	return errors.Join(errs...)
}

func (c TailscaleConfig) Validate() error {
	if c.AutoExitNode && strings.TrimSpace(c.ExitNode) != "" {
		return ErrTailscaleExitNodeMode
	}
	return nil
}

func (p Profile) IsWireGuard() bool {
	return p.Kind == "" || normalizeBackendKind(p.Kind) == BackendWireGuard
}

func (p Profile) IsTailscale() bool {
	return normalizeBackendKind(p.Kind) == BackendTailscale
}

func (p Profile) BindAddress() string {
	host := strings.TrimSpace(p.SocksHost)
	if host == "" {
		host = DefaultSocksHost
	}
	port := p.SocksPort
	if port == 0 {
		port = DefaultSocksPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (p Profile) WireGuardAddress() string {
	return WireGuardAddress(p.WireGuardConfig)
}

func WireGuardAddress(text string) string {
	current := ""
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			end := strings.Index(trimmed, "]")
			current = strings.ToLower(strings.TrimSpace(trimmed[1:end]))
			continue
		}
		if current != "interface" {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Address") {
			continue
		}
		value = strings.TrimSpace(value)
		if before, _, ok := strings.Cut(value, "#"); ok {
			value = strings.TrimSpace(before)
		}
		if before, _, ok := strings.Cut(value, ";"); ok {
			value = strings.TrimSpace(before)
		}
		if value != "" {
			return value
		}
	}
	return "not configured"
}

func (p Profile) WireproxyConfig() string {
	base := stripSection(p.WireGuardConfig, "Socks5")
	base = strings.TrimSpace(base)
	return fmt.Sprintf("%s\n\n[Socks5]\nBindAddress = %s\n", base, p.BindAddress())
}

func ValidateWireGuardConfig(text string) error {
	sections := parseSections(text)
	if len(sections) == 0 {
		return ErrWireGuardConfigEmpty
	}
	interfaceKeys := sections["interface"]
	peerKeys := sections["peer"]

	var missing []string
	if interfaceKeys == nil {
		missing = append(missing, "[Interface]")
	} else {
		for _, field := range []struct {
			key  string
			name string
		}{
			{key: "address", name: "Address"},
			{key: "privatekey", name: "PrivateKey"},
		} {
			if !interfaceKeys[field.key] {
				missing = append(missing, "[Interface] "+field.name)
			}
		}
	}
	if peerKeys == nil {
		missing = append(missing, "[Peer]")
	} else {
		for _, field := range []struct {
			key  string
			name string
		}{
			{key: "publickey", name: "PublicKey"},
		} {
			if !peerKeys[field.key] {
				missing = append(missing, "[Peer] "+field.name)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrWireGuardConfigMissing, strings.Join(missing, ", "))
	}
	return nil
}

func NextAvailablePort(profiles []Profile) int {
	used := map[int]bool{}
	for _, p := range profiles {
		if p.SocksPort > 0 {
			used[p.SocksPort] = true
		}
	}
	for port := DefaultSocksPort; port <= 65535; port++ {
		if !used[port] {
			return port
		}
	}
	return DefaultSocksPort
}

func PrepareImported(imported, existing []Profile) []Profile {
	usedPorts := map[int]bool{}
	for _, p := range existing {
		if p.SocksPort > 0 {
			usedPorts[p.SocksPort] = true
		}
	}

	nextPort := DefaultSocksPort
	now := time.Now().UTC()
	for i := range imported {
		imported[i].ID = NewID()
		imported[i].Normalize()
		if imported[i].IsTailscale() {
			imported[i].TailscaleConfig.Authenticated = false
		}
		if imported[i].Name == "" {
			imported[i].Name = "Imported profile"
		}
		imported[i].CreatedAt = now
		imported[i].UpdatedAt = now

		if imported[i].SocksPort < 1 || imported[i].SocksPort > 65535 || usedPorts[imported[i].SocksPort] {
			port, ok := nextAvailableImportPort(usedPorts, nextPort)
			if ok {
				imported[i].SocksPort = port
				nextPort = port + 1
			} else {
				imported[i].SocksPort = DefaultSocksPort
			}
		}
		usedPorts[imported[i].SocksPort] = true
	}
	return imported
}

func nextAvailableImportPort(usedPorts map[int]bool, start int) (int, bool) {
	if start < 1 || start > 65535 {
		start = DefaultSocksPort
	}
	for port := start; port <= 65535; port++ {
		if !usedPorts[port] {
			return port, true
		}
	}
	for port := 1; port < start; port++ {
		if !usedPorts[port] {
			return port, true
		}
	}
	return 0, false
}

func DuplicateBindAddress(profiles []Profile) (bindAddress, firstName, secondName string, found bool) {
	seen := map[string]string{}
	for _, p := range profiles {
		bind := p.BindAddress()
		if first, ok := seen[bind]; ok {
			return bind, first, p.Name, true
		}
		seen[bind] = p.Name
	}
	return "", "", "", false
}

func parseSections(text string) map[string]map[string]bool {
	sections := map[string]map[string]bool{}
	current := ""
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			end := strings.Index(trimmed, "]")
			current = strings.ToLower(strings.TrimSpace(trimmed[1:end]))
			if current != "" && sections[current] == nil {
				sections[current] = map[string]bool{}
			}
			continue
		}
		if current == "" {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			sections[current][key] = true
		}
	}
	return sections
}

func stripSection(text, sectionName string) string {
	target := strings.ToLower(strings.TrimSpace(sectionName))
	var kept []string
	dropping := false

	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
			end := strings.Index(trimmed, "]")
			current := strings.ToLower(strings.TrimSpace(trimmed[1:end]))
			dropping = current == target
		}
		if !dropping {
			kept = append(kept, line)
		}
	}

	return strings.TrimSpace(strings.Join(kept, "\n"))
}
