package profile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
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
	ErrProfileNameRequired        = errors.New("profile name is required")
	ErrSocksHostRequired          = errors.New("SOCKS5 host is required")
	ErrSocksPortNotNumber         = errors.New("SOCKS5 port must be a number")
	ErrSocksPortOutOfRange        = errors.New("SOCKS5 port must be between 1 and 65535")
	ErrBackendKindInvalid         = errors.New("profile backend must be WireGuard or Tailscale")
	ErrWireGuardConfigMissing     = errors.New("WireGuard config is missing required fields")
	ErrWireGuardConfigEmpty       = errors.New("WireGuard config is required")
	ErrTailscaleExitNodeMode      = errors.New("tailscale exit node must be automatic or a specific node, not both")
	ErrDuplicateBindAddress       = errors.New("duplicate SOCKS5 bind address")
	ErrPortForwardPortOutOfRange  = errors.New("port forward listen port must be between 1 and 65535")
	ErrPortForwardProtocolInvalid = errors.New("port forward protocol must be tcp or udp")
	ErrPortForwardTargetRequired  = errors.New("port forward target address is required")
	ErrPortForwardTargetInvalid   = errors.New("port forward target address must be host:port")
	ErrPortForwardDuplicatePort   = errors.New("duplicate port forward listen port")
)

// PortForwardProtocol identifies the transport protocol of a forward rule.
type PortForwardProtocol string

const (
	PortForwardTCP PortForwardProtocol = "tcp"
	PortForwardUDP PortForwardProtocol = "udp"
)

// PortForward describes one inbound-tailnet-IP-to-LAN relay rule: a
// Tailscale peer dialing this node's tailnet IP at ListenPort is relayed to
// TargetAddr (host:port) on the local network, using a plain net.Dial (not
// node.Dial, since the target is not itself a tailnet peer).
type PortForward struct {
	ListenPort int
	Protocol   PortForwardProtocol
	TargetAddr string
}

type TailscaleConfig struct {
	Hostname               string
	AuthKey                string
	Authenticated          bool
	ControlURL             string
	ExitNode               string
	AutoExitNode           bool
	ExitNodeAllowLANAccess bool
	Ephemeral              bool
	PortForwards           []PortForward
}

type Profile struct {
	ID              string
	Kind            BackendKind
	Name            string
	WireGuardConfig string
	TailscaleConfig TailscaleConfig
	SocksHost       string
	SocksPort       int
	AutoStart       bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
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
	for i := range c.PortForwards {
		c.PortForwards[i].TargetAddr = strings.TrimSpace(c.PortForwards[i].TargetAddr)
		c.PortForwards[i].Protocol = PortForwardProtocol(strings.ToLower(strings.TrimSpace(string(c.PortForwards[i].Protocol))))
		if c.PortForwards[i].Protocol == "" {
			c.PortForwards[i].Protocol = PortForwardTCP
		}
	}
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
	var errs []error
	if c.AutoExitNode && strings.TrimSpace(c.ExitNode) != "" {
		errs = append(errs, ErrTailscaleExitNodeMode)
	}
	seenPorts := map[int]bool{}
	for _, forward := range c.PortForwards {
		if forward.ListenPort < 1 || forward.ListenPort > 65535 {
			errs = append(errs, ErrPortForwardPortOutOfRange)
		}
		switch forward.Protocol {
		case PortForwardTCP, PortForwardUDP:
		default:
			errs = append(errs, ErrPortForwardProtocolInvalid)
		}
		target := strings.TrimSpace(forward.TargetAddr)
		if target == "" {
			errs = append(errs, ErrPortForwardTargetRequired)
		} else {
			_, _, err := net.SplitHostPort(target)
			if err != nil {
				errs = append(errs, ErrPortForwardTargetInvalid)
			}
		}
		if seenPorts[forward.ListenPort] {
			errs = append(errs, ErrPortForwardDuplicatePort)
		}
		seenPorts[forward.ListenPort] = true
	}
	return errors.Join(errs...)
}

// IsZero reports whether c is the zero-value TailscaleConfig.
func (c TailscaleConfig) IsZero() bool {
	return c.Hostname == "" &&
		c.AuthKey == "" &&
		!c.Authenticated &&
		c.ControlURL == "" &&
		c.ExitNode == "" &&
		!c.AutoExitNode &&
		!c.ExitNodeAllowLANAccess &&
		!c.Ephemeral &&
		len(c.PortForwards) == 0
}

// tailscaleConfigEqual compares every TailscaleConfig field explicitly,
// since the presence of the PortForwards slice makes the struct
// non-comparable with ==/!=.
func tailscaleConfigEqual(before, after TailscaleConfig) bool {
	return before.Hostname == after.Hostname &&
		before.AuthKey == after.AuthKey &&
		before.Authenticated == after.Authenticated &&
		before.ControlURL == after.ControlURL &&
		before.ExitNode == after.ExitNode &&
		before.AutoExitNode == after.AutoExitNode &&
		before.ExitNodeAllowLANAccess == after.ExitNodeAllowLANAccess &&
		before.Ephemeral == after.Ephemeral &&
		slices.Equal(before.PortForwards, after.PortForwards)
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
	return ""
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

// RuntimeConfigChanged reports whether applying after to an active profile
// requires its backend to be restarted.
func RuntimeConfigChanged(before, after Profile) bool {
	before.Normalize()
	after.Normalize()
	return before.Kind != after.Kind ||
		before.WireGuardConfig != after.WireGuardConfig ||
		tailscaleRestartConfigChanged(before.TailscaleConfig, after.TailscaleConfig) ||
		before.BindAddress() != after.BindAddress()
}

func tailscaleRestartConfigChanged(before, after TailscaleConfig) bool {
	before.Normalize()
	after.Normalize()
	return before.Hostname != after.Hostname ||
		before.AuthKey != after.AuthKey ||
		before.ControlURL != after.ControlURL ||
		before.Ephemeral != after.Ephemeral ||
		!slices.Equal(before.PortForwards, after.PortForwards)
}

// ExitNodeConfigChanged reports whether the live-updatable Tailscale exit-node
// preferences differ between two versions of the same profile.
func ExitNodeConfigChanged(before, after Profile) bool {
	before.Normalize()
	after.Normalize()
	if !before.IsTailscale() || !after.IsTailscale() {
		return false
	}
	return before.TailscaleConfig.ExitNode != after.TailscaleConfig.ExitNode ||
		before.TailscaleConfig.AutoExitNode != after.TailscaleConfig.AutoExitNode ||
		before.TailscaleConfig.ExitNodeAllowLANAccess != after.TailscaleConfig.ExitNodeAllowLANAccess
}

// FieldsChanged compares persisted profile fields while ignoring timestamps.
func FieldsChanged(before, after Profile) bool {
	before.Normalize()
	after.Normalize()
	return before.Kind != after.Kind ||
		before.Name != after.Name ||
		before.WireGuardConfig != after.WireGuardConfig ||
		!tailscaleConfigEqual(before.TailscaleConfig, after.TailscaleConfig) ||
		before.SocksHost != after.SocksHost ||
		before.SocksPort != after.SocksPort ||
		before.AutoStart != after.AutoStart
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
