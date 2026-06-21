package ui

import (
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/ncruces/zenity"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/wireproxy"
)

const sampleWireGuardConfig = `[Interface]
Address = 10.2.0.2/32
PrivateKey = private

[Peer]
PublicKey = public
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

func TestStartAutoProfilesMarksConnectedProfileBeforeStartedEvent(t *testing.T) {
	p := profile.New("auto", sampleWireGuardConfig, 1080)
	p.AutoStart = true
	runner := newFakeRunner()
	gui := newProfileTestGUI(t, p)
	gui.runner = runner
	gui.ctx = context.Background()

	gui.startAutoProfiles()

	if got := len(runner.startCalls); got != 1 {
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

	if got := runner.stopAllAndWaitCalls; got != 1 {
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
	runner.running[p.ID] = true
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

	p := profile.New("missing-address", "[Interface]\nPrivateKey = private\n[Peer]\nPublicKey = public\n", 1080)
	gui := newProfilesTestGUI(t, p)

	item := requireMenuItem(t, gui.trayMenu(), "missing-address")

	requireChildLabel(t, item, "WireGuard IP: NOT_CONFIGURED_L10N")
}

func TestUsageGuideMentionsEmbeddedRunnerAndNativePaths(t *testing.T) {
	guide := usageGuideText()
	for _, want := range []string{
		"embedded WireGuard engine",
		"does not launch the wireproxy command-line tool",
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
		"Error stopping profiles during shutdown",
		"save imported profiles: {{.Message}}",
		"import WireGuard config: {{.Message}}",
		"wireproxy config validation failed: {{.Message}}",
		"duplicate SOCKS5 bind address: {{.Address}} is used by \"{{.First}}\" and \"{{.Second}}\"",
		"duplicate SOCKS5 bind address: {{.Address}} is already used by active profile \"{{.Name}}\"",
		"Closing the window hides it when tray support is available. Use Quit from the tray menu to stop all profiles, wait briefly for them to close, and exit.",
		"The tray menu has one profile row per profile. The colored dot shows connection status, and each submenu shows status text, SOCKS5 bind address, WireGuard IP, and Connect or Disconnect actions. The icon is green only while connected; other states use a red icon and the status text shows disconnected, connecting, disconnecting, or error.",
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

func newProfilesTestGUI(t *testing.T, profiles ...profile.Profile) *GUI {
	t.Helper()

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
		store:      profile.NewStore(filepath.Join(t.TempDir(), "profiles.json")),
		runner:     wireproxy.NewRunner(),
		profiles:   profiles,
		statuses:   statuses,
		logs:       logs,
		selectedID: selectedID,
	}
	gui.nameEntry = widget.NewEntry()
	gui.hostEntry = widget.NewEntry()
	gui.portEntry = widget.NewEntry()
	gui.autoStartCheck = widget.NewCheck("Connect when app opens", nil)
	gui.configEntry = widget.NewMultiLineEntry()
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
	running             map[string]bool
	startErr            error
	startCalls          []string
	stopCalls           []string
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
	return f.running[profileID]
}

func (f *fakeRunner) Start(_ context.Context, p profile.Profile) error {
	f.startCalls = append(f.startCalls, p.ID)
	if f.startErr != nil {
		return f.startErr
	}
	f.running[p.ID] = true
	return nil
}

func (f *fakeRunner) Stop(profileID string) bool {
	f.stopCalls = append(f.stopCalls, profileID)
	if !f.running[profileID] {
		return false
	}
	delete(f.running, profileID)
	return true
}

func (f *fakeRunner) StopAll() {
	for profileID := range f.running {
		f.stopCalls = append(f.stopCalls, profileID)
		delete(f.running, profileID)
	}
}

func (f *fakeRunner) StopAllAndWait(context.Context) error {
	f.stopAllAndWaitCalls++
	f.StopAll()
	return nil
}
