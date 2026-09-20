package screens

import (
	"strconv"

	"github.com/charmbracelet/huh"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// EditForm wraps a huh.Form for creating or editing a profile.Profile. It
// keeps its own scratch fields (strings/bools suitable for huh field
// binding) and reassembles them into a profile.Profile via Result() once
// the form has been submitted.
type EditForm struct {
	form *huh.Form

	isNew      bool
	originalID string

	name      string
	kind      string
	socksHost string
	socksPort string

	wireGuardConfig string

	tsHostname     string
	tsAuthKey      string
	tsControlURL   string
	tsExitNode     string
	tsAutoExitNode bool
	tsExitNodeLAN  bool
	tsEphemeral    bool

	// autoStart, tsAuthenticated, and tsPortForwards preserve fields that
	// have no dedicated form control in v1 but must not be dropped by
	// Result() when editing an existing profile.
	autoStart       bool
	tsAuthenticated bool
	tsPortForwards  []profile.PortForward
}

const (
	kindWireGuard = "wireguard"
	kindTailscale = "tailscale"
)

// NewEditForm builds an EditForm seeded from existing. When isNew is true
// the caller intends to create a new profile (existing is typically a
// freshly application.Add()-ed profile.Profile); when false the form edits
// an already-saved profile in place.
func NewEditForm(existing profile.Profile, isNew bool) *EditForm {
	kind := string(existing.Kind)
	if kind == "" {
		kind = kindWireGuard
	}

	socksHost := existing.SocksHost
	if socksHost == "" {
		socksHost = profile.DefaultSocksHost
	}
	socksPort := existing.SocksPort
	if socksPort == 0 {
		socksPort = profile.DefaultSocksPort
	}

	f := &EditForm{
		isNew:           isNew,
		originalID:      existing.ID,
		name:            existing.Name,
		kind:            kind,
		socksHost:       socksHost,
		socksPort:       strconv.Itoa(socksPort),
		wireGuardConfig: existing.WireGuardConfig,
		tsHostname:      existing.TailscaleConfig.Hostname,
		tsAuthKey:       existing.TailscaleConfig.AuthKey,
		tsControlURL:    existing.TailscaleConfig.ControlURL,
		tsExitNode:      existing.TailscaleConfig.ExitNode,
		tsAutoExitNode:  existing.TailscaleConfig.AutoExitNode,
		tsExitNodeLAN:   existing.TailscaleConfig.ExitNodeAllowLANAccess,
		tsEphemeral:     existing.TailscaleConfig.Ephemeral,
		autoStart:       existing.AutoStart,
		tsAuthenticated: existing.TailscaleConfig.Authenticated,
		tsPortForwards:  existing.TailscaleConfig.PortForwards,
	}

	f.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Value(&f.name).
				Validate(func(s string) error {
					if trimmedEmpty(s) {
						return profile.ErrProfileNameRequired
					}
					return nil
				}),
			huh.NewSelect[string]().
				Title("Backend").
				Options(
					huh.NewOption("WireGuard", kindWireGuard),
					huh.NewOption("Tailscale", kindTailscale),
				).
				Value(&f.kind),
			huh.NewInput().
				Title("SOCKS5 host").
				Value(&f.socksHost).
				Validate(func(s string) error {
					if trimmedEmpty(s) {
						return profile.ErrSocksHostRequired
					}
					return nil
				}),
			huh.NewInput().
				Title("SOCKS5 port").
				Value(&f.socksPort).
				Validate(func(s string) error {
					port, err := strconv.Atoi(s)
					if err != nil {
						return profile.ErrSocksPortNotNumber
					}
					if port < 1 || port > 65535 {
						return profile.ErrSocksPortOutOfRange
					}
					return nil
				}),
			huh.NewConfirm().
				Title("Start automatically").
				Value(&f.autoStart),
		),
		huh.NewGroup(
			huh.NewText().
				Title("WireGuard config").
				Value(&f.wireGuardConfig),
		).WithHideFunc(func() bool { return f.kind != kindWireGuard }),
		huh.NewGroup(
			huh.NewInput().Title("Tailscale hostname").Value(&f.tsHostname),
			huh.NewInput().Title("Tailscale auth key").Value(&f.tsAuthKey),
			huh.NewInput().Title("Control URL").Value(&f.tsControlURL),
			huh.NewInput().Title("Exit node (name or IP)").Value(&f.tsExitNode),
			huh.NewConfirm().Title("Automatic exit node").Value(&f.tsAutoExitNode),
			huh.NewConfirm().Title("Allow LAN access via exit node").Value(&f.tsExitNodeLAN),
			huh.NewConfirm().Title("Ephemeral").Value(&f.tsEphemeral),
		).WithHideFunc(func() bool { return f.kind != kindTailscale }),
	)

	return f
}

// trimmedEmpty reports whether s contains only whitespace or nothing.
func trimmedEmpty(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// Form returns the underlying huh.Form for embedding in a Bubble Tea
// Update/View loop.
func (f *EditForm) Form() *huh.Form {
	return f.form
}

// IsNew reports whether this form was constructed for a brand-new profile
// (as opposed to editing an existing one).
func (f *EditForm) IsNew() bool {
	return f.isNew
}

// Result reassembles the form's scratch fields into a profile.Profile. The
// caller is expected to pass this to application.Save. SocksPort parse
// failures fall back to profile.DefaultSocksPort since field-level
// validation should already have rejected invalid input before submit.
func (f *EditForm) Result() profile.Profile {
	port, err := strconv.Atoi(f.socksPort)
	if err != nil {
		port = profile.DefaultSocksPort
	}

	result := profile.Profile{
		ID:        f.originalID,
		Kind:      profile.BackendKind(f.kind),
		Name:      f.name,
		SocksHost: f.socksHost,
		SocksPort: port,
	}

	result.AutoStart = f.autoStart

	if result.Kind == profile.BackendWireGuard {
		result.WireGuardConfig = f.wireGuardConfig
	} else {
		result.TailscaleConfig = profile.TailscaleConfig{
			Hostname:               f.tsHostname,
			AuthKey:                f.tsAuthKey,
			Authenticated:          f.tsAuthenticated,
			ControlURL:             f.tsControlURL,
			ExitNode:               f.tsExitNode,
			AutoExitNode:           f.tsAutoExitNode,
			ExitNodeAllowLANAccess: f.tsExitNodeLAN,
			Ephemeral:              f.tsEphemeral,
			PortForwards:           f.tsPortForwards,
		}
	}

	return result
}
