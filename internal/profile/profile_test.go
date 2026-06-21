package profile

import (
	"encoding/json"
	"errors"
	"path/filepath"
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

func TestWireproxyConfigReplacesSocks5Section(t *testing.T) {
	p := New("demo", sampleWG+"\n[Socks5]\nBindAddress = 127.0.0.1:9999\n", 1085)

	got := p.WireproxyConfig()
	if strings.Count(got, "[Socks5]") != 1 {
		t.Fatalf("expected one Socks5 section, got:\n%s", got)
	}
	if !strings.Contains(got, "BindAddress = 127.0.0.1:1085") {
		t.Fatalf("expected generated bind address, got:\n%s", got)
	}
	if strings.Contains(got, "9999") {
		t.Fatalf("expected old bind port to be stripped, got:\n%s", got)
	}
}

func TestWireGuardAddressReadsInterfaceAddress(t *testing.T) {
	config := `[Peer]
PublicKey = placeholder-peer-value

[Interface]
PrivateKey = placeholder-interface-value
Address = 10.8.0.2/32, fd00::2/128 # client tunnel IPs
`

	got := WireGuardAddress(config)
	want := "10.8.0.2/32, fd00::2/128"
	if got != want {
		t.Fatalf("WireGuardAddress() = %q, want %q", got, want)
	}
}

func TestWireGuardAddressFallback(t *testing.T) {
	got := WireGuardAddress("[Interface]\nPrivateKey = placeholder-interface-value\n")
	if got != "not configured" {
		t.Fatalf("WireGuardAddress() = %q, want not configured", got)
	}
}

func TestNormalizePreservesExistingTimestamps(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	p := Profile{
		ID:              "demo",
		Name:            " demo ",
		WireGuardConfig: sampleWG,
		SocksHost:       " 127.0.0.1 ",
		SocksPort:       1080,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}

	p.Normalize()

	if !p.CreatedAt.Equal(created) {
		t.Fatalf("Normalize changed CreatedAt: got %s want %s", p.CreatedAt, created)
	}
	if !p.UpdatedAt.Equal(updated) {
		t.Fatalf("Normalize changed UpdatedAt: got %s want %s", p.UpdatedAt, updated)
	}
}

func TestNormalizeReplacesUnsafeProfileID(t *testing.T) {
	for _, unsafeID := range []string{"../outside", `..\outside`, ".", ".."} {
		t.Run(unsafeID, func(t *testing.T) {
			p := Profile{
				ID:              unsafeID,
				Name:            "demo",
				WireGuardConfig: sampleWG,
				SocksHost:       DefaultSocksHost,
				SocksPort:       DefaultSocksPort,
			}

			p.Normalize()

			if p.ID == unsafeID || p.ID == "." || p.ID == ".." || strings.ContainsAny(p.ID, `/\`) {
				t.Fatalf("normalized unsafe ID %q to %q", unsafeID, p.ID)
			}
		})
	}
}

func TestStoreSaveDoesNotMutateCallerTimestamps(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	profiles := []Profile{{
		ID:              "demo",
		Name:            "demo",
		WireGuardConfig: sampleWG,
		SocksHost:       DefaultSocksHost,
		SocksPort:       DefaultSocksPort,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}}
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))

	err := store.Save(profiles)
	if err != nil {
		t.Fatal(err)
	}

	if !profiles[0].CreatedAt.Equal(created) {
		t.Fatalf("Save changed caller CreatedAt: got %s want %s", profiles[0].CreatedAt, created)
	}
	if !profiles[0].UpdatedAt.Equal(updated) {
		t.Fatalf("Save changed caller UpdatedAt: got %s want %s", profiles[0].UpdatedAt, updated)
	}
}

func TestStoreLoadPreservesTimestamps(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	err := store.Save([]Profile{{
		ID:              "demo",
		Name:            "demo",
		WireGuardConfig: sampleWG,
		SocksHost:       DefaultSocksHost,
		SocksPort:       DefaultSocksPort,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one profile, got %#v", got)
	}
	if !got[0].CreatedAt.Equal(created) {
		t.Fatalf("Load changed CreatedAt: got %s want %s", got[0].CreatedAt, created)
	}
	if !got[0].UpdatedAt.Equal(updated) {
		t.Fatalf("Load changed UpdatedAt: got %s want %s", got[0].UpdatedAt, updated)
	}
}

func TestDecodeImportAcceptsBundle(t *testing.T) {
	want := []Profile{New("demo", sampleWG, 1080)}
	data, err := EncodeBundle(want)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeImport("profiles.json", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "demo" {
		t.Fatalf("unexpected profiles: %#v", got)
	}
}

func TestDecodeImportAcceptsExportedDraftProfile(t *testing.T) {
	want := []Profile{New("draft", "", 1080)}
	data, err := EncodeBundle(want)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeImport("profiles.json", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "draft" {
		t.Fatalf("unexpected draft profiles: %#v", got)
	}
}

func TestLegacyProfileDefaultsToWireGuard(t *testing.T) {
	var p Profile
	err := json.Unmarshal([]byte(`{
		"id":"legacy",
		"name":"legacy",
		"wireguard_config":"`+strings.ReplaceAll(sampleWG, "\n", `\n`)+`",
		"socks_host":"127.0.0.1",
		"socks_port":1080
	}`), &p)
	if err != nil {
		t.Fatal(err)
	}

	p.Normalize()

	if !p.IsWireGuard() || p.Kind != BackendWireGuard {
		t.Fatalf("legacy profile kind = %q, want WireGuard", p.Kind)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTailscaleProfileValidatesWithoutWireGuardConfig(t *testing.T) {
	p := NewTailscale("tailnet", 1080)

	if !p.IsTailscale() {
		t.Fatalf("profile kind = %q, want Tailscale", p.Kind)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTailscaleProfileRejectsAutomaticAndSpecificExitNode(t *testing.T) {
	p := NewTailscale("tailnet", 1080)
	p.TailscaleConfig.AutoExitNode = true
	p.TailscaleConfig.ExitNode = "exit-a"

	err := p.Validate()
	if !errors.Is(err, ErrTailscaleExitNodeMode) {
		t.Fatalf("expected ErrTailscaleExitNodeMode, got %v", err)
	}
}

func TestTailscaleAuthenticatedProfileClearsAuthKeyOnNormalize(t *testing.T) {
	p := NewTailscale("tailnet", 1080)
	p.TailscaleConfig.Authenticated = true
	p.TailscaleConfig.AuthKey = "tskey-auth-example"

	p.Normalize()

	if p.TailscaleConfig.AuthKey != "" {
		t.Fatalf("authenticated profile auth key = %q, want empty", p.TailscaleConfig.AuthKey)
	}
}

func TestEncodeExportBundleClearsTailscaleAuthenticatedState(t *testing.T) {
	p := NewTailscale("tailnet", 1080)
	p.TailscaleConfig.Authenticated = true
	p.TailscaleConfig.AuthKey = "tskey-auth-example"

	data, err := EncodeExportBundle([]Profile{p})
	if err != nil {
		t.Fatal(err)
	}

	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Profiles) != 1 {
		t.Fatalf("exported profile count = %d, want 1", len(bundle.Profiles))
	}
	if bundle.Profiles[0].TailscaleConfig.Authenticated {
		t.Fatal("exported Tailscale profile should not include authenticated state")
	}
	if bundle.Profiles[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("exported authenticated auth key = %q, want empty", bundle.Profiles[0].TailscaleConfig.AuthKey)
	}
}

func TestDecodeImportAcceptsExportedTailscaleProfile(t *testing.T) {
	want := NewTailscale("tailnet", 1080)
	want.TailscaleConfig.Hostname = "wireproxy-gui"
	data, err := EncodeBundle([]Profile{want})
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeImport("profiles.json", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsTailscale() || got[0].TailscaleConfig.Hostname != "wireproxy-gui" {
		t.Fatalf("unexpected profiles: %#v", got)
	}
}

func TestPrepareImportedClearsTailscaleAuthenticatedState(t *testing.T) {
	imported := NewTailscale("tailnet", 1080)
	imported.TailscaleConfig.Authenticated = true
	imported.TailscaleConfig.AuthKey = "tskey-auth-example"

	got := PrepareImported([]Profile{imported}, nil)

	if len(got) != 1 {
		t.Fatalf("expected one imported profile, got %#v", got)
	}
	if got[0].TailscaleConfig.Authenticated {
		t.Fatal("imported profile should not stay authenticated because tsnet state is not imported")
	}
	if got[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("imported authenticated profile auth key = %q, want empty", got[0].TailscaleConfig.AuthKey)
	}
}

func TestDecodeImportRejectsAuthenticatedOnlyTailscaleProfile(t *testing.T) {
	_, err := DecodeImport("profiles.json", []byte(`{
		"kind":"tailscale",
		"tailscale_config":{"authenticated":true}
	}`))
	if !errors.Is(err, ErrImportProfilesEmpty) {
		t.Fatalf("expected ErrImportProfilesEmpty, got %v", err)
	}
}

func TestDecodeImportAcceptsWireGuardConfig(t *testing.T) {
	got, err := DecodeImport("demo.conf", []byte(sampleWG))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "demo" {
		t.Fatalf("unexpected profiles: %#v", got)
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

func TestDecodeImportRejectsUnknownJSON(t *testing.T) {
	_, err := DecodeImport("bad.json", []byte(`{"profiles":[]}`))
	if !errors.Is(err, ErrImportProfilesEmpty) {
		t.Fatalf("expected ErrImportProfilesEmpty, got %v", err)
	}
}

func TestDecodeImportRejectsInvalidJSONClearly(t *testing.T) {
	_, err := DecodeImport("bad.json", []byte(`{"profiles":`))
	if !errors.Is(err, ErrImportJSONInvalid) {
		t.Fatalf("expected ErrImportJSONInvalid, got %v", err)
	}
}

func TestDecodeImportRejectsNonProfileJSONArray(t *testing.T) {
	_, err := DecodeImport("bad.json", []byte(`[1]`))
	if !errors.Is(err, ErrImportProfilesEmpty) {
		t.Fatalf("expected ErrImportProfilesEmpty, got %v", err)
	}
}

func TestDecodeImportRejectsEmptyJSONProfiles(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`[{}]`),
		[]byte(`{"profiles":[{}]}`),
	} {
		_, err := DecodeImport("bad.json", data)
		if !errors.Is(err, ErrImportProfilesEmpty) {
			t.Fatalf("expected ErrImportProfilesEmpty for %s, got %v", data, err)
		}
	}
}

func TestPrepareImportedNamesUnnamedProfiles(t *testing.T) {
	imported := []Profile{{
		WireGuardConfig: sampleWG,
		SocksPort:       1080,
	}}

	got := PrepareImported(imported, nil)
	if len(got) != 1 {
		t.Fatalf("expected one prepared profile, got %#v", got)
	}
	if got[0].Name != "Imported profile" {
		t.Fatalf("unnamed imported profile name = %q, want Imported profile", got[0].Name)
	}
}

func TestPrepareImportedNeverAssignsOutOfRangePort(t *testing.T) {
	existing := make([]Profile, 0, 65535)
	for port := 1; port <= 65535; port++ {
		existing = append(existing, Profile{
			ID:              NewID(),
			Name:            "used",
			WireGuardConfig: sampleWG,
			SocksHost:       DefaultSocksHost,
			SocksPort:       port,
		})
	}
	imported := []Profile{{
		Name:            "imported",
		WireGuardConfig: sampleWG,
		SocksPort:       70000,
	}}

	got := PrepareImported(imported, existing)
	if len(got) != 1 {
		t.Fatalf("expected one prepared profile, got %#v", got)
	}
	if got[0].SocksPort < 1 || got[0].SocksPort > 65535 {
		t.Fatalf("imported port = %d, want valid SOCKS port range", got[0].SocksPort)
	}
}

func TestDuplicateBindAddress(t *testing.T) {
	profiles := []Profile{
		New("first", sampleWG, 1080),
		New("second", sampleWG, 1081),
		New("third", sampleWG, 1080),
	}

	bind, first, second, found := DuplicateBindAddress(profiles)
	if !found {
		t.Fatal("expected duplicate bind address")
	}
	if bind != "127.0.0.1:1080" || first != "first" || second != "third" {
		t.Fatalf("unexpected duplicate: %q %q %q", bind, first, second)
	}
}

func TestBundleEncoding(t *testing.T) {
	data, err := EncodeBundle([]Profile{New("demo", sampleWG, 1080)})
	if err != nil {
		t.Fatal(err)
	}
	var bundle Bundle
	err = json.Unmarshal(data, &bundle)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Version != 1 || len(bundle.Profiles) != 1 {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
}
