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
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/ncruces/zenity"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
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

func TestApplyAppThemeUsesNativeTheme(t *testing.T) {
	app := fynetest.NewTempApp(t)
	applyAppTheme(app)
	if _, ok := app.Settings().Theme().(nativeTheme); !ok {
		t.Fatalf("app theme = %T, want nativeTheme", app.Settings().Theme())
	}
}

func TestNativeThemeUsesMacOSSystemBlueAccent(t *testing.T) {
	th := newNativeTheme()
	for _, test := range []struct {
		name    string
		variant fyne.ThemeVariant
		want    color.NRGBA
	}{
		{name: "light", variant: theme.VariantLight, want: color.NRGBA{R: 0x00, G: 0x7a, B: 0xff, A: 0xff}},
		{name: "dark", variant: theme.VariantDark, want: color.NRGBA{R: 0x0a, G: 0x84, B: 0xff, A: 0xff}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := th.Color(theme.ColorNamePrimary, test.variant); got != test.want {
				t.Fatalf("primary color = %#v, want %#v", got, test.want)
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
		t.Fatal("native theme should delegate icons")
	}
	if th.Font(fyne.TextStyle{}).Name() != fallback.Font(fyne.TextStyle{}).Name() {
		t.Fatal("native theme should delegate fonts")
	}
	if got, want := th.Size(theme.SizeNameText), fallback.Size(theme.SizeNameText); got != want {
		t.Fatalf("native theme text size = %v, want %v", got, want)
	}
	for name, want := range map[fyne.ThemeSizeName]float32{
		theme.SizeNamePadding:         6,
		theme.SizeNameInputRadius:     10,
		theme.SizeNameButtonRadius:    10,
		theme.SizeNameCardRadius:      14,
		theme.SizeNameSelectionRadius: 8,
	} {
		if got := th.Size(name); got != want {
			t.Fatalf("native theme %s = %v, want %v", name, got, want)
		}
	}
}

func TestBuildShowsGuidedEmptyStateWithoutProfiles(t *testing.T) {
	app := fynetest.NewTempApp(t)
	applyAppTheme(app)
	gui := &GUI{app: app, core: newFakeApplication()}
	gui.build()
	t.Cleanup(gui.window.Close)
	if !gui.emptyState.Visible() || gui.detailPane.Visible() {
		t.Fatalf("empty/detail visibility = %t/%t", gui.emptyState.Visible(), gui.detailPane.Visible())
	}
	if gui.sidebarSummaryLabel.Text != "No profiles" {
		t.Fatalf("sidebar summary = %q", gui.sidebarSummaryLabel.Text)
	}
}

func TestImportProfilesUsesNativeSelectedPathAndApplicationBoundary(t *testing.T) {
	importPath := filepath.Join(t.TempDir(), "office.conf")
	err := os.WriteFile(importPath, []byte(sampleWireGuardConfig), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	imported := profile.New("office", sampleWireGuardConfig, 1080)
	gui, core := newProfilesTestGUI(t)
	gui.files = &fakeProfileFileDialog{openPath: importPath}
	core.importResult = []profile.Profile{imported}

	gui.importProfiles()

	if core.importFileName != "office.conf" || string(core.importData) != sampleWireGuardConfig {
		t.Fatalf("application import arguments = %q/%q", core.importFileName, core.importData)
	}
	if gui.selectedID != imported.ID {
		t.Fatalf("selected profile = %q, want %q", gui.selectedID, imported.ID)
	}
}

func TestImportProfilesIgnoresCanceledNativePath(t *testing.T) {
	gui, core := newProfilesTestGUI(t)
	files := &fakeProfileFileDialog{}
	gui.files = files
	gui.importProfiles()
	if files.openCalls != 1 || core.importCalls != 0 {
		t.Fatalf("cancel calls: dialog=%d application=%d", files.openCalls, core.importCalls)
	}
}

func TestExportProfilesUsesApplicationCodecAndNativePath(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	exportPath := filepath.Join(t.TempDir(), "profiles.json")
	gui, core := newProfilesTestGUI(t, item)
	files := &fakeProfileFileDialog{savePath: exportPath}
	gui.files = files
	core.exportData = []byte("profile export\n")

	gui.exportProfiles([]profile.Profile{item}, "demo.json")

	if files.saveFileName != "demo.json" || len(core.exportIDs) != 1 || core.exportIDs[0] != item.ID {
		t.Fatalf("export boundary args: file=%q ids=%#v", files.saveFileName, core.exportIDs)
	}
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "profile export\n" {
		t.Fatalf("exported data = %q", data)
	}
}

func TestExportProfilesToPathRestrictsExistingFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	err := os.WriteFile(path, []byte("old"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = exportProfilesToPath([]byte("new"), path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("export mode = %o, want 600", got)
	}
}

func TestSelectedPathTreatsNativeCancelAsNoop(t *testing.T) {
	path, err := selectedPath("ignored", zenity.ErrCanceled)
	if err != nil || path != "" {
		t.Fatalf("selectedPath() = %q, %v", path, err)
	}
}

func TestSaveSelectedProfileMapsFormIntoApplicationUseCase(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	gui, core := newProfilesTestGUI(t, item)
	gui.nameEntry.SetText("renamed")
	gui.autoStartCheck.SetChecked(true)

	err := gui.saveSelectedProfile()
	if err != nil {
		t.Fatal(err)
	}
	if len(core.savedProfiles) != 1 || core.savedProfiles[0].Name != "renamed" || !core.savedProfiles[0].AutoStart {
		t.Fatalf("application Save input = %#v", core.savedProfiles)
	}
}

func TestProfileFromFormReturnsCleanPortError(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	gui, _ := newProfilesTestGUI(t, item)
	gui.portEntry.SetText("not-a-number")
	_, err := gui.profileFromForm(item)
	if !errors.Is(err, profile.ErrSocksPortNotNumber) || err.Error() != profile.ErrSocksPortNotNumber.Error() {
		t.Fatalf("port error = %v", err)
	}
}

func TestProfileFromFormBuildsTailscaleProfile(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	gui, _ := newProfilesTestGUI(t, item)
	gui.kindSelect.SetSelected(tr("Tailscale"))
	gui.tsHostname.SetText("ts-demo")
	gui.tsAuthKey.SetText("auth")
	gui.tsControlURL.SetText("https://control.example.com")
	gui.tsAutoExit.SetChecked(true)
	gui.tsAllowLAN.SetChecked(true)
	gui.tsEphemeral.SetChecked(true)

	got, err := gui.profileFromForm(item)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsTailscale() || got.WireGuardConfig != "" || got.TailscaleConfig.Hostname != "ts-demo" || got.TailscaleConfig.AuthKey != "auth" {
		t.Fatalf("mapped Tailscale profile = %#v", got)
	}
	if !got.TailscaleConfig.AutoExitNode || !got.TailscaleConfig.ExitNodeAllowLANAccess || !got.TailscaleConfig.Ephemeral {
		t.Fatalf("mapped Tailscale flags = %#v", got.TailscaleConfig)
	}
}

func TestProfileFromFormKeepsAuthenticatedTailscaleProfileKeyless(t *testing.T) {
	item := profile.NewTailscale("demo", 1080)
	item.TailscaleConfig.Authenticated = true
	gui, _ := newProfilesTestGUI(t, item)
	gui.tsAuthKey.SetText("tskey-should-not-save")
	got, err := gui.profileFromForm(item)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TailscaleConfig.Authenticated || got.TailscaleConfig.AuthKey != "" {
		t.Fatalf("mapped authenticated config = %#v", got.TailscaleConfig)
	}
}

func TestProfileFromFormMapsExitNodeLabelToStableID(t *testing.T) {
	item := profile.NewTailscale("demo", 1080)
	gui, _ := newProfilesTestGUI(t, item)
	gui.applyExitNodeOptions([]connection.ExitNode{{ID: "stable-exit", Name: "exit-a", Online: true}})
	gui.tsExitNode.SetText("exit-a")
	got, err := gui.profileFromForm(item)
	if err != nil {
		t.Fatal(err)
	}
	if got.TailscaleConfig.ExitNode != "stable-exit" {
		t.Fatalf("ExitNode = %q", got.TailscaleConfig.ExitNode)
	}
}

func TestApplyExitNodeOptionsShowsLabelForStoredID(t *testing.T) {
	item := profile.NewTailscale("demo", 1080)
	item.TailscaleConfig.ExitNode = "stable-exit"
	gui, _ := newProfilesTestGUI(t, item)
	gui.applyExitNodeOptions([]connection.ExitNode{{ID: "stable-exit", Name: "exit-a", Online: true}})
	if gui.tsExitNode.Text != "exit-a" || gui.selectedExitNodeValue() != "stable-exit" {
		t.Fatalf("exit node text/value = %q/%q", gui.tsExitNode.Text, gui.selectedExitNodeValue())
	}
}

func TestTailscaleFormGuidesAuthentication(t *testing.T) {
	gui, _ := newProfilesTestGUI(t)
	if got, want := gui.tsHostname.PlaceHolder, "Defaults to profile name"; got != want {
		t.Fatalf("hostname placeholder = %q", got)
	}
	if got, want := gui.tsAuthKey.PlaceHolder, "Optional; paste auth key, or leave empty for browser sign-in"; got != want {
		t.Fatalf("auth placeholder = %q", got)
	}
	if gui.tsLoginButton.Text != "Login" || gui.tsControlURL.PlaceHolder != "Optional; leave empty for Tailscale" {
		t.Fatalf("unexpected Tailscale controls")
	}
	if !strings.Contains(requireFormHint(t, gui.tailscaleForm, "Auth key"), "browser sign-in") {
		t.Fatal("auth hint does not explain browser sign-in")
	}
}

// TestTailscaleFormHintsStayWithinViewport guards against Fyne's canvas.Text
// hint rows, which never wrap, forcing the Form (and therefore the window)
// wider than the viewport. See internal/ui/app.go setupTailscaleForm.
func TestTailscaleFormHintsStayWithinViewport(t *testing.T) {
	gui, _ := newProfilesTestGUI(t)
	const maxHintChars = 90
	for _, item := range gui.tailscaleForm.Items {
		if len(item.HintText) > maxHintChars {
			t.Fatalf("hint for %q is %d chars, unwrapped canvas.Text will widen the window: %q", item.Text, len(item.HintText), item.HintText)
		}
	}
}

func TestAuthenticatedTailscaleFormShowsLogoutState(t *testing.T) {
	item := profile.NewTailscale("demo", 1080)
	item.TailscaleConfig.Authenticated = true
	gui, _ := newProfilesTestGUI(t, item)
	if gui.tsAuthKey.PlaceHolder != "Authenticated" || gui.tsAuthKey.Text != "" || !gui.tsAuthKey.Disabled() {
		t.Fatalf("authenticated auth field state is wrong")
	}
	if gui.tsLoginButton.Text != "Logout" || gui.tsLoginButton.Disabled() {
		t.Fatalf("logout button state is wrong")
	}
}

func TestRuntimeLockedFieldsReflectApplicationState(t *testing.T) {
	item := profile.NewTailscale("demo", 1080)
	gui, core := newProfilesTestGUI(t, item)
	core.locked[item.ID] = true
	core.statuses[item.ID] = application.StatusRunning
	gui.refresh()

	for name, field := range map[string]fyne.Disableable{
		"backend": gui.kindSelect, "host": gui.hostEntry, "port": gui.portEntry,
		"hostname": gui.tsHostname, "auth": gui.tsAuthKey, "login": gui.tsLoginButton,
		"control": gui.tsControlURL, "ephemeral": gui.tsEphemeral,
	} {
		if !field.Disabled() {
			t.Fatalf("%s should be disabled while runtime is locked", name)
		}
	}
	for name, field := range map[string]fyne.Disableable{
		"name": gui.nameEntry, "startup": gui.autoStartCheck, "exit node": gui.tsExitNode,
		"auto exit": gui.tsAutoExit, "LAN": gui.tsAllowLAN,
	} {
		if field.Disabled() {
			t.Fatalf("%s should remain enabled while connected", name)
		}
	}
}

func TestTransitioningTailscaleExitNodeFieldsAreDisabled(t *testing.T) {
	item := profile.NewTailscale("demo", 1080)
	gui, core := newProfilesTestGUI(t, item)
	core.locked[item.ID] = true
	core.statuses[item.ID] = application.StatusStarting
	gui.refresh()
	for name, field := range map[string]fyne.Disableable{
		"exit node": gui.tsExitNode, "refresh": gui.tsExitRefresh,
		"auto exit": gui.tsAutoExit, "LAN": gui.tsAllowLAN,
	} {
		if !field.Disabled() {
			t.Fatalf("%s should be disabled while starting", name)
		}
	}
}

func TestStatusSummaryUsesUserFacingStatus(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	if got, want := statusSummaryText(item, "running"), "Connected · SOCKS5 127.0.0.1:1080"; got != want {
		t.Fatalf("status summary = %q", got)
	}
}

func TestProfileListRowShowsStatusAndBindOnSeparateLines(t *testing.T) {
	item := profile.New("office", sampleWireGuardConfig, 1080)
	gui, core := newProfilesTestGUI(t, item)
	core.statuses[item.ID] = application.StatusRunning
	row := newProfileListItem()
	gui.updateProfileListItem(0, row)
	icon, name, bind, ok := profileListItemViews(row)
	if !ok || name.Text != "office" || bind.Text != "Connected · SOCKS5 127.0.0.1:1080" {
		t.Fatalf("profile row = ok:%t name:%q bind:%q", ok, name.Text, bind.Text)
	}
	if icon.Resource.Name() != connectedTrayIcon.Name() {
		t.Fatalf("profile row icon = %q", icon.Resource.Name())
	}
}

func TestConfigLogSplitHonorsResizeOffset(t *testing.T) {
	app := fynetest.NewTempApp(t)
	config := widget.NewLabel("config")
	log := widget.NewLabel("log")
	split := newConfigLogSplit(config, log)
	window := app.NewWindow("split")
	window.SetContent(split)
	window.Resize(fyne.NewSize(800, 600))
	fynetest.ApplyTheme(t, theme.DefaultTheme())
	window.Canvas().Refresh(split)
	want := split.Size().Width * float32(split.Offset)
	if delta := split.Leading.Size().Width - want; delta < -8 || delta > 8 {
		t.Fatalf("leading width = %.1f, want %.1f", split.Leading.Size().Width, want)
	}
}

// TestConfigLogSplitIsHorizontal ensures the Activity log stays in its own
// full-height vertical column next to the configuration panel, instead of a
// stacked top/bottom split where the config form's height changes (e.g.
// switching between WireGuard and Tailscale) would squeeze the log.
func TestConfigLogSplitIsHorizontal(t *testing.T) {
	split := newConfigLogSplit(widget.NewLabel("config"), widget.NewLabel("log"))
	if !split.Horizontal {
		t.Fatal("config/log split must be horizontal so the log keeps its own vertical column")
	}
}

func TestWireGuardConfigEditorHasSmallSplitMinimum(t *testing.T) {
	entry := newWireGuardConfigEntry()
	if entry.MinSize().Height > 120 {
		t.Fatalf("config editor min height = %v", entry.MinSize().Height)
	}
}

// TestBackendConfigSwitcherMinSizeStaysStable guards against the window
// resizing whenever the user switches between the WireGuard and Tailscale
// backends. container.NewStack's MinSize only counts visible children, so
// toggling Show()/Hide() between the short WireGuard editor and the much
// taller Tailscale form used to change the stack's minimum height and force
// the whole window to resize. newBackendConfigSwitcher must keep the
// reported MinSize identical across both selections.
func TestBackendConfigSwitcherMinSizeStaysStable(t *testing.T) {
	fynetest.NewTempApp(t)
	gui, _ := newProfilesTestGUI(t)
	switcher := newBackendConfigSwitcher(gui.configEntry, gui.tailscaleForm)

	gui.updateBackendVisibility(profile.BackendWireGuard)
	wireGuardMin := switcher.MinSize()

	gui.updateBackendVisibility(profile.BackendTailscale)
	tailscaleMin := switcher.MinSize()

	if wireGuardMin != tailscaleMin {
		t.Fatalf("switcher min size changed across backends: wireguard=%v tailscale=%v", wireGuardMin, tailscaleMin)
	}
}

func TestSelectedLogTailStateSurvivesProfileSwitch(t *testing.T) {
	first := profile.New("first", sampleWireGuardConfig, 1080)
	second := profile.New("second", sampleWireGuardConfig, 1081)
	gui, _ := newProfilesTestGUI(t, first, second)
	gui.setSelectedLogTail(false)
	gui.selectedID = second.ID
	if !gui.selectedLogTail() {
		t.Fatal("second profile should tail by default")
	}
	gui.setSelectedLogTail(true)
	gui.selectedID = first.ID
	if gui.selectedLogTail() {
		t.Fatal("first profile tail state was not preserved")
	}
}

func TestRefreshButtonsUsesApplicationRuntimeLock(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	gui, core := newProfilesTestGUI(t, item)
	core.locked[item.ID] = true
	core.statuses[item.ID] = application.StatusRunning
	gui.refreshButtons()
	if gui.connectButton.Visible() || !gui.disconnectButton.Visible() || gui.disconnectButton.Disabled() {
		t.Fatal("running profile should show an enabled Disconnect action")
	}
}

func TestPrimaryConnectionActionKeepsStartingInterruptible(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	gui, core := newProfilesTestGUI(t, item)
	core.locked[item.ID] = true
	core.statuses[item.ID] = application.StatusStarting
	gui.refreshButtons()
	if gui.connectButton.Visible() || !gui.disconnectButton.Visible() || gui.disconnectButton.Disabled() {
		t.Fatal("starting profile should expose an enabled cancel action")
	}
	if gui.disconnectButton.Text != "Cancel Connection" {
		t.Fatalf("starting action = %q", gui.disconnectButton.Text)
	}
}

func TestPrimaryConnectionActionShowsStoppingProgress(t *testing.T) {
	item := profile.New("demo", sampleWireGuardConfig, 1080)
	gui, core := newProfilesTestGUI(t, item)
	core.locked[item.ID] = true
	core.statuses[item.ID] = application.StatusStopping
	gui.refreshButtons()
	if gui.connectButton.Visible() || !gui.disconnectButton.Visible() || !gui.disconnectButton.Disabled() {
		t.Fatal("stopping profile should show disabled progress feedback")
	}
	if gui.disconnectButton.Text != "Disconnecting…" {
		t.Fatalf("stopping action = %q", gui.disconnectButton.Text)
	}
}

func TestProfileHeaderPresentsHierarchyAndStatus(t *testing.T) {
	item := profile.New("office", sampleWireGuardConfig, 1080)
	gui, core := newProfilesTestGUI(t, item)
	core.statuses[item.ID] = application.StatusRunning
	gui.showSelected()
	if gui.profileTitleLabel.Text != "office" {
		t.Fatalf("profile title = %q", gui.profileTitleLabel.Text)
	}
	if gui.profileMetaLabel.Text != "WireGuard · SOCKS5 127.0.0.1:1080" {
		t.Fatalf("profile metadata = %q", gui.profileMetaLabel.Text)
	}
	if gui.statusLabel.Text != "Connected" || gui.statusLabel.Importance != widget.SuccessImportance {
		t.Fatalf("status = %q/%v", gui.statusLabel.Text, gui.statusLabel.Importance)
	}
	if gui.statusIcon.Resource.Name() != connectedTrayIcon.Name() {
		t.Fatalf("status icon = %q", gui.statusIcon.Resource.Name())
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

func TestTrayMenuShowsProfileSubmenusWithInfoAndActions(t *testing.T) {
	office := profile.New("office", sampleWireGuardConfig, 1080)
	home := profile.New("home", sampleWireGuardConfig, 1081)
	gui, core := newProfilesTestGUI(t, office, home)
	core.statuses[office.ID] = application.StatusRunning
	menu := gui.trayMenu()
	officeItem := requireMenuItem(t, menu, "office")
	homeItem := requireMenuItem(t, menu, "home")
	requireChildLabel(t, officeItem, "Status: connected")
	requireChildLabel(t, officeItem, "SOCKS5 bind: 127.0.0.1:1080")
	requireChildAction(t, officeItem, "Connect", true)
	requireChildAction(t, officeItem, "Disconnect", false)
	requireChildAction(t, homeItem, "Connect", false)
	requireChildAction(t, homeItem, "Disconnect", true)
}

func TestTrayMenuShowsTailscaleProfileDetail(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	item.TailscaleConfig.Hostname = "proxy-node"
	gui, _ := newProfilesTestGUI(t, item)
	requireChildLabel(t, requireMenuItem(t, gui.trayMenu(), "tailnet"), "Tailscale node: proxy-node")
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

func TestUsageGuideContentIsScrollable(t *testing.T) {
	fynetest.NewTempApp(t)
	content := newUsageGuideContent(usageGuideText())
	if content.Scroll != fyne.ScrollVerticalOnly || content.Wrapping != fyne.TextWrapWord {
		t.Fatalf("usage guide presentation = scroll:%v wrapping:%v", content.Scroll, content.Wrapping)
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

func requireMenuItem(t *testing.T, menu *fyne.Menu, label string) *fyne.MenuItem {
	t.Helper()
	for _, item := range menu.Items {
		if item.Label == label {
			if item.ChildMenu == nil {
				t.Fatalf("menu item %q should have a child menu", label)
			}
			return item
		}
	}
	t.Fatalf("menu is missing %q", label)
	return nil
}

func requireFormHint(t *testing.T, form *widget.Form, label string) string {
	t.Helper()
	for _, item := range form.Items {
		if item.Text == label {
			return item.HintText
		}
	}
	t.Fatalf("form is missing %q", label)
	return ""
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
	t.Fatalf("child menu %q is missing %q", parent.Label, label)
}

func requireChildAction(t *testing.T, parent *fyne.MenuItem, label string, disabled bool) {
	t.Helper()
	for _, item := range parent.ChildMenu.Items {
		if item.Label == label {
			if item.Action == nil || item.Disabled != disabled {
				t.Fatalf("action %q state = action:%t disabled:%t", label, item.Action != nil, item.Disabled)
			}
			return
		}
	}
	t.Fatalf("child menu %q is missing action %q", parent.Label, label)
}

func withTranslator(t *testing.T, fn translateFunc) {
	t.Helper()
	original := translate
	translate = fn
	t.Cleanup(func() { translate = original })
}

func newProfilesTestGUI(t *testing.T, profiles ...profile.Profile) (*GUI, *fakeApplication) {
	t.Helper()
	fynetest.NewTempApp(t)
	core := newFakeApplication(profiles...)
	selectedID := ""
	if len(profiles) > 0 {
		selectedID = profiles[0].ID
	}
	gui := &GUI{core: core, selectedID: selectedID}
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
	gui.profileTitleLabel = newHeadingLabel("")
	gui.profileMetaLabel = newSecondaryLabel("")
	gui.statusIcon = widget.NewIcon(disconnectedTrayIcon)
	gui.statusLabel = newSecondaryLabel("")
	gui.feedbackLabel = newSecondaryLabel("")
	gui.saveButton = widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), nil)
	gui.deleteButton = widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), nil)
	gui.connectButton = widget.NewButtonWithIcon("Connect", theme.MediaPlayIcon(), nil)
	gui.connectButton.Importance = widget.HighImportance
	gui.disconnectButton = widget.NewButtonWithIcon("Disconnect", theme.MediaStopIcon(), nil)
	gui.exportButton = widget.NewButtonWithIcon("Export", theme.UploadIcon(), nil)
	gui.showSelected()
	return gui, core
}

type fakeProfileFileDialog struct {
	openPath string
	openErr  error
	savePath string
	saveErr  error

	openCalls    int
	saveCalls    int
	saveFileName string
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

type fakeApplication struct {
	mu sync.Mutex

	changes  chan application.Change
	profiles []profile.Profile
	statuses map[string]application.Status
	logs     map[string][]application.LogEntry
	locked   map[string]bool

	savedProfiles  []profile.Profile
	saveErr        error
	importResult   []profile.Profile
	importErr      error
	importFileName string
	importData     []byte
	importCalls    int
	exportData     []byte
	exportErr      error
	exportIDs      []string
	exitNodes      []connection.ExitNode
	exitNodesErr   error
	shutdownCalls  int
}

func newFakeApplication(profiles ...profile.Profile) *fakeApplication {
	statuses := make(map[string]application.Status, len(profiles))
	for _, item := range profiles {
		statuses[item.ID] = application.StatusStopped
	}
	return &fakeApplication{
		changes:  make(chan application.Change, 32),
		profiles: append([]profile.Profile(nil), profiles...),
		statuses: statuses,
		logs:     map[string][]application.LogEntry{},
		locked:   map[string]bool{},
	}
}

func (f *fakeApplication) Changes() <-chan application.Change { return f.changes }

func (f *fakeApplication) Profiles() []profile.Profile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]profile.Profile(nil), f.profiles...)
}

func (f *fakeApplication) Profile(id string) (profile.Profile, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, item := range f.profiles {
		if item.ID == id {
			return item, true
		}
	}
	return profile.Profile{}, false
}

func (f *fakeApplication) Status(id string) application.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	if status := f.statuses[id]; status != "" {
		return status
	}
	return application.StatusStopped
}

func (f *fakeApplication) Logs(id string) []application.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]application.LogEntry(nil), f.logs[id]...)
}

func (f *fakeApplication) RuntimeLocked(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.locked[id]
}

func (f *fakeApplication) Add(name string) (profile.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item := profile.New(name, "", profile.NextAvailablePort(f.profiles))
	f.profiles = append(f.profiles, item)
	f.statuses[item.ID] = application.StatusStopped
	return item, nil
}

func (f *fakeApplication) Save(item profile.Profile) (application.SaveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.savedProfiles = append(f.savedProfiles, item)
	if f.saveErr != nil {
		return application.SaveResult{}, f.saveErr
	}
	for index := range f.profiles {
		if f.profiles[index].ID == item.ID {
			f.profiles[index] = item
			return application.SaveResult{Changed: true}, nil
		}
	}
	return application.SaveResult{}, application.ErrProfileNotFound
}

func (f *fakeApplication) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.profiles {
		if f.profiles[index].ID == id {
			f.profiles = append(f.profiles[:index], f.profiles[index+1:]...)
			return nil
		}
	}
	return application.ErrProfileNotFound
}

func (f *fakeApplication) Import(fileName string, data []byte) ([]profile.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.importCalls++
	f.importFileName = fileName
	f.importData = append([]byte(nil), data...)
	if f.importErr != nil {
		return nil, f.importErr
	}
	f.profiles = append(f.profiles, f.importResult...)
	for _, item := range f.importResult {
		f.statuses[item.ID] = application.StatusStopped
	}
	return append([]profile.Profile(nil), f.importResult...), nil
}

func (f *fakeApplication) Export(ids []string) ([]byte, error) {
	f.exportIDs = append([]string(nil), ids...)
	return append([]byte(nil), f.exportData...), f.exportErr
}

func (*fakeApplication) Connect(string) error                 { return nil }
func (*fakeApplication) Login(string) error                   { return nil }
func (*fakeApplication) ConnectAll() error                    { return nil }
func (*fakeApplication) AutoConnect() error                   { return nil }
func (*fakeApplication) Disconnect(string) bool               { return true }
func (*fakeApplication) DisconnectFromTray(string) bool       { return true }
func (*fakeApplication) DisconnectAll()                       {}
func (*fakeApplication) Logout(context.Context, string) error { return nil }

func (f *fakeApplication) ExitNodes(context.Context, string) ([]connection.ExitNode, error) {
	return append([]connection.ExitNode(nil), f.exitNodes...), f.exitNodesErr
}

func (f *fakeApplication) Shutdown(context.Context) error {
	f.shutdownCalls++
	return nil
}

var _ Application = (*fakeApplication)(nil)
