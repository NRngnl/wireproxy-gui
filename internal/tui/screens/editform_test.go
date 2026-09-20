package screens

import (
	"reflect"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func TestEditFormResultRoundTripsWireGuard(t *testing.T) {
	existing := profile.Profile{
		ID:              "abc123",
		Kind:            profile.BackendWireGuard,
		Name:            "my-vpn",
		WireGuardConfig: "[Interface]\nPrivateKey = x",
		SocksHost:       "127.0.0.1",
		SocksPort:       1080,
	}

	form := NewEditForm(existing, false)
	if form.IsNew() {
		t.Fatalf("expected IsNew() false for an editing form")
	}
	if form.Form() == nil {
		t.Fatalf("expected Form() to return a non-nil huh.Form")
	}

	// Simulate the user editing fields via the form's bound scratch
	// state directly (bypassing the interactive huh runtime, which
	// requires a terminal).
	form.name = "renamed-vpn"
	form.kind = kindWireGuard
	form.socksHost = "0.0.0.0"
	form.socksPort = "9090"
	form.wireGuardConfig = "[Interface]\nPrivateKey = y"

	got := form.Result()

	want := profile.Profile{
		ID:              "abc123",
		Kind:            profile.BackendWireGuard,
		Name:            "renamed-vpn",
		WireGuardConfig: "[Interface]\nPrivateKey = y",
		SocksHost:       "0.0.0.0",
		SocksPort:       9090,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Result() = %#v, want %#v", got, want)
	}
}

func TestEditFormResultRoundTripsTailscale(t *testing.T) {
	existing := profile.Profile{
		ID:   "def456",
		Kind: profile.BackendTailscale,
		Name: "ts-profile",
		TailscaleConfig: profile.TailscaleConfig{
			Hostname:   "host1",
			AuthKey:    "tskey-abc",
			ControlURL: "https://controlplane.example",
			ExitNode:   "exit-1",
		},
		SocksHost: "127.0.0.1",
		SocksPort: 1080,
	}

	form := NewEditForm(existing, true)
	if !form.IsNew() {
		t.Fatalf("expected IsNew() true")
	}

	form.tsAutoExitNode = true
	form.tsExitNodeLAN = true
	form.tsEphemeral = true

	got := form.Result()

	want := profile.Profile{
		ID:   "def456",
		Kind: profile.BackendTailscale,
		Name: "ts-profile",
		TailscaleConfig: profile.TailscaleConfig{
			Hostname:               "host1",
			AuthKey:                "tskey-abc",
			ControlURL:             "https://controlplane.example",
			ExitNode:               "exit-1",
			AutoExitNode:           true,
			ExitNodeAllowLANAccess: true,
			Ephemeral:              true,
		},
		SocksHost: "127.0.0.1",
		SocksPort: 1080,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Result() = %#v, want %#v", got, want)
	}
}

func TestEditFormResultPreservesAutoStartAuthenticatedAndPortForwards(t *testing.T) {
	existing := profile.Profile{
		ID:        "ghi789",
		Kind:      profile.BackendTailscale,
		Name:      "ts-preserved",
		SocksHost: "127.0.0.1",
		SocksPort: 1080,
		AutoStart: true,
		TailscaleConfig: profile.TailscaleConfig{
			Hostname:      "host2",
			Authenticated: true,
			PortForwards: []profile.PortForward{
				{ListenPort: 8080, TargetAddr: "127.0.0.1:80", Protocol: profile.PortForwardTCP},
			},
		},
	}

	form := NewEditForm(existing, false)

	// Simulate a benign edit that doesn't touch AutoStart, Authenticated,
	// or PortForwards via any dedicated control.
	form.name = "ts-preserved-renamed"

	got := form.Result()

	if got.AutoStart != true {
		t.Fatalf("AutoStart = %v, want true (must be preserved from existing profile)", got.AutoStart)
	}
	if got.TailscaleConfig.Authenticated != true {
		t.Fatalf("TailscaleConfig.Authenticated = %v, want true (must be preserved)", got.TailscaleConfig.Authenticated)
	}
	if !reflect.DeepEqual(got.TailscaleConfig.PortForwards, existing.TailscaleConfig.PortForwards) {
		t.Fatalf("PortForwards = %#v, want %#v (must be preserved unchanged)", got.TailscaleConfig.PortForwards, existing.TailscaleConfig.PortForwards)
	}
}

func TestEditFormDefaultsForNewProfile(t *testing.T) {
	form := NewEditForm(profile.Profile{}, true)
	if form.kind != kindWireGuard {
		t.Fatalf("expected default kind %q, got %q", kindWireGuard, form.kind)
	}
	if form.socksHost != profile.DefaultSocksHost {
		t.Fatalf("expected default socks host %q, got %q", profile.DefaultSocksHost, form.socksHost)
	}
	if form.socksPort != "1080" {
		t.Fatalf("expected default socks port \"1080\", got %q", form.socksPort)
	}
}

func TestTrimmedEmpty(t *testing.T) {
	cases := map[string]bool{
		"":     true,
		"   ":  true,
		"\t\n": true,
		"x":    false,
		" x ":  false,
	}
	for input, want := range cases {
		if got := trimmedEmpty(input); got != want {
			t.Errorf("trimmedEmpty(%q) = %v, want %v", input, got, want)
		}
	}
}
