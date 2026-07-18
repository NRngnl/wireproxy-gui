package profilejson

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

const sampleWG = `[Interface]
Address = 10.2.0.2/32
PrivateKey = placeholder-interface-value

[Peer]
PublicKey = placeholder-peer-value
AllowedIPs = 0.0.0.0/0
`

func TestRepositorySaveDoesNotMutateCallerTimestamps(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	profiles := []profile.Profile{{
		ID:              "demo",
		Name:            "demo",
		WireGuardConfig: sampleWG,
		SocksHost:       profile.DefaultSocksHost,
		SocksPort:       profile.DefaultSocksPort,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}}
	repository := NewRepository(filepath.Join(t.TempDir(), "profiles.json"))

	err := repository.Save(profiles)
	if err != nil {
		t.Fatal(err)
	}
	if !profiles[0].CreatedAt.Equal(created) || !profiles[0].UpdatedAt.Equal(updated) {
		t.Fatalf("Save mutated caller timestamps: %#v", profiles[0])
	}
}

func TestRepositoryRoundTripPreservesTimestampsAndPermissions(t *testing.T) {
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "nested", "profiles.json")
	repository := NewRepository(path)
	err := repository.Save([]profile.Profile{{
		ID:              "demo",
		Name:            "demo",
		WireGuardConfig: sampleWG,
		SocksHost:       profile.DefaultSocksHost,
		SocksPort:       profile.DefaultSocksPort,
		CreatedAt:       created,
		UpdatedAt:       updated,
	}})
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("profile store mode = %o, want 600", got)
	}
	got, err := repository.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].CreatedAt.Equal(created) || !got[0].UpdatedAt.Equal(updated) {
		t.Fatalf("unexpected round trip: %#v", got)
	}
}

func TestRepositoryRoundTripPreservesTailscaleDomainFields(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	item.AutoStart = true
	item.TailscaleConfig = profile.TailscaleConfig{
		Hostname:               "proxy-node",
		AuthKey:                "tskey-auth-example",
		ControlURL:             "https://control.example.com",
		ExitNode:               "stable-exit",
		ExitNodeAllowLANAccess: true,
		Ephemeral:              true,
	}
	repository := NewRepository(filepath.Join(t.TempDir(), "profiles.json"))
	err := repository.Save([]profile.Profile{item})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repository.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != item {
		t.Fatalf("Tailscale round trip = %#v, want %#v", got, item)
	}
}

func TestRepositoryLoadMissingFileIsEmpty(t *testing.T) {
	repository := NewRepository(filepath.Join(t.TempDir(), "profiles.json"))
	got, err := repository.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("missing repository returned %#v", got)
	}
}

func TestRepositoryLoadReportsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	err := os.WriteFile(path, []byte("{"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRepository(path).Load()
	if err == nil || !strings.HasPrefix(err.Error(), "load profiles: ") {
		t.Fatalf("Load error = %v, want load profiles prefix", err)
	}
}

func TestCodecDecodeImportAcceptsBundleAndDraft(t *testing.T) {
	want := []profile.Profile{profile.New("draft", "", 1080)}
	data, err := EncodeBundle(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Codec{}).DecodeImport("profiles.json", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "draft" {
		t.Fatalf("unexpected profiles: %#v", got)
	}
}

func TestCodecDecodeImportAcceptsWireGuardConfig(t *testing.T) {
	got, err := (Codec{}).DecodeImport("office.conf", []byte(sampleWG))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "office" || !got[0].IsWireGuard() {
		t.Fatalf("unexpected profiles: %#v", got)
	}
}

func TestCodecEncodeExportClearsLocalTailscaleAuthentication(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	item.TailscaleConfig.Authenticated = true
	item.TailscaleConfig.AuthKey = "tskey-auth-example"
	data, err := (Codec{}).EncodeExport([]profile.Profile{item})
	if err != nil {
		t.Fatal(err)
	}
	var stored bundle
	err = json.Unmarshal(data, &stored)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Profiles) != 1 || stored.Profiles[0].TailscaleConfig == nil || stored.Profiles[0].TailscaleConfig.Authenticated || stored.Profiles[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("export leaked local authentication state: %#v", stored.Profiles)
	}
}

func TestCodecDecodeImportRejectsAuthenticatedOnlyTailscaleProfile(t *testing.T) {
	_, err := (Codec{}).DecodeImport("profiles.json", []byte(`{
		"kind":"tailscale",
		"tailscale_config":{"authenticated":true}
	}`))
	if !errors.Is(err, application.ErrImportProfilesEmpty) {
		t.Fatalf("expected ErrImportProfilesEmpty, got %v", err)
	}
}

func TestCodecLegacyProfileDefaultsToWireGuard(t *testing.T) {
	data := []byte(`{
		"id":"legacy",
		"name":"legacy",
		"wireguard_config":"` + strings.ReplaceAll(sampleWG, "\n", `\n`) + `",
		"socks_host":"127.0.0.1",
		"socks_port":1080
	}`)
	profiles, err := (Codec{}).DecodeImport("profiles.json", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || !profiles[0].IsWireGuard() {
		t.Fatalf("legacy profiles = %#v", profiles)
	}
	profiles[0].Normalize()
	if profiles[0].Kind != profile.BackendWireGuard {
		t.Fatalf("legacy kind = %q", profiles[0].Kind)
	}
}

func TestCodecDecodeImportErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want error
	}{
		{name: "empty", data: "", want: application.ErrImportFileEmpty},
		{name: "empty bundle", data: `{"profiles":[]}`, want: application.ErrImportProfilesEmpty},
		{name: "empty array item", data: `[{}]`, want: application.ErrImportProfilesEmpty},
		{name: "non profile array", data: `[1]`, want: application.ErrImportProfilesEmpty},
		{name: "invalid json", data: `{"profiles":`, want: application.ErrImportJSONInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Codec{}).DecodeImport("bad.json", []byte(test.data))
			if !errors.Is(err, test.want) {
				t.Fatalf("DecodeImport() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBundleEncodingUsesVersionOne(t *testing.T) {
	data, err := EncodeBundle([]profile.Profile{profile.New("demo", sampleWG, 1080)})
	if err != nil {
		t.Fatal(err)
	}
	var stored bundle
	err = json.Unmarshal(data, &stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 || len(stored.Profiles) != 1 {
		t.Fatalf("unexpected bundle: %#v", stored)
	}
	text := string(data)
	if !strings.Contains(text, `"wireguard_config"`) || strings.Contains(text, `"WireGuardConfig"`) {
		t.Fatalf("adapter did not preserve JSON schema: %s", text)
	}
	if strings.Contains(text, `"tailscale_config"`) {
		t.Fatalf("zero Tailscale adapter state should be omitted: %s", text)
	}
}
