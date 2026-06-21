package ui

import (
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/ncruces/zenity"

	"example.com/wireproxy-gui/internal/connection"
	"example.com/wireproxy-gui/internal/profile"
	"example.com/wireproxy-gui/internal/wireproxy"
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
var uiTestMu sync.Mutex

func TestApplyAppThemeUsesNativeTheme(t *testing.T) {
	app := fynetest.NewTempApp(t)

	applyAppTheme(app)

	if _, ok := app.Settings().Theme().(nativeTheme); !ok {
		t.Fatalf("app theme = %T, want nativeTheme", app.Settings().Theme())
	}
}

func TestNativeThemeUsesMacOSSystemBlueAccent(t *testing.T) {
	th := newNativeTheme()

	tests := []struct {
		name    string
		variant fyne.ThemeVariant
		want    color.NRGBA
	}{
		{name: "light", variant: theme.VariantLight, want: color.NRGBA{R: 0x00, G: 0x7a, B: 0xff, A: 0xff}},
		{name: "dark", variant: theme.VariantDark, want: color.NRGBA{R: 0x0a, G: 0x84, B: 0xff, A: 0xff}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := th.Color(theme.ColorNamePrimary, tt.variant); got != tt.want {
				t.Fatalf("primary color = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNativeThemeDefinesAllFyneColorNames(t *testing.T) {
	fynetest.AssertAllColorNamesDefined(t, newNativeTheme(), "native")
}

func TestNativeThemeDelegatesIconsFontsAndSizes(t *testing.T) {
	th := newNativeTheme()
	fallback := theme.DefaultTheme()

	if th.Icon(theme.IconNameConfirm).Name() != fallback.Icon(theme.IconNameConfirm).Name() {
		t.Fatal("native theme should delegate icons to the Fyne default theme")
	}
	if th.Font(fyne.TextStyle{}).Name() != fallback.Font(fyne.TextStyle{}).Name() {
		t.Fatal("native theme should delegate fonts to the Fyne default theme")
	}
	if got, want := th.Size(theme.SizeNameText), fallback.Size(theme.SizeNameText); got != want {
		t.Fatalf("native theme text size = %v, want %v", got, want)
	}
}

func TestRuntimeConfigChangedIgnoresNonRuntimeFields(t *testing.T) {
	before := profile.New("demo", sampleWireGuardConfig, 1080)
	after := before
	after.Name = "renamed"
	after.AutoStart = true

	if runtimeConfigChanged(before, after) {
		t.Fatal("name and startup preference should not be runtime config changes")
	}
}

func TestRuntimeConfigChangedDetectsSocksBindAddress(t *testing.T) {
	before := profile.New("demo", sampleWireGuardConfig, 1080)
	after := before
	after.SocksPort = 1081

	if !runtimeConfigChanged(before, after) {
		t.Fatal("SOCKS5 bind address change should be a runtime config change")
	}
}

func TestRuntimeConfigChangedDetectsWireGuardConfig(t *testing.T) {
	before := profile.New("demo", sampleWireGuardConfig, 1080)
	after := before
	after.WireGuardConfig += "\n# changed"

	if !runtimeConfigChanged(before, after) {
		t.Fatal("WireGuard config change should be a runtime config change")
	}
}

func TestRuntimeConfigChangedDetectsTailscaleConfig(t *testing.T) {
	before := profile.NewTailscale("demo", 1080)
	after := before
	after.TailscaleConfig.AuthKey = "tskey-auth-example"

	if !runtimeConfigChanged(before, after) {
		t.Fatal("Tailscale auth change should be a runtime config change")
	}
}

func TestRuntimeConfigChangedIgnoresTailscaleExitNodePrefs(t *testing.T) {
	before := profile.NewTailscale("demo", 1080)
	after := before
	after.TailscaleConfig.ExitNode = "stable-exit"
	after.TailscaleConfig.ExitNodeAllowLANAccess = true

	if runtimeConfigChanged(before, after) {
		t.Fatal("Tailscale exit-node pref change should not be a restart-required runtime config change")
	}
	if !tailscaleExitNodeConfigChanged(before, after) {
		t.Fatal("Tailscale exit-node pref change should be tracked separately")
	}
}

func TestRuntimeConfigChangedIgnoresTailscaleAuthenticatedFlag(t *testing.T) {
	before := profile.NewTailscale("demo", 1080)
	after := before
	after.TailscaleConfig.Authenticated = true

	if runtimeConfigChanged(before, after) {
		t.Fatal("Tailscale authenticated flag should not be a restart-required runtime config change")
	}
}

func TestRuntimeConfigChangedDetectsBackendKind(t *testing.T) {
	before := profile.New("demo", sampleWireGuardConfig, 1080)
	after := before
	after.Kind = profile.BackendTailscale
	after.WireGuardConfig = ""

	if !runtimeConfigChanged(before, after) {
		t.Fatal("backend kind change should be a runtime config change")
	}
}

func TestImportProfilesUsesNativeSelectedPath(t *testing.T) {
	importPath := filepath.Join(t.TempDir(), "office.conf")
	err := os.WriteFile(importPath, []byte(sampleWireGuardConfig), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	files := &fakeProfileFileDialog{openPath: importPath}
	gui := newProfilesTestGUI(t)
	gui.files = files

	gui.importProfiles()

	if files.openCalls != 1 {
		t.Fatalf("expected one native open dialog call, got %d", files.openCalls)
	}
	if len(gui.profiles) != 1 {
		t.Fatalf("expected one imported profile, got %#v", gui.profiles)
	}
	if gui.profiles[0].Name != "office" {
		t.Fatalf("expected imported profile name from path, got %q", gui.profiles[0].Name)
	}
	saved, err := gui.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Name != "office" {
		t.Fatalf("expected imported profile to be saved, got %#v", saved)
	}
}

func TestImportProfilesIgnoresCanceledNativePath(t *testing.T) {
	files := &fakeProfileFileDialog{}
	gui := newProfilesTestGUI(t)
	gui.files = files

	gui.importProfiles()

	if files.openCalls != 1 {
		t.Fatalf("expected one native open dialog call, got %d", files.openCalls)
	}
	if len(gui.profiles) != 0 {
		t.Fatalf("canceled import should not change profiles: %#v", gui.profiles)
	}
}

func TestExportProfilesUsesNativeSelectedPath(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	exportPath := filepath.Join(t.TempDir(), "profiles.json")
	files := &fakeProfileFileDialog{savePath: exportPath}
	gui := newProfilesTestGUI(t, p)
	gui.files = files

	gui.exportProfiles([]profile.Profile{p}, "demo.json")

	if files.saveCalls != 1 {
		t.Fatalf("expected one native save dialog call, got %d", files.saveCalls)
	}
	if files.saveFileName != "demo.json" {
		t.Fatalf("expected default export name, got %q", files.saveFileName)
	}
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := profile.DecodeImport(filepath.Base(exportPath), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 || exported[0].Name != "demo" {
		t.Fatalf("unexpected exported profiles: %#v", exported)
	}
}

func TestExportProfilesClearsTailscaleAuthenticatedState(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	p.TailscaleConfig.Authenticated = true
	p.TailscaleConfig.AuthKey = "tskey-auth-example"
	exportPath := filepath.Join(t.TempDir(), "profiles.json")

	err := exportProfilesToPath([]profile.Profile{p}, exportPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle profile.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Profiles) != 1 {
		t.Fatalf("exported profile count = %d, want 1", len(bundle.Profiles))
	}
	if bundle.Profiles[0].TailscaleConfig.Authenticated {
		t.Fatal("exported Tailscale profile should not keep authenticated state")
	}
	if bundle.Profiles[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("exported authenticated auth key = %q, want empty", bundle.Profiles[0].TailscaleConfig.AuthKey)
	}
}

func TestExportProfilesRestrictsExistingFilePermissions(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	exportPath := filepath.Join(t.TempDir(), "profiles.json")
	err := os.WriteFile(exportPath, []byte("old"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	err = exportProfilesToPath([]profile.Profile{p}, exportPath)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected exported profile permissions 0600, got %o", got)
	}
}

func TestSelectedPathTreatsNativeCancelAsNoop(t *testing.T) {
	path, err := selectedPath("ignored", zenity.ErrCanceled)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("expected canceled path to be empty, got %q", path)
	}
}

func TestSaveSelectedProfileLeavesUnchangedProfileUntouched(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	p.UpdatedAt = updated
	gui := newProfileTestGUI(t, p)

	err := gui.saveSelectedProfile()
	if err != nil {
		t.Fatal(err)
	}
	if !gui.profiles[0].UpdatedAt.Equal(updated) {
		t.Fatalf("unchanged save touched UpdatedAt: got %s want %s", gui.profiles[0].UpdatedAt, updated)
	}
	if len(gui.logs[p.ID]) != 0 {
		t.Fatalf("unchanged save should not add a log entry: %#v", gui.logs[p.ID])
	}
}

func TestSaveSelectedProfileTouchesChangedProfile(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	updated := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	p.UpdatedAt = updated
	gui := newProfileTestGUI(t, p)
	gui.nameEntry.SetText("renamed")

	err := gui.saveSelectedProfile()
	if err != nil {
		t.Fatal(err)
	}
	if !gui.profiles[0].UpdatedAt.After(updated) {
		t.Fatalf("changed save did not touch UpdatedAt: got %s want after %s", gui.profiles[0].UpdatedAt, updated)
	}
	if len(gui.logs[p.ID]) != 1 {
		t.Fatalf("changed save should add one log entry: %#v", gui.logs[p.ID])
	}
}

func TestSaveSelectedProfileBlocksRuntimeConfigEditWhileRunning(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.statuses[p.ID] = "running"
	gui.portEntry.SetText("1081")

	err := gui.saveSelectedProfile()
	if !errors.Is(err, errRuntimeProfileEdit) {
		t.Fatalf("expected errRuntimeProfileEdit, got %v", err)
	}
	if gui.profiles[0].SocksPort != 1080 {
		t.Fatalf("runtime edit should not be saved: %#v", gui.profiles[0])
	}
}

func TestRuntimeConfigEditErrorMentionsDisconnectingWait(t *testing.T) {
	if !strings.Contains(errRuntimeProfileEdit.Error(), "wait for it to finish disconnecting") {
		t.Fatalf("runtime edit error should mention disconnecting wait: %q", errRuntimeProfileEdit.Error())
	}
	if !strings.Contains(errRuntimeProfileEdit.Error(), "Tailscale auth settings") {
		t.Fatalf("runtime edit error should mention locked Tailscale fields: %q", errRuntimeProfileEdit.Error())
	}
}

func TestRuntimeExitNodeEditErrorMentionsWait(t *testing.T) {
	if !strings.Contains(errRuntimeExitNodeEdit.Error(), "connected or disconnected") {
		t.Fatalf("runtime exit-node edit error should mention valid states: %q", errRuntimeExitNodeEdit.Error())
	}
}

func TestSaveSelectedProfileAllowsNonRuntimeEditWhileRunning(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.statuses[p.ID] = "running"
	gui.nameEntry.SetText("renamed")
	gui.autoStartCheck.SetChecked(true)

	err := gui.saveSelectedProfile()
	if err != nil {
		t.Fatal(err)
	}
	if gui.profiles[0].Name != "renamed" || !gui.profiles[0].AutoStart {
		t.Fatalf("non-runtime edit was not saved: %#v", gui.profiles[0])
	}
	if gui.profiles[0].SocksPort != 1080 {
		t.Fatalf("runtime fields should be unchanged: %#v", gui.profiles[0])
	}
}

func TestSaveSelectedProfileUpdatesRunningTailscaleExitNode(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	runner := newFakeRunner()
	runner.setRunning(p.ID, true)
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()
	gui.statuses[p.ID] = "running"
	gui.tsExitNode.SetText("stable-exit")
	gui.tsAllowLAN.SetChecked(true)

	err := gui.saveSelectedProfile()
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.updateExitNodeCallCount(); got != 1 {
		t.Fatalf("UpdateExitNode calls = %d, want 1", got)
	}
	updatedID, updatedConfig := runner.updatedExitNodeAt(0)
	if updatedID != p.ID {
		t.Fatalf("UpdateExitNode profile ID = %q, want %q", updatedID, p.ID)
	}
	if updatedConfig.ExitNode != "stable-exit" || !updatedConfig.ExitNodeAllowLANAccess {
		t.Fatalf("UpdateExitNode config = %#v", updatedConfig)
	}
	if gui.profiles[0].TailscaleConfig.ExitNode != "stable-exit" {
		t.Fatalf("saved exit node = %#v", gui.profiles[0].TailscaleConfig)
	}
	if got := strings.Join(gui.logs[p.ID], "\n"); !strings.Contains(got, "updated Tailscale exit-node settings") {
		t.Fatalf("expected dynamic update log, got %q", got)
	}
}

func TestSaveSelectedProfileBlocksExitNodeChangeWhileStarting(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	runner := newFakeRunner()
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.statuses[p.ID] = "starting"
	gui.tsExitNode.SetText("stable-exit")

	err := gui.saveSelectedProfile()
	if !errors.Is(err, errRuntimeExitNodeEdit) {
		t.Fatalf("expected errRuntimeExitNodeEdit, got %v", err)
	}
	if got := runner.updateExitNodeCallCount(); got != 0 {
		t.Fatalf("UpdateExitNode calls = %d, want 0", got)
	}
	if gui.profiles[0].TailscaleConfig.ExitNode != "" {
		t.Fatalf("starting exit-node edit should not be saved: %#v", gui.profiles[0].TailscaleConfig)
	}
}

func TestProfileFromFormReturnsCleanPortError(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.portEntry.SetText("not-a-number")

	_, err := gui.profileFromForm(p)
	if !errors.Is(err, profile.ErrSocksPortNotNumber) {
		t.Fatalf("expected ErrSocksPortNotNumber, got %v", err)
	}
	if err.Error() != profile.ErrSocksPortNotNumber.Error() {
		t.Fatalf("expected clean port error, got %q", err.Error())
	}
}

func TestProfileFromFormBuildsTailscaleProfile(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.kindSelect.SetSelected(tr("Tailscale"))
	gui.tsHostname.SetText("ts-demo")
	gui.tsAuthKey.SetText("auth")
	gui.tsControlURL.SetText("https://control.example.com")
	gui.tsAutoExit.SetChecked(true)
	gui.tsAllowLAN.SetChecked(true)
	gui.tsEphemeral.SetChecked(true)

	got, err := gui.profileFromForm(p)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsTailscale() {
		t.Fatalf("profile kind = %q, want Tailscale", got.Kind)
	}
	if got.WireGuardConfig != "" {
		t.Fatalf("Tailscale profile should clear WireGuard config, got %q", got.WireGuardConfig)
	}
	if got.TailscaleConfig.Hostname != "ts-demo" || got.TailscaleConfig.ControlURL != "https://control.example.com" {
		t.Fatalf("unexpected Tailscale config: %#v", got.TailscaleConfig)
	}
	if !got.TailscaleConfig.AutoExitNode || !got.TailscaleConfig.ExitNodeAllowLANAccess || !got.TailscaleConfig.Ephemeral {
		t.Fatalf("unexpected Tailscale booleans: %#v", got.TailscaleConfig)
	}
}

func TestProfileFromFormKeepsAuthenticatedTailscaleProfileKeyless(t *testing.T) {
	p := profile.NewTailscale("demo", 1080)
	p.TailscaleConfig.Authenticated = true
	gui := newProfileTestGUI(t, p)
	gui.tsAuthKey.SetText("tskey-should-not-save")

	got, err := gui.profileFromForm(p)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TailscaleConfig.Authenticated {
		t.Fatal("authenticated flag should be preserved")
	}
	if got.TailscaleConfig.AuthKey != "" {
		t.Fatalf("authenticated profile should not save auth key, got %q", got.TailscaleConfig.AuthKey)
	}
}

func TestProfileFromFormUsesSelectedExitNodeID(t *testing.T) {
	p := profile.NewTailscale("demo", 1080)
	gui := newProfileTestGUI(t, p)
	gui.applyExitNodeOptions([]connection.ExitNode{
		{ID: "stable-exit", Name: "exit-a", Online: true},
	})
	gui.tsExitNode.SetText("exit-a")

	got, err := gui.profileFromForm(p)
	if err != nil {
		t.Fatal(err)
	}

	if got.TailscaleConfig.ExitNode != "stable-exit" {
		t.Fatalf("ExitNode = %q, want stable-exit", got.TailscaleConfig.ExitNode)
	}
}

func TestProfileFromFormIgnoresSpecificExitNodeWhenAutomatic(t *testing.T) {
	p := profile.NewTailscale("demo", 1080)
	gui := newProfileTestGUI(t, p)
	gui.applyExitNodeOptions([]connection.ExitNode{
		{ID: "stable-exit", Name: "exit-a", Online: true},
	})
	gui.tsExitNode.SetText("exit-a")
	gui.tsAutoExit.SetChecked(true)

	got, err := gui.profileFromForm(p)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TailscaleConfig.AutoExitNode {
		t.Fatal("AutoExitNode should be set")
	}
	if got.TailscaleConfig.ExitNode != "" {
		t.Fatalf("ExitNode = %q, want empty in automatic mode", got.TailscaleConfig.ExitNode)
	}
}

func TestApplyExitNodeOptionsShowsLabelForStoredID(t *testing.T) {
	p := profile.NewTailscale("demo", 1080)
	p.TailscaleConfig.ExitNode = "stable-exit"
	gui := newProfileTestGUI(t, p)

	gui.applyExitNodeOptions([]connection.ExitNode{
		{ID: "stable-exit", Name: "exit-a", Online: true},
	})

	if gui.tsExitNode.Text != "exit-a" {
		t.Fatalf("exit node text = %q, want exit-a", gui.tsExitNode.Text)
	}
	if got := gui.selectedExitNodeValue(); got != "stable-exit" {
		t.Fatalf("selected exit node value = %q, want stable-exit", got)
	}
}

func TestTailscaleFormGuidesAuthentication(t *testing.T) {
	gui := newProfilesTestGUI(t)
	gui.setupTailscaleForm()

	if got, want := gui.tsHostname.PlaceHolder, "Defaults to profile name"; got != want {
		t.Fatalf("hostname placeholder = %q, want %q", got, want)
	}
	if got, want := gui.tsAuthKey.PlaceHolder, "Optional; paste auth key, or leave empty for browser sign-in"; got != want {
		t.Fatalf("auth key placeholder = %q, want %q", got, want)
	}
	if gui.tsLoginButton == nil {
		t.Fatal("Tailscale auth form should include a Login button")
	}
	if got, want := gui.tsLoginButton.Text, "Login"; got != want {
		t.Fatalf("login button text = %q, want %q", got, want)
	}
	if got, want := gui.tsControlURL.PlaceHolder, "Optional; leave empty for Tailscale"; got != want {
		t.Fatalf("control URL placeholder = %q, want %q", got, want)
	}
	if got, want := gui.tsExitNode.PlaceHolder, "Refresh to choose a device, or type a node/IP"; got != want {
		t.Fatalf("exit node placeholder = %q, want %q", got, want)
	}
	if got, want := requireFormHint(t, gui.tailscaleForm, "Auth key"), "Paste an auth key, then click Login. Leave it empty to use the browser sign-in URL in the profile log. After authentication succeeds, the saved auth key is removed and this field stays locked until Logout. Logout removes this profile's stored Tailscale state and unlocks auth."; got != want {
		t.Fatalf("auth key hint = %q, want %q", got, want)
	}
	if got, want := requireFormHint(t, gui.tailscaleForm, "Exit node"), "Connect first, then refresh to list devices from this tailnet. Save while connected to apply exit-node changes immediately."; got != want {
		t.Fatalf("exit node hint = %q, want %q", got, want)
	}
	if got, want := requireFormHint(t, gui.tailscaleForm, "Exit node mode"), "Automatic exit asks Tailscale to choose an available exit node."; got != want {
		t.Fatalf("exit node mode hint = %q, want %q", got, want)
	}
}

func TestAuthenticatedTailscaleFormShowsLogoutState(t *testing.T) {
	p := profile.NewTailscale("demo", 1080)
	p.TailscaleConfig.Authenticated = true
	gui := newProfileTestGUI(t, p)

	if got, want := gui.tsAuthKey.PlaceHolder, "Authenticated"; got != want {
		t.Fatalf("auth key placeholder = %q, want %q", got, want)
	}
	if gui.tsAuthKey.Text != "" {
		t.Fatalf("authenticated auth key field text = %q, want empty", gui.tsAuthKey.Text)
	}
	if !gui.tsAuthKey.Disabled() {
		t.Fatal("authenticated auth key field should be disabled")
	}
	if got, want := gui.tsLoginButton.Text, "Logout"; got != want {
		t.Fatalf("auth action button = %q, want %q", got, want)
	}
	if gui.tsLoginButton.Disabled() {
		t.Fatal("logout button should be enabled while stopped")
	}
}

func TestLoginTailscaleSelectedStartsWithSavedAuthKey(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	runner := newFakeRunner()
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()
	gui.tsAuthKey.SetText("tskey-auth-example")

	runTestUI(func() {
		gui.loginTailscaleSelected()
	})

	waitForCondition(t, func() bool {
		return runner.startCallCount() == 1 && gui.statuses[p.ID] == "running"
	}, "Tailscale login start")
	started := runner.startedProfileAt(0)
	if !started.IsTailscale() {
		t.Fatalf("started profile kind = %q, want Tailscale", started.Kind)
	}
	if started.TailscaleConfig.AuthKey != "tskey-auth-example" {
		t.Fatalf("started auth key = %q, want saved auth key", started.TailscaleConfig.AuthKey)
	}
	if !gui.profiles[0].TailscaleConfig.Authenticated {
		t.Fatal("profile should be marked authenticated after successful start")
	}
	if gui.profiles[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("auth key should be cleared after successful start, got %q", gui.profiles[0].TailscaleConfig.AuthKey)
	}
	saved, err := gui.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || !saved[0].TailscaleConfig.Authenticated || saved[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("saved profile should be authenticated and keyless, got %#v", saved)
	}
	if got := strings.Join(gui.logs[p.ID], "\n"); !strings.Contains(got, "Tailscale authenticated; auth key removed from saved profile") {
		t.Fatalf("expected auth cleanup log, got %q", got)
	}
}

func TestLogoutTailscaleSelectedClearsAuthenticatedState(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	p.TailscaleConfig.Authenticated = true
	runner := newFakeRunner()
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()

	gui.tailscaleAuthActionSelected()

	if got := runner.logoutCallCount(); got != 1 {
		t.Fatalf("Logout calls = %d, want 1", got)
	}
	if runner.logoutCalls[0] != p.ID {
		t.Fatalf("Logout profile ID = %q, want %q", runner.logoutCalls[0], p.ID)
	}
	if gui.profiles[0].TailscaleConfig.Authenticated {
		t.Fatal("profile should no longer be authenticated after logout")
	}
	if gui.profiles[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("auth key after logout = %q, want empty", gui.profiles[0].TailscaleConfig.AuthKey)
	}
	if got, want := gui.tsLoginButton.Text, "Login"; got != want {
		t.Fatalf("auth action button = %q, want %q", got, want)
	}
	if gui.tsAuthKey.Disabled() {
		t.Fatal("auth key field should be enabled after logout")
	}
	if got := strings.Join(gui.logs[p.ID], "\n"); !strings.Contains(got, "logged out of Tailscale") {
		t.Fatalf("expected logout log, got %q", got)
	}
}

func TestLogoutTailscaleSelectedBlockedWhileRunning(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	p.TailscaleConfig.Authenticated = true
	runner := newFakeRunner()
	runner.setRunning(p.ID, true)
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.statuses[p.ID] = "running"
	gui.window = fynetest.NewTempWindow(t, widget.NewLabel(""))

	gui.logoutTailscaleSelected()

	if got := runner.logoutCallCount(); got != 0 {
		t.Fatalf("Logout calls = %d, want 0", got)
	}
	if !gui.profiles[0].TailscaleConfig.Authenticated {
		t.Fatal("running logout should not clear authenticated state")
	}
}

func TestLogoutTailscaleSelectedRestoresProfileWhenLogoutFails(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	p.TailscaleConfig.Authenticated = true
	runner := newFakeRunner()
	runner.logoutErr = errors.New("state removal failed")
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()
	gui.window = fynetest.NewTempWindow(t, widget.NewLabel(""))

	gui.logoutTailscaleSelected()

	if got := runner.logoutCallCount(); got != 1 {
		t.Fatalf("Logout calls = %d, want 1", got)
	}
	if !gui.profiles[0].TailscaleConfig.Authenticated {
		t.Fatal("failed logout should restore authenticated state in memory")
	}
	saved, err := gui.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || !saved[0].TailscaleConfig.Authenticated {
		t.Fatalf("failed logout should restore authenticated state on disk, got %#v", saved)
	}
}

func TestRuntimeLockedTailscaleFieldsAreDisabled(t *testing.T) {
	p := profile.NewTailscale("demo", 1080)
	gui := newProfileTestGUI(t, p)
	runner := newFakeRunner()
	runner.setRunning(p.ID, true)
	gui.runner = runner

	gui.refresh()

	for name, field := range map[string]fyne.Disableable{
		"backend":     gui.kindSelect,
		"socks host":  gui.hostEntry,
		"socks port":  gui.portEntry,
		"hostname":    gui.tsHostname,
		"auth key":    gui.tsAuthKey,
		"login":       gui.tsLoginButton,
		"control URL": gui.tsControlURL,
		"ephemeral":   gui.tsEphemeral,
	} {
		if !field.Disabled() {
			t.Fatalf("%s field should be disabled while Tailscale profile is running", name)
		}
	}
	for name, field := range map[string]fyne.Disableable{
		"name":       gui.nameEntry,
		"startup":    gui.autoStartCheck,
		"exit node":  gui.tsExitNode,
		"refresh":    gui.tsExitRefresh,
		"auto exit":  gui.tsAutoExit,
		"LAN access": gui.tsAllowLAN,
		"save":       gui.saveButton,
		"export":     gui.exportButton,
		"delete":     gui.deleteButton,
		"disconnect": gui.disconnectButton,
	} {
		if field.Disabled() {
			t.Fatalf("%s field should remain enabled while Tailscale profile is running", name)
		}
	}

	runner.setRunning(p.ID, false)
	gui.statuses[p.ID] = "stopped"
	gui.refresh()

	if gui.tsAuthKey.Disabled() {
		t.Fatal("auth key field should be enabled again after disconnect")
	}
	if gui.tsLoginButton.Disabled() {
		t.Fatal("login button should be enabled again after disconnect")
	}
	if gui.tsExitNode.Disabled() {
		t.Fatal("exit node field should be enabled again after disconnect")
	}
}

func TestStartingTailscaleExitNodeFieldsAreDisabled(t *testing.T) {
	p := profile.NewTailscale("demo", 1080)
	gui := newProfileTestGUI(t, p)
	gui.statuses[p.ID] = "starting"

	gui.refresh()

	for name, field := range map[string]fyne.Disableable{
		"exit node":  gui.tsExitNode,
		"refresh":    gui.tsExitRefresh,
		"auto exit":  gui.tsAutoExit,
		"LAN access": gui.tsAllowLAN,
	} {
		if !field.Disabled() {
			t.Fatalf("%s field should be disabled while Tailscale profile is starting", name)
		}
	}
}

func TestRuntimeLockedStatus(t *testing.T) {
	locked := []string{"starting", "running", "stopping"}
	for _, status := range locked {
		if !runtimeLockedStatus(status) {
			t.Fatalf("%q should be locked", status)
		}
	}

	unlocked := []string{"", "stopped", "error"}
	for _, status := range unlocked {
		if runtimeLockedStatus(status) {
			t.Fatalf("%q should not be locked", status)
		}
	}
}

func TestStatusSummaryUsesUserFacingStatus(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)

	if got, want := statusSummaryText(p, "running"), "demo on 127.0.0.1:1080 [connected]"; got != want {
		t.Fatalf("running status summary = %q, want %q", got, want)
	}
	if got, want := statusSummaryText(p, "stopping"), "demo on 127.0.0.1:1080 [disconnecting]"; got != want {
		t.Fatalf("stopping status summary = %q, want %q", got, want)
	}
	if got, want := statusSummaryText(p, "stopped"), "demo on 127.0.0.1:1080 [disconnected]"; got != want {
		t.Fatalf("stopped status summary = %q, want %q", got, want)
	}
}

func TestProfileListRowShowsStatusAndBindOnSeparateLines(t *testing.T) {
	p := profile.New("KFT-VPN-Plk-macos-proxy", sampleWireGuardConfig, 25344)
	gui := newProfilesTestGUI(t, p)
	gui.statuses[p.ID] = "running"
	item := newProfileListItem()

	gui.updateProfileListItem(0, item)

	statusIcon, name, bind, ok := profileListItemViews(item)
	if !ok {
		t.Fatalf("profile list item should expose name and bind labels: %#v", item)
	}
	if got, want := statusIcon.Resource.Name(), "status-connected.svg"; got != want {
		t.Fatalf("status icon = %q, want %q", got, want)
	}
	if got, want := name.Text, "KFT-VPN-Plk-macos-proxy"; got != want {
		t.Fatalf("name line = %q, want %q", got, want)
	}
	if got, want := bind.Text, "SOCKS5 127.0.0.1:25344"; got != want {
		t.Fatalf("bind line = %q, want %q", got, want)
	}
	if name.Truncation != fyne.TextTruncateEllipsis {
		t.Fatalf("name/status line should truncate with ellipsis, got %v", name.Truncation)
	}
	if bind.Truncation != fyne.TextTruncateEllipsis {
		t.Fatalf("bind line should truncate with ellipsis, got %v", bind.Truncation)
	}
	if bind.SizeName != theme.SizeNameCaptionText {
		t.Fatalf("bind line size = %q, want %q", bind.SizeName, theme.SizeNameCaptionText)
	}
	if bind.Importance != widget.LowImportance {
		t.Fatalf("bind line importance = %v, want %v", bind.Importance, widget.LowImportance)
	}
}

func TestProfileListRowUsesLocalizedBindLine(t *testing.T) {
	withTranslator(t, func(message string, data ...any) string {
		switch message {
		case "SOCKS5 {{.Address}}":
			values := data[0].(map[string]any)
			return "BIND " + values["Address"].(string)
		default:
			return message
		}
	})

	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfilesTestGUI(t, p)
	gui.statuses[p.ID] = "running"
	item := newProfileListItem()

	gui.updateProfileListItem(0, item)

	name, bind, ok := profileListItemLabels(item)
	if !ok {
		t.Fatalf("profile list item should expose name and bind labels: %#v", item)
	}
	if got, want := name.Text, "demo"; got != want {
		t.Fatalf("name line = %q, want %q", got, want)
	}
	if got, want := bind.Text, "BIND 127.0.0.1:1080"; got != want {
		t.Fatalf("bind line = %q, want %q", got, want)
	}
}

func TestConfigLogSplitHonorsResizeOffset(t *testing.T) {
	fynetest.NewTempApp(t)
	config := newWireGuardConfigEntry()
	config.SetText(strings.Repeat("WireGuard line\n", 80))
	log := container.NewVScroll(widget.NewLabel(strings.Repeat("log line\n", 40)))
	split := newConfigLogSplit(config, log)
	window := fynetest.NewTempWindow(t, split)
	window.Resize(fyne.NewSize(640, 480))

	split.SetOffset(0.25)
	split.Refresh()
	shortConfigHeight := config.Size().Height
	tallLogHeight := log.Size().Height

	split.SetOffset(0.75)
	split.Refresh()
	tallConfigHeight := config.Size().Height
	shortLogHeight := log.Size().Height

	if tallConfigHeight <= shortConfigHeight+80 {
		t.Fatalf("config editor height did not grow with split offset: short=%v tall=%v", shortConfigHeight, tallConfigHeight)
	}
	if shortLogHeight >= tallLogHeight-80 {
		t.Fatalf("log panel height did not shrink with split offset: tall=%v short=%v", tallLogHeight, shortLogHeight)
	}
}

func TestWireGuardConfigEditorHasSmallSplitMinimum(t *testing.T) {
	entry := newWireGuardConfigEntry()
	if entry.MinSize().Height > 120 {
		t.Fatalf("config editor minimum height = %v, want small enough for split resizing", entry.MinSize().Height)
	}
}

func TestSelectedLogTailsByDefault(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.logScroll.Resize(fyne.NewSize(320, 48))
	for range 40 {
		gui.logs[p.ID] = append(gui.logs[p.ID], "12:00:00  log line")
	}

	gui.showSelected()

	if !gui.logTail {
		t.Fatal("selected log should start in tail mode")
	}
	if !logScrollAtBottom(gui.logScroll, gui.logScroll.Offset) {
		t.Fatalf("selected log should scroll to bottom, offset=%v max=%v", gui.logScroll.Offset, logMaxScrollOffset(gui.logScroll))
	}
	if gui.logScroll.Offset.Y <= 0 {
		t.Fatalf("expected log scroll offset to move below top, got %v", gui.logScroll.Offset)
	}
}

func TestSelectedLogStopsTailingUntilScrolledBackToBottom(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.logScroll.Resize(fyne.NewSize(320, 48))
	for range 40 {
		gui.logs[p.ID] = append(gui.logs[p.ID], "12:00:00  log line")
	}
	gui.showSelected()
	bottom := logMaxScrollOffset(gui.logScroll)
	if bottom <= 0 {
		t.Fatalf("test log should be scrollable, max offset=%v", bottom)
	}

	gui.logScroll.ScrollToOffset(fyne.NewPos(0, 0))
	gui.handleLogScrolled(gui.logScroll.Offset)
	if gui.logTail {
		t.Fatal("scrolling away from bottom should leave tail mode")
	}
	gui.appendLog(p.ID, time.Now(), "new line while scrolled up")
	if got := gui.logScroll.Offset.Y; got != 0 {
		t.Fatalf("log should not jump while tail mode is off, offset=%v", got)
	}

	gui.logScroll.ScrollToBottom()
	gui.handleLogScrolled(gui.logScroll.Offset)
	if !gui.logTail {
		t.Fatal("scrolling back to bottom should restore tail mode")
	}
	gui.appendLog(p.ID, time.Now(), "new line after returning to bottom")
	if !logScrollAtBottom(gui.logScroll, gui.logScroll.Offset) {
		t.Fatalf("log should tail after returning to bottom, offset=%v max=%v", gui.logScroll.Offset, logMaxScrollOffset(gui.logScroll))
	}
}

func TestSelectedLogTailStateSurvivesProfileSwitch(t *testing.T) {
	first := profile.New("first", sampleWireGuardConfig, 1080)
	second := profile.New("second", sampleWireGuardConfig, 1081)
	gui := newProfilesTestGUI(t, first, second)
	gui.logScroll.Resize(fyne.NewSize(320, 48))
	for range 40 {
		gui.logs[first.ID] = append(gui.logs[first.ID], "12:00:00  first line")
		gui.logs[second.ID] = append(gui.logs[second.ID], "12:00:00  second line")
	}

	gui.selectedID = first.ID
	gui.showSelected()
	gui.logScroll.ScrollToOffset(fyne.NewPos(0, 0))
	gui.handleLogScrolled(gui.logScroll.Offset)
	if gui.logTail {
		t.Fatal("first profile should leave tail mode after scrolling up")
	}

	gui.selectedID = second.ID
	gui.showSelected()
	if !gui.logTail {
		t.Fatal("second profile should default to tail mode")
	}
	if !logScrollAtBottom(gui.logScroll, gui.logScroll.Offset) {
		t.Fatalf("second profile should show bottom by default, offset=%v max=%v", gui.logScroll.Offset, logMaxScrollOffset(gui.logScroll))
	}

	gui.selectedID = first.ID
	gui.showSelected()
	if gui.logTail {
		t.Fatal("first profile should preserve paused tail mode after switching away and back")
	}
	gui.appendLog(first.ID, time.Now(), "new line while first profile is still scrolled up")
	if got := gui.logScroll.Offset.Y; got != 0 {
		t.Fatalf("first profile log should not jump after switching back, offset=%v", got)
	}
}

func TestRefreshButtonsTreatsStoppingProfileAsLocked(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.statuses[p.ID] = "stopping"

	gui.refreshButtons()

	if !gui.connectButton.Disabled() {
		t.Fatal("connect button should be disabled while profile is stopping")
	}
	if gui.disconnectButton.Disabled() {
		t.Fatal("disconnect button should stay enabled while profile is stopping")
	}
}

func TestDisconnectAllMarksEveryActiveStatusAsStopping(t *testing.T) {
	starting := profile.New("starting", sampleWireGuardConfig, 1080)
	running := profile.New("running", sampleWireGuardConfig, 1081)
	stopping := profile.New("stopping", sampleWireGuardConfig, 1082)
	stopped := profile.New("stopped", sampleWireGuardConfig, 1083)
	failed := profile.New("failed", sampleWireGuardConfig, 1084)
	gui := newProfilesTestGUI(t, starting, running, stopping, stopped, failed)
	gui.statuses[starting.ID] = "starting"
	gui.statuses[running.ID] = "running"
	gui.statuses[stopping.ID] = "stopping"
	gui.statuses[stopped.ID] = "stopped"
	gui.statuses[failed.ID] = "error"

	gui.disconnectAll()

	for _, p := range []profile.Profile{starting, running, stopping} {
		if got := gui.statuses[p.ID]; got != "stopping" {
			t.Fatalf("%s status = %q, want stopping", p.Name, got)
		}
	}
	if got := gui.statuses[stopped.ID]; got != "stopped" {
		t.Fatalf("stopped profile status = %q, want stopped", got)
	}
	if got := gui.statuses[failed.ID]; got != "error" {
		t.Fatalf("failed profile status = %q, want error", got)
	}
}

func TestRunningBindConflictIncludesStoppingProfiles(t *testing.T) {
	active := profile.New("active", sampleWireGuardConfig, 1080)
	candidate := profile.New("candidate", sampleWireGuardConfig, 1080)
	gui := newProfilesTestGUI(t, active, candidate)
	gui.statuses[active.ID] = "stopping"

	err := gui.runningBindConflict(candidate)
	if !errors.Is(err, profile.ErrDuplicateBindAddress) {
		t.Fatalf("expected duplicate bind error for stopping profile, got %v", err)
	}
	if !strings.Contains(err.Error(), "active profile") {
		t.Fatalf("duplicate bind error should describe active profile, got %q", err.Error())
	}
}

func TestStartProfileSkipsRuntimeLockedProfile(t *testing.T) {
	p := profile.New("demo", sampleWireGuardConfig, 1080)
	gui := newProfileTestGUI(t, p)
	gui.statuses[p.ID] = "stopping"

	err := gui.startProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := gui.statuses[p.ID]; got != "stopping" {
		t.Fatalf("locked profile status = %q, want stopping", got)
	}
	if len(gui.logs[p.ID]) != 0 {
		t.Fatalf("locked profile should not log a new connection attempt: %#v", gui.logs[p.ID])
	}
}

func TestStartProfileDoesNotBlockWhileRunnerStartWaits(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	runner := newFakeRunner()
	blockStart := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() {
		unblock.Do(func() {
			close(blockStart)
		})
	})
	runner.startBlock = blockStart
	runner.startStarted = make(chan string, 1)
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()

	var err error
	runTestUI(func() {
		err = gui.startProfile(p, "Connect profile")
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-runner.startStarted:
		if got != p.ID {
			t.Fatalf("started profile = %q, want %q", got, p.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner.Start was not called")
	}
	if got := gui.statuses[p.ID]; got != "starting" {
		t.Fatalf("status while runner.Start is blocked = %q, want starting", got)
	}
	if len(gui.logs[p.ID]) == 0 || !strings.Contains(gui.logs[p.ID][0], "connecting profile") {
		t.Fatalf("start should log before runner.Start returns, got %#v", gui.logs[p.ID])
	}

	unblock.Do(func() {
		close(blockStart)
	})
	waitForCondition(t, func() bool {
		return gui.statuses[p.ID] == "running"
	}, "running status after runner.Start unblocks")
}

func TestDisconnectSelectedCancelsPendingStart(t *testing.T) {
	p := profile.NewTailscale("tailnet", 1080)
	runner := newFakeRunner()
	blockStart := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() {
		unblock.Do(func() {
			close(blockStart)
		})
	})
	runner.startBlock = blockStart
	runner.startStarted = make(chan string, 1)
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()

	var err error
	runTestUI(func() {
		err = gui.startProfile(p, "Connect profile")
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.startStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runner.Start was not called")
	}

	runTestUI(func() {
		gui.disconnectSelected()
	})

	waitForCondition(t, func() bool {
		return gui.statuses[p.ID] == "stopped"
	}, "pending start cancellation")
	if runner.Running(p.ID) {
		t.Fatal("profile should not keep running after pending start is canceled")
	}
	if len(gui.logs[p.ID]) == 0 || !strings.Contains(gui.logs[p.ID][len(gui.logs[p.ID])-1], "disconnected") {
		t.Fatalf("canceled start should log disconnection, got %#v", gui.logs[p.ID])
	}
}

func TestStartAutoProfilesMarksConnectedProfileBeforeStartedEvent(t *testing.T) {
	p := profile.New("auto", sampleWireGuardConfig, 1080)
	p.AutoStart = true
	runner := newFakeRunner()
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()

	runTestUI(func() {
		gui.startAutoProfiles()
	})

	waitForCondition(t, func() bool {
		return runner.startCallCount() == 1
	}, "auto-start call")
	waitForCondition(t, func() bool {
		return gui.statuses[p.ID] == "running"
	}, "auto-started running status")
	if got := runner.startCallCount(); got != 1 {
		t.Fatalf("expected one auto-start call, got %d", got)
	}
	if got := gui.statuses[p.ID]; got != "running" {
		t.Fatalf("auto-started status = %q, want running", got)
	}
	if got, want := gui.statusLabel.Text, "auto on 127.0.0.1:1080 [connected]"; got != want {
		t.Fatalf("status label = %q, want %q", got, want)
	}
}

func TestShutdownStopsProfilesOnce(t *testing.T) {
	p := profile.New("auto", sampleWireGuardConfig, 1080)
	runner := newFakeRunner()
	gui := newProfileTestGUI(t, p)
	gui.runner = runner

	gui.shutdown()
	gui.shutdown()

	if got := runner.stopAllAndWaitCallCount(); got != 1 {
		t.Fatalf("StopAllAndWait calls = %d, want 1", got)
	}
}

func TestTrayMenuMarksSingleQuitItem(t *testing.T) {
	gui := newProfilesTestGUI(t)

	menu := gui.trayMenu()

	quitCount := 0
	for _, item := range menu.Items {
		if item.IsQuit {
			quitCount++
		}
	}
	if quitCount != 1 {
		t.Fatalf("expected exactly one framework quit item, got %d in %#v", quitCount, menu.Items)
	}
	last := menu.Items[len(menu.Items)-1]
	if !last.IsQuit {
		t.Fatalf("last tray menu item must be marked IsQuit so Fyne does not append another Quit: %#v", last)
	}
	if last.Action == nil {
		t.Fatal("quit menu item should keep the app shutdown action")
	}
	if len(menu.Items) < 2 || !menu.Items[len(menu.Items)-2].IsSeparator {
		t.Fatalf("quit item should stay separated from action items: %#v", menu.Items)
	}
}

func TestTrayMenuShowsProfileSubmenusWithInfoAndActions(t *testing.T) {
	office := profile.New("office", sampleWireGuardConfig, 1080)
	home := profile.New(
		"home",
		strings.Replace(sampleWireGuardConfig, "Address = 10.2.0.2/32", "Address = 10.8.0.2/32, fd00::2/128", 1),
		1081,
	)
	gui := newProfilesTestGUI(t, office, home)
	gui.statuses[office.ID] = "running"

	menu := gui.trayMenu()

	officeItem := requireMenuItem(t, menu, "office")
	requireMenuIcon(t, officeItem, "status-connected.svg")
	requireChildLabel(t, officeItem, "Status: connected")
	requireChildLabel(t, officeItem, "SOCKS5 bind: 127.0.0.1:1080")
	requireChildLabel(t, officeItem, "WireGuard IP: 10.2.0.2/32")
	requireChildAction(t, officeItem, "Connect", true)
	requireChildAction(t, officeItem, "Disconnect", false)

	homeItem := requireMenuItem(t, menu, "home")
	requireMenuIcon(t, homeItem, "status-disconnected.svg")
	requireChildLabel(t, homeItem, "Status: disconnected")
	requireChildLabel(t, homeItem, "SOCKS5 bind: 127.0.0.1:1081")
	requireChildLabel(t, homeItem, "WireGuard IP: 10.8.0.2/32, fd00::2/128")
	requireChildAction(t, homeItem, "Connect", false)
	requireChildAction(t, homeItem, "Disconnect", true)
}

func TestTrayMenuShowsTailscaleProfileDetail(t *testing.T) {
	p := profile.NewTailscale("exit proxy", 1080)
	p.TailscaleConfig.Hostname = "wireproxy-exit"
	gui := newProfilesTestGUI(t, p)

	item := requireMenuItem(t, gui.trayMenu(), "exit proxy")

	requireChildLabel(t, item, "Tailscale node: wireproxy-exit")
}

func TestTrayMenuTreatsStartingProfileAsActive(t *testing.T) {
	p := profile.New("starting", sampleWireGuardConfig, 1080)
	gui := newProfilesTestGUI(t, p)
	gui.statuses[p.ID] = "starting"

	item := requireMenuItem(t, gui.trayMenu(), "starting")

	requireMenuIcon(t, item, "status-disconnected.svg")
	requireChildLabel(t, item, "Status: connecting")
	requireChildAction(t, item, "Connect", true)
	requireChildAction(t, item, "Disconnect", false)
}

func TestTrayMenuShowsRunningAutoStartedProfileAsConnectedBeforeStartedEvent(t *testing.T) {
	p := profile.New("auto", sampleWireGuardConfig, 1080)
	runner := newFakeRunner()
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	runner.setRunning(p.ID, true)
	gui.statuses[p.ID] = "starting"

	item := requireMenuItem(t, gui.trayMenu(), "auto")

	requireMenuIcon(t, item, "status-connected.svg")
	requireChildLabel(t, item, "Status: connected")
	requireChildAction(t, item, "Connect", true)
	requireChildAction(t, item, "Disconnect", false)
}

func TestTrayMenuLocalizesMissingWireGuardAddress(t *testing.T) {
	withTranslator(t, func(message string, data ...any) string {
		switch message {
		case "not configured":
			return "NOT_CONFIGURED_L10N"
		case "WireGuard IP: {{.Address}}":
			values := data[0].(map[string]any)
			return "WireGuard IP: " + values["Address"].(string)
		default:
			return message
		}
	})

	p := profile.New("missing-address", "[Interface]\nPrivateKey = placeholder-interface-value\n[Peer]\nPublicKey = placeholder-peer-value\n", 1080)
	gui := newProfilesTestGUI(t, p)

	item := requireMenuItem(t, gui.trayMenu(), "missing-address")

	requireChildLabel(t, item, "WireGuard IP: NOT_CONFIGURED_L10N")
}

func TestUsageGuideMentionsEmbeddedRunnerAndNativePaths(t *testing.T) {
	guide := usageGuideText()
	for _, want := range []string{
		"embedded WireGuard engine",
		"embedded tsnet node",
		"does not launch the wireproxy, tailscale, or tailscaled command-line tools",
		"browser sign-in URL in the profile log",
		"register this app as a Tailscale device",
		"approve the device in the admin console",
		"app will detect approval automatically",
		"removes the saved auth key",
		"marks the profile authenticated",
		"locked until you click Logout",
		"Logout removes this profile's stored Tailscale state",
		"Auth key, control URL, hostname, backend, and SOCKS5 bind fields are locked",
		"Exit-node mode, selected exit node, and LAN access can be changed while connected",
		"Exit-node choices are not live-updating",
		"Automatic exit asks Tailscale to choose an available exit node",
		"click Refresh again after tailnet devices or approvals change",
		"profile log follows the newest line by default",
		"Scrolling up pauses following",
		"one profile row per profile",
		"colored dot shows connection status",
		"submenu shows status text",
		"icon is green only while connected",
		"wait for it to finish disconnecting",
		"wait briefly for them to close",
		"operating system's native file dialog",
		"selected filesystem path",
		"Tailscale node state and the local authenticated marker are not exported",
	} {
		if !strings.Contains(guide, want) {
			t.Fatalf("usage guide is missing %q:\n%s", want, guide)
		}
	}
	if strings.Contains(guide, "Green means connected") {
		t.Fatalf("usage guide should not imply color alone describes every status:\n%s", guide)
	}
}

func TestUsageGuideUsesTranslator(t *testing.T) {
	withTranslator(t, func(message string, _ ...any) string {
		if message == usageGuideParagraphs[0] {
			return "TRANSLATED INTRO"
		}
		return message
	})

	guide := usageGuideText()
	if !strings.Contains(guide, "TRANSLATED INTRO") {
		t.Fatalf("usage guide did not use translator:\n%s", guide)
	}
}

func TestUsageGuideContentIsScrollable(t *testing.T) {
	fynetest.NewTempApp(t)
	content := newUsageGuideContent(usageGuideText())

	if content.Scroll != fyne.ScrollVerticalOnly {
		t.Fatalf("usage guide scroll direction = %v, want vertical-only", content.Scroll)
	}
	if content.Wrapping != fyne.TextWrapWord {
		t.Fatalf("usage guide wrapping = %v, want word wrapping", content.Wrapping)
	}
	if content.MinSize().Height > 80 {
		t.Fatalf("usage guide min height = %v, want scrollable bounded content", content.MinSize().Height)
	}
}

func TestEnglishCatalogTranslatorRendersTemplates(t *testing.T) {
	got := translateEnglishCatalog("{{.Name}} on {{.Address}} [{{.Status}}]", map[string]any{
		"Name":    "demo",
		"Address": "127.0.0.1:1080",
		"Status":  "connected",
	})
	if want := "demo on 127.0.0.1:1080 [connected]"; got != want {
		t.Fatalf("catalog template = %q, want %q", got, want)
	}
}

func TestLocalizedErrorTextUsesTranslator(t *testing.T) {
	withTranslator(t, func(message string, _ ...any) string {
		if message == profile.ErrSocksPortNotNumber.Error() {
			return "PORT_ERROR_L10N"
		}
		return message
	})

	err := localizedErrorText(errors.Join(errTestContext, profile.ErrSocksPortNotNumber))
	if !strings.Contains(err, "PORT_ERROR_L10N") {
		t.Fatalf("localized error did not replace known error: %q", err)
	}
}

func TestLocalizedLogMessageHandlesConnectedAddress(t *testing.T) {
	withTranslator(t, func(message string, data ...any) string {
		if message == "connected on {{.Address}}" {
			values := data[0].(map[string]any)
			return "CONNECTED_AT_" + values["Address"].(string)
		}
		return message
	})

	if got, want := localizedLogMessage("connected on 127.0.0.1:1080"), "CONNECTED_AT_127.0.0.1:1080"; got != want {
		t.Fatalf("localized log message = %q, want %q", got, want)
	}
}

func TestLocalizedLogMessageHandlesTailscaleLoginURL(t *testing.T) {
	withTranslator(t, func(message string, data ...any) string {
		if message == "Tailscale login required: open {{.URL}}" {
			values := data[0].(map[string]any)
			return "TAILSCALE_LOGIN " + values["URL"].(string)
		}
		return message
	})

	if got, want := localizedLogMessage("Tailscale login required: open https://login.tailscale.com/a/abc"), "TAILSCALE_LOGIN https://login.tailscale.com/a/abc"; got != want {
		t.Fatalf("localized log message = %q, want %q", got, want)
	}
}

func TestLocalizedTextHandlesStructuredErrorMessages(t *testing.T) {
	withTranslator(t, func(message string, data ...any) string {
		switch message {
		case "listen SOCKS5 on {{.Address}}: {{.Message}}":
			values := data[0].(map[string]any)
			return "LISTEN " + values["Address"].(string) + ": " + values["Message"].(string)
		case "start embedded WireGuard engine: {{.Message}}":
			values := data[0].(map[string]any)
			return "START_ENGINE: " + values["Message"].(string)
		case "save imported profiles: {{.Message}}":
			values := data[0].(map[string]any)
			return "SAVE_IMPORT: " + values["Message"].(string)
		case "save authenticated Tailscale profile: {{.Message}}":
			values := data[0].(map[string]any)
			return "SAVE_AUTH: " + values["Message"].(string)
		case "restore Tailscale authentication state: {{.Message}}":
			values := data[0].(map[string]any)
			return "RESTORE_AUTH: " + values["Message"].(string)
		case "update Tailscale exit node: {{.Message}}":
			values := data[0].(map[string]any)
			return "UPDATE_EXIT: " + values["Message"].(string)
		case "import WireGuard config: {{.Message}}":
			values := data[0].(map[string]any)
			return "IMPORT_WG: " + values["Message"].(string)
		case "wireproxy config validation failed: {{.Message}}":
			values := data[0].(map[string]any)
			return "CONFIG_INVALID: " + values["Message"].(string)
		case profile.ErrWireGuardConfigEmpty.Error():
			return "WG_EMPTY_L10N"
		default:
			return message
		}
	})

	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{
			name: "listen error",
			text: "listen SOCKS5 on 127.0.0.1:1080: bind: address already in use",
			want: "LISTEN 127.0.0.1:1080: bind: address already in use",
		},
		{
			name: "engine start error",
			text: "start embedded WireGuard engine: permission denied",
			want: "START_ENGINE: permission denied",
		},
		{
			name: "save imported profiles",
			text: "save imported profiles: permission denied",
			want: "SAVE_IMPORT: permission denied",
		},
		{
			name: "save authenticated profile",
			text: "save authenticated Tailscale profile: permission denied",
			want: "SAVE_AUTH: permission denied",
		},
		{
			name: "restore authenticated profile",
			text: "restore Tailscale authentication state: permission denied",
			want: "RESTORE_AUTH: permission denied",
		},
		{
			name: "update Tailscale exit node",
			text: "update Tailscale exit node: Tailscale profile is not connected",
			want: "UPDATE_EXIT: Tailscale profile is not connected",
		},
		{
			name: "import config nested sentinel",
			text: "import WireGuard config: WireGuard config is required",
			want: "IMPORT_WG: WG_EMPTY_L10N",
		},
		{
			name: "wireproxy config validation",
			text: "wireproxy config validation failed: WireGuard config is required",
			want: "CONFIG_INVALID: WG_EMPTY_L10N",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := localizedText(tc.text); got != tc.want {
				t.Fatalf("localizedText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestLocalizedTextHandlesDuplicateBindMessages(t *testing.T) {
	withTranslator(t, func(message string, data ...any) string {
		switch message {
		case "duplicate SOCKS5 bind address: {{.Address}} is used by \"{{.First}}\" and \"{{.Second}}\"":
			values := data[0].(map[string]any)
			return "DUP " + values["Address"].(string) + ` "` + values["First"].(string) + `" "` + values["Second"].(string) + `"`
		case "duplicate SOCKS5 bind address: {{.Address}} is already used by active profile \"{{.Name}}\"":
			values := data[0].(map[string]any)
			return "ACTIVE_DUP " + values["Address"].(string) + ` "` + values["Name"].(string) + `"`
		default:
			return message
		}
	})

	if got, want := localizedText(`duplicate SOCKS5 bind address: 127.0.0.1:1080 is used by "first profile" and "second profile"`), `DUP 127.0.0.1:1080 "first profile" "second profile"`; got != want {
		t.Fatalf("duplicate bind text = %q, want %q", got, want)
	}
	if got, want := localizedText(`duplicate SOCKS5 bind address: 127.0.0.1:1080 is already used by active profile "office"`), `ACTIVE_DUP 127.0.0.1:1080 "office"`; got != want {
		t.Fatalf("active duplicate bind text = %q, want %q", got, want)
	}
}

func TestDisplayErrorTextAvoidsDuplicateTitlePrefix(t *testing.T) {
	if got, want := displayErrorText("Load profiles", errLoadProfilesInvalid), "Load profiles: invalid JSON"; got != want {
		t.Fatalf("display error text = %q, want %q", got, want)
	}
}

func TestEnglishCatalogCoversImportantAppText(t *testing.T) {
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
		"Close",
		"Quit",
		"not configured",
		"listen SOCKS5 on {{.Address}}: {{.Message}}",
		"start embedded WireGuard engine: {{.Message}}",
		"start embedded Tailscale node: {{.Message}}",
		"configure Tailscale exit node: {{.Message}}",
		"Error stopping profiles during shutdown",
		"save imported profiles: {{.Message}}",
		"save authenticated Tailscale profile: {{.Message}}",
		"restore Tailscale authentication state: {{.Message}}",
		"import WireGuard config: {{.Message}}",
		"wireproxy config validation failed: {{.Message}}",
		"update Tailscale exit node: {{.Message}}",
		"duplicate SOCKS5 bind address: {{.Address}} is used by \"{{.First}}\" and \"{{.Second}}\"",
		"duplicate SOCKS5 bind address: {{.Address}} is already used by active profile \"{{.Name}}\"",
		"Closing the window hides it when tray support is available. Use Quit from the tray menu to stop all profiles, wait briefly for them to close, and exit.",
		"The tray menu has one profile row per profile. The colored dot shows connection status, and each submenu shows status text, SOCKS5 bind address, backend detail, and Connect or Disconnect actions. The icon is green only while connected; other states use a red icon and the status text shows disconnected, connecting, disconnecting, or error.",
		"Login",
		"Login to Tailscale",
		"Logout",
		"Logout from Tailscale",
		"Authenticated",
		"Tailscale login required: open {{.URL}}",
		"Tailscale login complete; approve this device in the Tailscale admin console. The app will continue automatically after approval",
		"Tailscale has existing profile state; auth key was not used for this start",
		"Tailscale authenticated; auth key removed from saved profile",
		"invalid Tailscale profile ID",
		"profile is not a Tailscale profile",
		"Tailscale profile is not connected",
		"exit nodes are only available for running Tailscale profiles",
		"native file picker is unavailable; install zenity, matedialog, or qarma on Unix-like systems",
		"select a Tailscale profile before login",
		"select a Tailscale profile before logout",
		"logged out of Tailscale",
		"Automatic exit asks Tailscale to choose an available exit node.",
		"updated Tailscale exit-node settings",
	} {
		if _, ok := catalog[key]; !ok {
			t.Fatalf("English catalog is missing %q", key)
		}
	}
}

func requireMenuItem(t *testing.T, menu *fyne.Menu, label string) *fyne.MenuItem {
	t.Helper()
	for _, item := range menu.Items {
		if item.Label == label {
			if item.ChildMenu == nil {
				t.Fatalf("menu item %q should have a child menu", item.Label)
			}
			return item
		}
	}
	t.Fatalf("menu is missing item %q; items: %#v", label, menu.Items)
	return nil
}

func requireFormHint(t *testing.T, form *widget.Form, label string) string {
	t.Helper()
	for _, item := range form.Items {
		if item.Text == label {
			return item.HintText
		}
	}
	t.Fatalf("form is missing item %q; items: %#v", label, form.Items)
	return ""
}

func requireMenuIcon(t *testing.T, item *fyne.MenuItem, name string) {
	t.Helper()
	if item.Icon == nil {
		t.Fatalf("menu item %q should have icon %q", item.Label, name)
	}
	if item.Icon.Name() != name {
		t.Fatalf("menu item %q icon = %q, want %q", item.Label, item.Icon.Name(), name)
	}
}

func requireChildLabel(t *testing.T, parent *fyne.MenuItem, label string) {
	t.Helper()
	for _, item := range parent.ChildMenu.Items {
		if item.Label == label {
			if !item.Disabled {
				t.Fatalf("info item %q should be disabled", label)
			}
			return
		}
	}
	t.Fatalf("child menu %q is missing label %q; items: %#v", parent.Label, label, parent.ChildMenu.Items)
}

func requireChildAction(t *testing.T, parent *fyne.MenuItem, label string, disabled bool) {
	t.Helper()
	for _, item := range parent.ChildMenu.Items {
		if item.Label == label {
			if item.Action == nil {
				t.Fatalf("action item %q should have an action", label)
			}
			if item.Disabled != disabled {
				t.Fatalf("action item %q disabled = %t, want %t", label, item.Disabled, disabled)
			}
			return
		}
	}
	t.Fatalf("child menu %q is missing action %q; items: %#v", parent.Label, label, parent.ChildMenu.Items)
}

func newProfileTestGUI(t *testing.T, p profile.Profile) *GUI {
	t.Helper()
	return newProfilesTestGUI(t, p)
}

func withTranslator(t *testing.T, fn translateFunc) {
	t.Helper()
	original := translate
	translate = fn
	t.Cleanup(func() {
		translate = original
	})
}

func withImmediateUI(t *testing.T) {
	t.Helper()
	uiTestMu.Lock()
	original := runOnUI
	runOnUI = func(fn func()) {
		uiTestMu.Lock()
		defer uiTestMu.Unlock()
		fn()
	}
	uiTestMu.Unlock()
	t.Cleanup(func() {
		uiTestMu.Lock()
		defer uiTestMu.Unlock()
		runOnUI = original
	})
}

func runTestUI(fn func()) {
	uiTestMu.Lock()
	defer uiTestMu.Unlock()
	fn()
}

func waitForCondition(t *testing.T, ok func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		uiTestMu.Lock()
		done := ok()
		uiTestMu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func newProfilesTestGUI(t *testing.T, profiles ...profile.Profile) *GUI {
	t.Helper()
	withImmediateUI(t)

	statuses := map[string]string{}
	logs := map[string][]string{}
	selectedID := ""
	for i, p := range profiles {
		statuses[p.ID] = "stopped"
		if i == 0 {
			selectedID = p.ID
		}
	}

	gui := &GUI{
		store:        profile.NewStore(filepath.Join(t.TempDir(), "profiles.json")),
		runner:       wireproxy.NewRunner(),
		profiles:     profiles,
		statuses:     statuses,
		logs:         logs,
		startCancels: map[string]context.CancelFunc{},
		selectedID:   selectedID,
	}
	gui.kindSelect = widget.NewSelect([]string{tr("WireGuard"), tr("Tailscale")}, func(_ string) {
		gui.updateBackendVisibility(gui.selectedBackendKind())
	})
	gui.nameEntry = widget.NewEntry()
	gui.hostEntry = widget.NewEntry()
	gui.portEntry = widget.NewEntry()
	gui.autoStartCheck = widget.NewCheck("Connect when app opens", nil)
	gui.configEntry = newWireGuardConfigEntry()
	gui.configLabel = widget.NewLabel(tr("WireGuard configuration"))
	gui.setupTailscaleForm()
	gui.setupLogView()
	gui.statusLabel = widget.NewLabel("")
	gui.saveButton = widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), nil)
	gui.deleteButton = widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), nil)
	gui.connectButton = widget.NewButtonWithIcon("Connect", theme.MediaPlayIcon(), nil)
	gui.disconnectButton = widget.NewButtonWithIcon("Disconnect", theme.MediaStopIcon(), nil)
	gui.exportButton = widget.NewButtonWithIcon("Export", theme.UploadIcon(), nil)
	gui.showSelected()
	return gui
}

type fakeProfileFileDialog struct {
	openPath string
	openErr  error

	savePath     string
	saveFileName string
	saveErr      error

	openCalls int
	saveCalls int
}

func (f *fakeProfileFileDialog) OpenProfilePath() (string, error) {
	f.openCalls++
	return f.openPath, f.openErr
}

func (f *fakeProfileFileDialog) SaveProfilesPath(fileName string) (string, error) {
	f.saveCalls++
	f.saveFileName = fileName
	return f.savePath, f.saveErr
}

type fakeRunner struct {
	events              chan wireproxy.Event
	mu                  sync.Mutex
	running             map[string]bool
	startErr            error
	startBlock          chan struct{}
	startStarted        chan string
	startCalls          []string
	startProfiles       []profile.Profile
	stopCalls           []string
	exitNodes           []connection.ExitNode
	exitNodesErr        error
	exitNodeCalls       []string
	updateExitNodeCalls []string
	updateExitNodes     []profile.TailscaleConfig
	updateExitNodeErr   error
	logoutCalls         []string
	logoutErr           error
	stopAllAndWaitCalls int
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		events:  make(chan wireproxy.Event),
		running: map[string]bool{},
	}
}

func (f *fakeRunner) Events() <-chan wireproxy.Event {
	return f.events
}

func (f *fakeRunner) Running(profileID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[profileID]
}

func (f *fakeRunner) ExitNodes(_ context.Context, profileID string) ([]connection.ExitNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exitNodeCalls = append(f.exitNodeCalls, profileID)
	if f.exitNodesErr != nil {
		return nil, f.exitNodesErr
	}
	return f.exitNodes, nil
}

func (f *fakeRunner) UpdateExitNode(_ context.Context, profileID string, cfg profile.TailscaleConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateExitNodeCalls = append(f.updateExitNodeCalls, profileID)
	f.updateExitNodes = append(f.updateExitNodes, cfg)
	return f.updateExitNodeErr
}

func (f *fakeRunner) Logout(_ context.Context, profileID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutCalls = append(f.logoutCalls, profileID)
	return f.logoutErr
}

func (f *fakeRunner) Start(ctx context.Context, p profile.Profile) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	f.mu.Lock()
	f.startCalls = append(f.startCalls, p.ID)
	f.startProfiles = append(f.startProfiles, p)
	if f.startStarted != nil {
		select {
		case f.startStarted <- p.ID:
		default:
		}
	}
	block := f.startBlock
	startErr := f.startErr
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if startErr != nil {
		return startErr
	}
	f.mu.Lock()
	f.running[p.ID] = true
	f.mu.Unlock()
	return nil
}

func (f *fakeRunner) Stop(profileID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, profileID)
	if !f.running[profileID] {
		return false
	}
	delete(f.running, profileID)
	return true
}

func (f *fakeRunner) StopAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for profileID := range f.running {
		f.stopCalls = append(f.stopCalls, profileID)
		delete(f.running, profileID)
	}
}

func (f *fakeRunner) StopAllAndWait(context.Context) error {
	f.mu.Lock()
	f.stopAllAndWaitCalls++
	for profileID := range f.running {
		f.stopCalls = append(f.stopCalls, profileID)
		delete(f.running, profileID)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeRunner) setRunning(profileID string, running bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if running {
		f.running[profileID] = true
		return
	}
	delete(f.running, profileID)
}

func (f *fakeRunner) startCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.startCalls)
}

func (f *fakeRunner) startedProfileAt(index int) profile.Profile {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.startProfiles) {
		return profile.Profile{}
	}
	return f.startProfiles[index]
}

func (f *fakeRunner) updateExitNodeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updateExitNodeCalls)
}

func (f *fakeRunner) updatedExitNodeAt(index int) (string, profile.TailscaleConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.updateExitNodeCalls) {
		return "", profile.TailscaleConfig{}
	}
	return f.updateExitNodeCalls[index], f.updateExitNodes[index]
}

func (f *fakeRunner) logoutCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logoutCalls)
}

func (f *fakeRunner) stopAllAndWaitCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopAllAndWaitCalls
}
