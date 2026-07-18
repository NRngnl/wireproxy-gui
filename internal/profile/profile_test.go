package profile

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const sampleWG = `[Interface]
Address = 10.2.0.2/32
PrivateKey = placeholder-interface-value

[Peer]
PublicKey = placeholder-peer-value
Endpoint = example.com:51820
AllowedIPs = 0.0.0.0/0
`

func TestWireGuardAddressReadsInterfaceAddress(t *testing.T) {
	config := `[Peer]
PublicKey = placeholder-peer-value

[Interface]
PrivateKey = placeholder-interface-value
Address = 10.8.0.2/32, fd00::2/128 # client tunnel IPs
`
	if got, want := WireGuardAddress(config), "10.8.0.2/32, fd00::2/128"; got != want {
		t.Fatalf("WireGuardAddress() = %q, want %q", got, want)
	}
}

func TestWireGuardAddressFallback(t *testing.T) {
	if got := WireGuardAddress("[Interface]\nPrivateKey = placeholder-interface-value\n"); got != "" {
		t.Fatalf("WireGuardAddress() = %q, want empty", got)
	}
}

func TestNormalizePreservesExistingTimestamps(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	item := Profile{
		ID:              "demo",
		Name:            " demo ",
		WireGuardConfig: sampleWG,
		SocksHost:       " 127.0.0.1 ",
		SocksPort:       1080,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}

	item.Normalize()

	if !item.CreatedAt.Equal(created) || !item.UpdatedAt.Equal(updated) {
		t.Fatalf("Normalize changed timestamps: created=%s updated=%s", item.CreatedAt, item.UpdatedAt)
	}
}

func TestNormalizeReplacesUnsafeProfileID(t *testing.T) {
	for _, unsafeID := range []string{"../outside", `..\outside`, ".", ".."} {
		t.Run(unsafeID, func(t *testing.T) {
			item := Profile{
				ID:              unsafeID,
				Name:            "demo",
				WireGuardConfig: sampleWG,
				SocksHost:       DefaultSocksHost,
				SocksPort:       DefaultSocksPort,
			}
			item.Normalize()
			if item.ID == unsafeID || item.ID == "." || item.ID == ".." || strings.ContainsAny(item.ID, `/\`) {
				t.Fatalf("normalized unsafe ID %q to %q", unsafeID, item.ID)
			}
		})
	}
}

func TestTailscaleProfileValidatesWithoutWireGuardConfig(t *testing.T) {
	item := NewTailscale("tailnet", 1080)
	if !item.IsTailscale() {
		t.Fatalf("profile kind = %q, want Tailscale", item.Kind)
	}
	err := item.Validate()
	if err != nil {
		t.Fatal(err)
	}
}

func TestTailscaleProfileRejectsAutomaticAndSpecificExitNode(t *testing.T) {
	item := NewTailscale("tailnet", 1080)
	item.TailscaleConfig.AutoExitNode = true
	item.TailscaleConfig.ExitNode = "exit-a"
	err := item.Validate()
	if !errors.Is(err, ErrTailscaleExitNodeMode) {
		t.Fatalf("expected ErrTailscaleExitNodeMode, got %v", err)
	}
}

func TestTailscaleAuthenticatedProfileClearsAuthKeyOnNormalize(t *testing.T) {
	item := NewTailscale("tailnet", 1080)
	item.TailscaleConfig.Authenticated = true
	item.TailscaleConfig.AuthKey = "tskey-auth-example"
	item.Normalize()
	if item.TailscaleConfig.AuthKey != "" {
		t.Fatalf("authenticated profile auth key = %q, want empty", item.TailscaleConfig.AuthKey)
	}
}

func TestWireGuardConfigAllowsPeerWithoutEndpoint(t *testing.T) {
	config := `[Interface]
Address = 10.2.0.1/32
PrivateKey = placeholder-interface-value

[Peer]
PublicKey = placeholder-peer-value
AllowedIPs = 10.2.0.2/32
`
	err := ValidateWireGuardConfig(config)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWireGuardConfigMissingFieldsUseWireGuardNames(t *testing.T) {
	err := ValidateWireGuardConfig("[Interface]\nAddress = 10.2.0.2/32\n[Peer]\n")
	if !errors.Is(err, ErrWireGuardConfigMissing) {
		t.Fatalf("expected ErrWireGuardConfigMissing, got %v", err)
	}
	for _, want := range []string{"[Interface] PrivateKey", "[Peer] PublicKey"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error, got %q", want, err.Error())
		}
	}
}

func TestNextAvailablePortSkipsUsedPorts(t *testing.T) {
	profiles := []Profile{
		New("first", sampleWG, 1080),
		New("second", sampleWG, 1081),
	}
	if got := NextAvailablePort(profiles); got != 1082 {
		t.Fatalf("NextAvailablePort() = %d, want 1082", got)
	}
}

func TestDuplicateBindAddress(t *testing.T) {
	profiles := []Profile{
		New("first", sampleWG, 1080),
		New("second", sampleWG, 1081),
		New("third", sampleWG, 1080),
	}
	bind, first, second, found := DuplicateBindAddress(profiles)
	if !found || bind != "127.0.0.1:1080" || first != "first" || second != "third" {
		t.Fatalf("unexpected duplicate: found=%t %q %q %q", found, bind, first, second)
	}
}

func TestRuntimeConfigChangedIgnoresNonRuntimeFields(t *testing.T) {
	before := New("demo", sampleWG, 1080)
	after := before
	after.Name = "renamed"
	after.AutoStart = true
	if RuntimeConfigChanged(before, after) {
		t.Fatal("name and startup preference should not require a restart")
	}
}

func TestRuntimeConfigChangedDetectsRestartFields(t *testing.T) {
	wireGuard := New("demo", sampleWG, 1080)
	for name, mutate := range map[string]func(*Profile){
		"bind":             func(item *Profile) { item.SocksPort++ },
		"wireguard config": func(item *Profile) { item.WireGuardConfig += "\n# changed" },
		"backend": func(item *Profile) {
			item.Kind = BackendTailscale
			item.WireGuardConfig = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			after := wireGuard
			mutate(&after)
			if !RuntimeConfigChanged(wireGuard, after) {
				t.Fatalf("%s should require a restart", name)
			}
		})
	}

	tailscale := NewTailscale("tailnet", 1080)
	after := tailscale
	after.TailscaleConfig.AuthKey = "auth"
	if !RuntimeConfigChanged(tailscale, after) {
		t.Fatal("Tailscale auth change should require a restart")
	}
}

func TestRuntimeConfigChangedIgnoresLiveTailscaleFields(t *testing.T) {
	before := NewTailscale("tailnet", 1080)
	after := before
	after.TailscaleConfig.ExitNode = "stable-exit"
	after.TailscaleConfig.ExitNodeAllowLANAccess = true
	after.TailscaleConfig.Authenticated = true
	if RuntimeConfigChanged(before, after) {
		t.Fatal("live exit-node and authenticated state changes should not require a restart")
	}
	if !ExitNodeConfigChanged(before, after) {
		t.Fatal("exit-node preferences should be tracked separately")
	}
}

func TestFieldsChangedIgnoresTimestamps(t *testing.T) {
	before := New("demo", sampleWG, 1080)
	after := before
	after.UpdatedAt = after.UpdatedAt.Add(time.Hour)
	if FieldsChanged(before, after) {
		t.Fatal("timestamps are persistence metadata, not profile field changes")
	}
	after.Name = "renamed"
	if !FieldsChanged(before, after) {
		t.Fatal("name change should be detected")
	}
}
