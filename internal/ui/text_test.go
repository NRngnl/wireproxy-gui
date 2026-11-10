package ui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

const sampleWireGuardConfig = `[Interface]
Address = 10.2.0.2/32
PrivateKey = placeholder-interface-value

[Peer]
PublicKey = placeholder-peer-value
AllowedIPs = 0.0.0.0/0
`

var errTestContext = errors.New("context")
var errLoadProfilesInvalid = errors.New("load profiles: invalid JSON")

func TestStatusSummaryUsesUserFacingStatus(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	if got, want := statusSummaryText(item, "running"), "Connected · SOCKS5 127.0.0.1:1080"; got != want {
		t.Fatalf("status summary = %q", got)
	}
}

func TestProfilesSummaryReportsConnectedCount(t *testing.T) {
	first := profile.New("first", sampleWireGuardConfig, 1080)
	second := profile.New("second", sampleWireGuardConfig, 1081)
	statuses := map[string]string{first.ID: "running", second.ID: "stopped"}
	got := profilesSummaryText([]profile.Profile{first, second}, func(id string) string { return statuses[id] })
	if got != "2 profiles · 1 connected" {
		t.Fatalf("profiles summary = %q", got)
	}
	if got := profilesSummaryText([]profile.Profile{first}, func(string) string { return "stopped" }); got != "1 profile · 0 connected" {
		t.Fatalf("singular profiles summary = %q", got)
	}
	if got := profilesSummaryText(nil, func(string) string { return "stopped" }); got != "No profiles" {
		t.Fatalf("empty profiles summary = %q", got)
	}
}

func TestProfileLogTextFormatsAndLocalizesEntries(t *testing.T) {
	at := time.Date(2025, 1, 1, 12, 34, 56, 0, time.UTC)
	withTranslator(t, func(message string, _ ...any) string {
		if message == "disconnected" {
			return "DISCONNECTED"
		}
		return message
	})
	got := profileLogText([]application.LogEntry{{At: at, Message: "disconnected"}})
	if got != "12:34:56  DISCONNECTED" {
		t.Fatalf("profileLogText() = %q", got)
	}
}

func TestOperationErrorTitleKeepsPresentationLabelsOutOfCore(t *testing.T) {
	for operation, want := range map[application.Operation]string{
		application.OperationConnect:    "Connect profile",
		application.OperationConnectAll: "Connect All",
		application.OperationLogin:      "Login to Tailscale",
		application.OperationSave:       "Save profile",
		application.OperationAutoStart:  "",
	} {
		if got := operationErrorTitle(operation); got != want {
			t.Fatalf("title for %q = %q, want %q", operation, got, want)
		}
	}
}

func TestUsageGuideMentionsEmbeddedRunnerAndNativePaths(t *testing.T) {
	guide := usageGuideText()
	for _, want := range []string{
		"embedded WireGuard engine", "embedded tsnet node", "does not launch the wireproxy",
		"browser sign-in URL", "approve the device in the admin console", "removes the saved auth key",
		"Exit-node mode", "profile log follows the newest line", "operating system's native file dialog",
		"Tailscale node state and the local authenticated marker are not exported",
	} {
		if !strings.Contains(guide, want) {
			t.Fatalf("usage guide is missing %q", want)
		}
	}
}

func TestUsageGuideUsesTranslator(t *testing.T) {
	withTranslator(t, func(message string, _ ...any) string {
		if message == usageGuideParagraphs[0] {
			return "TRANSLATED INTRO"
		}
		return message
	})
	if guide := usageGuideText(); !strings.Contains(guide, "TRANSLATED INTRO") {
		t.Fatalf("usage guide did not use translator: %s", guide)
	}
}

func TestLocalizedErrorTextUsesApplicationAndDomainErrors(t *testing.T) {
	withTranslator(t, func(message string, _ ...any) string {
		switch message {
		case application.ErrRuntimeProfileEdit.Error():
			return "RUNTIME_EDIT_L10N"
		case profile.ErrSocksPortNotNumber.Error():
			return "PORT_ERROR_L10N"
		default:
			return message
		}
	})
	err := localizedErrorText(errors.Join(application.ErrRuntimeProfileEdit, errTestContext, profile.ErrSocksPortNotNumber))
	if !strings.Contains(err, "RUNTIME_EDIT_L10N") || !strings.Contains(err, "PORT_ERROR_L10N") {
		t.Fatalf("localized error = %q", err)
	}
}

func TestLocalizedTextHandlesStructuredMessages(t *testing.T) {
	withTranslator(t, func(message string, data ...any) string {
		switch message {
		case "connected on {{.Address}}":
			return "CONNECTED " + data[0].(map[string]any)["Address"].(string)
		case "update Tailscale exit node: {{.Message}}":
			return "UPDATE " + data[0].(map[string]any)["Message"].(string)
		default:
			return message
		}
	})
	if got := localizedText("connected on 127.0.0.1:1080"); got != "CONNECTED 127.0.0.1:1080" {
		t.Fatalf("localized connected message = %q", got)
	}
	if got := localizedText("update Tailscale exit node: failed"); got != "UPDATE failed" {
		t.Fatalf("localized update message = %q", got)
	}
}

func TestDisplayErrorTextAvoidsDuplicateTitlePrefix(t *testing.T) {
	if got, want := displayErrorText("Load profiles", errLoadProfilesInvalid), "Load profiles: invalid JSON"; got != want {
		t.Fatalf("display error text = %q, want %q", got, want)
	}
}

func TestEnglishCatalogCoversBoundaryMessages(t *testing.T) {
	data, err := appTranslations.ReadFile("translations/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]string
	err = json.Unmarshal(data, &catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		application.ErrRuntimeProfileEdit.Error(), application.ErrRuntimeExitNodeEdit.Error(),
		application.ErrImportFileEmpty.Error(), application.ErrImportJSONInvalid.Error(),
		"Login", "Logout", "Authenticated", "updated Tailscale exit-node settings",
		"invalid Tailscale profile ID", "profile is not a Tailscale profile",
		"exit nodes are only available for running Tailscale profiles",
		"duplicate SOCKS5 bind address: {{.Address}} is already used by active profile \"{{.Name}}\"",
	} {
		if _, ok := catalog[key]; !ok {
			t.Fatalf("English catalog is missing %q", key)
		}
	}
}

func withTranslator(t *testing.T, fn translateFunc) {
	t.Helper()
	original := translate
	translate = fn
	t.Cleanup(func() { translate = original })
}
