package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/NRngnl/wireproxy-gui/internal/buildinfo"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/wireproxy"
)

const (
	maxLogLines         = 1000
	shutdownWaitTimeout = 5 * time.Second
	configEditorMinRows = 3
)

var usageGuideParagraphs = []string{
	"Add or import a WireGuard profile, then choose a SOCKS5 host and port for that profile.",
	"Each connected, connecting, or disconnecting profile must use a unique SOCKS5 bind address, such as 127.0.0.1:1080 and 127.0.0.1:1081.",
	"Connect starts the embedded WireGuard engine and SOCKS5 listener for the selected profile. The app does not launch the wireproxy command-line tool. Connect All starts every saved profile. Runtime status is shown in the selected profile log.",
	"The profile log follows the newest line by default. Scrolling up pauses following so you can read earlier output; scrolling back to the bottom follows new lines again.",
	"The tray menu has one profile row per profile. The colored dot shows connection status, and each submenu shows status text, SOCKS5 bind address, WireGuard IP, and Connect or Disconnect actions. The icon is green only while connected; other states use a red icon and the status text shows disconnected, connecting, disconnecting, or error.",
	"Disconnect a connected profile, and wait for it to finish disconnecting, before changing its WireGuard configuration or SOCKS5 bind address. Profile name and startup preference can be changed while connected.",
	"Import and Export open the operating system's native file dialog and use the selected filesystem path. Export writes profiles as JSON. Import accepts exported JSON bundles or WireGuard .conf files.",
	"Closing the window hides it when tray support is available. Use Quit from the tray menu to stop all profiles, wait briefly for them to close, and exit.",
}

var errRuntimeProfileEdit = errors.New("disconnect the profile, and wait for it to finish disconnecting, before changing its WireGuard configuration or SOCKS5 bind address")

var (
	connectedTrayIcon    = fyne.NewStaticResource("status-connected.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><circle cx="8" cy="8" r="6" fill="#28a745"/></svg>`))
	disconnectedTrayIcon = fyne.NewStaticResource(
		"status-disconnected.svg",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><circle cx="8" cy="8" r="6" fill="#d73a49"/></svg>`),
	)
)

type GUI struct {
	app    fyne.App
	window fyne.Window
	store  *profile.Store
	runner profileRunner
	ctx    context.Context
	cancel context.CancelFunc
	files  profileFileDialog
	tray   desktop.App

	shutdownOnce sync.Once

	profiles   []profile.Profile
	selectedID string
	statuses   map[string]string
	logs       map[string][]string
	logTails   map[string]bool
	logOffsets map[string]fyne.Position

	list           *widget.List
	nameEntry      *widget.Entry
	hostEntry      *widget.Entry
	portEntry      *widget.Entry
	autoStartCheck *widget.Check
	configEntry    *widget.Entry
	logLabel       *widget.Label
	logScroll      *container.Scroll
	logTail        bool
	statusLabel    *widget.Label

	saveButton       *widget.Button
	deleteButton     *widget.Button
	connectButton    *widget.Button
	disconnectButton *widget.Button
	exportButton     *widget.Button
}

type profileFileDialog interface {
	OpenProfilePath() (string, error)
	SaveProfilesPath(fileName string) (string, error)
}

type profileRunner interface {
	Events() <-chan wireproxy.Event
	Running(profileID string) bool
	Start(context.Context, profile.Profile) error
	Stop(profileID string) bool
	StopAll()
	StopAllAndWait(context.Context) error
}

type displayError string

func (e displayError) Error() string {
	return string(e)
}

func Run() {
	fyneApp := app.NewWithID("com.github.nrngnl.wireproxy-gui")
	applyAppTheme(fyneApp)
	ctx, cancel := context.WithCancel(context.Background())

	storePath, err := profile.DefaultStorePath()
	if err != nil {
		storePath = filepath.Join(".", "profiles.json")
	}

	gui := &GUI{
		app:      fyneApp,
		store:    profile.NewStore(storePath),
		runner:   wireproxy.NewRunner(),
		ctx:      ctx,
		cancel:   cancel,
		files:    nativeProfileFileDialog{},
		statuses: map[string]string{},
		logs:     map[string][]string{},
	}

	loadErr := gui.load()
	gui.build()
	gui.events()
	if loadErr != nil {
		gui.showError("Load profiles", loadErr)
	}
	gui.startAutoProfiles()
	gui.installTray()
	defer gui.shutdown()
	gui.window.ShowAndRun()
}

func (g *GUI) load() error {
	profiles, err := g.store.Load()
	if err != nil {
		return err
	}
	g.profiles = profiles
	for _, p := range g.profiles {
		g.statuses[p.ID] = "stopped"
	}
	if len(g.profiles) > 0 {
		g.selectedID = g.profiles[0].ID
	}
	return nil
}

func (g *GUI) build() {
	g.window = g.app.NewWindow(tr(buildinfo.WindowTitle()))
	g.window.Resize(fyne.NewSize(1120, 760))

	g.nameEntry = widget.NewEntry()
	g.hostEntry = widget.NewEntry()
	g.portEntry = widget.NewEntry()
	g.autoStartCheck = widget.NewCheck(tr("Connect when app opens"), nil)

	g.configEntry = newWireGuardConfigEntry()

	g.setupLogView()

	g.statusLabel = widget.NewLabel(tr("No profile selected"))

	g.saveButton = widget.NewButtonWithIcon(tr("Save"), theme.DocumentSaveIcon(), g.saveSelected)
	g.deleteButton = widget.NewButtonWithIcon(tr("Delete"), theme.DeleteIcon(), g.deleteSelected)
	g.connectButton = widget.NewButtonWithIcon(tr("Connect"), theme.MediaPlayIcon(), g.connectSelected)
	g.disconnectButton = widget.NewButtonWithIcon(tr("Disconnect"), theme.MediaStopIcon(), g.disconnectSelected)
	g.exportButton = widget.NewButtonWithIcon(tr("Export"), theme.UploadIcon(), g.exportSelected)

	g.list = widget.NewList(
		func() int { return len(g.profiles) },
		newProfileListItem,
		g.updateProfileListItem,
	)
	g.list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(g.profiles) {
			g.selectedID = g.profiles[id].ID
			g.showSelected()
		}
	}

	leftActions := container.NewHBox(
		widget.NewButtonWithIcon(tr("Add"), theme.ContentAddIcon(), g.addProfile),
		widget.NewButtonWithIcon(tr("Import"), theme.DownloadIcon(), g.importProfiles),
		widget.NewButtonWithIcon(tr("Help"), theme.HelpIcon(), g.showUsageGuide),
	)
	left := container.NewBorder(leftActions, nil, nil, nil, g.list)

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: tr("Name"), Widget: g.nameEntry},
			{Text: tr("SOCKS5 host"), Widget: g.hostEntry},
			{Text: tr("SOCKS5 port"), Widget: g.portEntry},
			{Text: tr("Startup"), Widget: g.autoStartCheck},
		},
	}
	actionBar := container.NewHBox(
		g.saveButton,
		g.exportButton,
		g.connectButton,
		g.disconnectButton,
		g.deleteButton,
		widget.NewButtonWithIcon(tr("Connect All"), theme.MediaPlayIcon(), g.connectAll),
		widget.NewButtonWithIcon(tr("Disconnect All"), theme.MediaStopIcon(), g.disconnectAll),
		widget.NewButtonWithIcon(tr("Export All"), theme.UploadIcon(), g.exportAll),
	)
	detail := container.NewBorder(
		container.NewVBox(g.statusLabel, form, widget.NewLabel(tr("WireGuard configuration"))),
		actionBar,
		nil,
		nil,
		newConfigLogSplit(
			g.configEntry,
			container.NewBorder(widget.NewLabel(tr("Profile log")), nil, nil, nil, g.logScroll),
		),
	)

	split := container.NewHSplit(left, detail)
	split.Offset = 0.30
	g.window.SetContent(split)

	g.showSelected()
	if len(g.profiles) > 0 {
		g.list.Select(0)
	}
}

func newWireGuardConfigEntry() *widget.Entry {
	entry := widget.NewMultiLineEntry()
	entry.SetMinRowsVisible(configEditorMinRows)
	entry.Wrapping = fyne.TextWrapOff
	return entry
}

func newConfigLogSplit(configPanel, logPanel fyne.CanvasObject) *container.Split {
	split := container.NewVSplit(configPanel, logPanel)
	split.Offset = 0.58
	return split
}

func (g *GUI) installTray() {
	if g.setupTray() {
		g.window.SetCloseIntercept(func() {
			g.window.Hide()
		})
	}
}

func (g *GUI) setupTray() bool {
	desk, ok := g.app.(desktop.App)
	if !ok {
		return false
	}

	g.tray = desk
	desk.SetSystemTrayIcon(theme.ComputerIcon())
	desk.SetSystemTrayMenu(g.trayMenu())
	return true
}

func newProfileListItem() fyne.CanvasObject {
	statusIcon := widget.NewIcon(disconnectedTrayIcon)

	name := widget.NewLabel("")
	name.Truncation = fyne.TextTruncateEllipsis

	bind := widget.NewLabel("")
	bind.Importance = widget.LowImportance
	bind.SizeName = theme.SizeNameCaptionText
	bind.Truncation = fyne.TextTruncateEllipsis

	return container.NewVBox(
		container.NewBorder(nil, nil, statusIcon, nil, name),
		bind,
	)
}

func (g *GUI) updateProfileListItem(id widget.ListItemID, obj fyne.CanvasObject) {
	statusIcon, name, bind, ok := profileListItemViews(obj)
	if !ok {
		return
	}
	if id < 0 || id >= len(g.profiles) {
		statusIcon.SetResource(disconnectedTrayIcon)
		name.SetText("")
		bind.SetText("")
		return
	}

	p := g.profiles[id]
	statusIcon.SetResource(trayStatusIcon(g.profileStatus(p.ID)))
	name.SetText(p.Name)
	bind.SetText(tr("SOCKS5 {{.Address}}", map[string]any{
		"Address": p.BindAddress(),
	}))
}

func profileListItemViews(obj fyne.CanvasObject) (statusIcon *widget.Icon, name, bind *widget.Label, ok bool) {
	var icons []*widget.Icon
	var labels []*widget.Label
	collectProfileListItemViews(obj, &icons, &labels)
	if len(icons) < 1 || len(labels) < 2 {
		return nil, nil, nil, false
	}
	return icons[0], labels[0], labels[1], true
}

func profileListItemLabels(obj fyne.CanvasObject) (name, bind *widget.Label, ok bool) {
	_, name, bind, ok = profileListItemViews(obj)
	return name, bind, ok
}

func collectProfileListItemViews(obj fyne.CanvasObject, icons *[]*widget.Icon, labels *[]*widget.Label) {
	switch v := obj.(type) {
	case *widget.Icon:
		*icons = append(*icons, v)
	case *widget.Label:
		*labels = append(*labels, v)
	case *fyne.Container:
		for _, child := range v.Objects {
			collectProfileListItemViews(child, icons, labels)
		}
	}
}

func (g *GUI) setupLogView() {
	g.logLabel = widget.NewLabel("")
	g.logLabel.Wrapping = fyne.TextWrapWord
	g.logLabel.TextStyle = fyne.TextStyle{Monospace: true}
	g.logScroll = container.NewVScroll(g.logLabel)
	g.logTail = true
	g.logScroll.OnScrolled = g.handleLogScrolled
}

func (g *GUI) handleLogScrolled(pos fyne.Position) {
	g.setSelectedLogTail(logScrollAtBottom(g.logScroll, pos))
	if g.selectedID != "" {
		g.ensureLogState()
		g.logOffsets[g.selectedID] = pos
	}
}

func (g *GUI) setLogText(text string, forceTail bool) {
	if g.logLabel == nil {
		return
	}
	if forceTail {
		g.setSelectedLogTail(true)
	} else {
		g.logTail = g.selectedLogTail()
	}
	g.logLabel.SetText(text)
	if g.logTail {
		g.scrollLogToBottom()
		return
	}
	if g.logScroll == nil || g.logScroll.Content == nil || g.selectedID == "" {
		return
	}
	if pos, ok := g.logOffsets[g.selectedID]; ok {
		g.logScroll.Content.Resize(g.logScroll.Content.MinSize().Max(g.logScroll.Size()))
		g.logScroll.ScrollToOffset(pos)
	}
}

func (g *GUI) scrollLogToBottom() {
	if g.logScroll == nil || g.logScroll.Content == nil {
		return
	}
	g.logScroll.Content.Resize(g.logScroll.Content.MinSize().Max(g.logScroll.Size()))
	g.setSelectedLogTail(true)
	g.logScroll.ScrollToBottom()
}

func (g *GUI) selectedLogTail() bool {
	if g.selectedID == "" {
		return g.logTail
	}
	g.ensureLogState()
	tail, ok := g.logTails[g.selectedID]
	return !ok || tail
}

func (g *GUI) setSelectedLogTail(tail bool) {
	g.logTail = tail
	if g.selectedID == "" {
		return
	}
	g.ensureLogState()
	g.logTails[g.selectedID] = tail
}

func (g *GUI) ensureLogState() {
	if g.logTails == nil {
		g.logTails = map[string]bool{}
	}
	if g.logOffsets == nil {
		g.logOffsets = map[string]fyne.Position{}
	}
}

func logScrollAtBottom(scroll *container.Scroll, pos fyne.Position) bool {
	if scroll == nil {
		return true
	}
	const tolerance float32 = 2
	return pos.Y >= logMaxScrollOffset(scroll)-tolerance
}

func logMaxScrollOffset(scroll *container.Scroll) float32 {
	if scroll == nil || scroll.Content == nil {
		return 0
	}
	maxOffset := scroll.Content.MinSize().Height - scroll.Size().Height
	if maxOffset < 0 {
		return 0
	}
	return maxOffset
}

func (g *GUI) trayMenu() *fyne.Menu {
	items := []*fyne.MenuItem{
		fyne.NewMenuItem(tr("Show"), func() {
			fyne.Do(func() {
				g.window.Show()
				g.window.RequestFocus()
			})
		}),
	}
	if len(g.profiles) > 0 {
		items = append(items, fyne.NewMenuItemSeparator())
		for _, p := range g.profiles {
			items = append(items, g.profileTrayMenuItem(p))
		}
	}
	items = append(items,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("Connect All"), func() {
			fyne.Do(g.connectAll)
		}),
		fyne.NewMenuItem(tr("Disconnect All"), func() {
			fyne.Do(g.disconnectAll)
		}),
		fyne.NewMenuItemSeparator(),
		g.trayQuitMenuItem(),
	)

	return fyne.NewMenu(tr("Wireproxy GUI"), items...)
}

func (g *GUI) profileTrayMenuItem(p profile.Profile) *fyne.MenuItem {
	status := g.profileStatus(p.ID)
	item := fyne.NewMenuItemWithIcon(p.Name, trayStatusIcon(status), nil)

	connect := fyne.NewMenuItem(tr("Connect"), func() {
		fyne.Do(func() {
			g.connectProfileFromTray(p.ID)
		})
	})
	disconnect := fyne.NewMenuItem(tr("Disconnect"), func() {
		fyne.Do(func() {
			g.disconnectProfileFromTray(p.ID)
		})
	})
	if trayStatusActive(status) {
		connect.Disabled = true
	} else {
		disconnect.Disabled = true
	}

	item.ChildMenu = fyne.NewMenu(p.Name,
		disabledMenuItem(tr("Status: {{.Status}}", map[string]any{"Status": trayStatusText(status)})),
		disabledMenuItem(tr("SOCKS5 bind: {{.Address}}", map[string]any{"Address": p.BindAddress()})),
		disabledMenuItem(tr("WireGuard IP: {{.Address}}", map[string]any{"Address": wireGuardAddressText(p)})),
		fyne.NewMenuItemSeparator(),
		connect,
		disconnect,
	)
	return item
}

func disabledMenuItem(label string) *fyne.MenuItem {
	item := fyne.NewMenuItem(label, nil)
	item.Disabled = true
	return item
}

func (g *GUI) trayQuitMenuItem() *fyne.MenuItem {
	quit := fyne.NewMenuItem(tr("Quit"), func() {
		fyne.Do(func() {
			g.shutdown()
			g.app.Quit()
		})
	})
	quit.IsQuit = true
	return quit
}

func (g *GUI) refreshTrayMenu() {
	if g.tray != nil {
		g.tray.SetSystemTrayMenu(g.trayMenu())
	}
}

func (g *GUI) profileStatus(profileID string) string {
	status := g.statuses[profileID]
	if status == "" {
		status = "stopped"
	}
	if g.runner != nil {
		if g.runner.Running(profileID) && status != "stopping" {
			return "running"
		}
	}
	return status
}

func trayStatusConnected(status string) bool {
	return status == "running"
}

func trayStatusActive(status string) bool {
	switch status {
	case "starting", "running", "stopping":
		return true
	default:
		return false
	}
}

func trayStatusIcon(status string) fyne.Resource {
	if trayStatusConnected(status) {
		return connectedTrayIcon
	}
	return disconnectedTrayIcon
}

func trayStatusText(status string) string {
	switch status {
	case "running":
		return tr("connected")
	case "starting":
		return tr("connecting")
	case "stopping":
		return tr("disconnecting")
	case "error":
		return tr("error")
	default:
		return tr("disconnected")
	}
}

func statusSummaryText(p profile.Profile, status string) string {
	return tr("{{.Name}} on {{.Address}} [{{.Status}}]", map[string]any{
		"Name":    p.Name,
		"Address": p.BindAddress(),
		"Status":  trayStatusText(status),
	})
}

func wireGuardAddressText(p profile.Profile) string {
	address := p.WireGuardAddress()
	if address == "not configured" {
		return tr("not configured")
	}
	return address
}

func (g *GUI) events() {
	go func() {
		for {
			select {
			case <-g.ctx.Done():
				return
			case ev := <-g.runner.Events():
				fyne.Do(func() {
					g.handleEvent(ev)
				})
			}
		}
	}()
}

func (g *GUI) handleEvent(ev wireproxy.Event) {
	if !g.hasProfile(ev.ProfileID) {
		return
	}
	switch ev.Type {
	case wireproxy.EventStarted:
		g.statuses[ev.ProfileID] = "running"
	case wireproxy.EventStopped:
		g.statuses[ev.ProfileID] = "stopped"
	case wireproxy.EventError:
		g.statuses[ev.ProfileID] = "error"
	}
	g.appendLog(ev.ProfileID, ev.At, ev.Message)
	g.refresh()
}

func (g *GUI) startAutoProfiles() {
	profiles := make([]profile.Profile, 0, len(g.profiles))
	for _, p := range g.profiles {
		if !p.AutoStart {
			continue
		}
		profiles = append(profiles, p)
	}
	err := duplicateBindError(profiles)
	if err != nil {
		for _, p := range profiles {
			g.statuses[p.ID] = "error"
			g.appendLog(p.ID, time.Now(), err.Error())
		}
		g.refresh()
		return
	}

	for _, p := range profiles {
		_ = g.startProfile(p)
	}
	g.refresh()
}

func (g *GUI) shutdown() {
	g.shutdownOnce.Do(func() {
		if g.runner != nil {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownWaitTimeout)
			err := g.runner.StopAllAndWait(ctx)
			cancel()
			if err != nil {
				fyne.LogError(tr("Error stopping profiles during shutdown"), err)
			}
		}
		if g.cancel != nil {
			g.cancel()
		}
	})
}

func (g *GUI) addProfile() {
	p := profile.New(tr("New profile"), "", profile.NextAvailablePort(g.profiles))
	g.profiles = append(g.profiles, p)
	g.statuses[p.ID] = "stopped"
	g.selectedID = p.ID
	err := g.saveAll()
	if err != nil {
		g.showError("Save profile", err)
	}
	g.refresh()
	g.selectByID(p.ID)
}

func (g *GUI) importProfiles() {
	path, err := g.profileFileDialog().OpenProfilePath()
	if err != nil {
		g.showError("Import profiles", err)
		return
	}
	if path == "" {
		return
	}
	err = g.importProfilesFromPath(path)
	if err != nil {
		g.showError("Import profiles", err)
	}
}

func (g *GUI) importProfilesFromPath(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	imported, err := profile.DecodeImport(filepath.Base(path), data)
	if err != nil {
		return err
	}
	imported = profile.PrepareImported(imported, g.profiles)
	for _, p := range imported {
		g.profiles = append(g.profiles, p)
		g.statuses[p.ID] = "stopped"
		g.appendLog(p.ID, time.Now(), "imported profile")
	}
	if len(imported) > 0 {
		g.selectedID = imported[0].ID
	}
	err = g.saveAll()
	if err != nil {
		return fmt.Errorf("save imported profiles: %w", err)
	}
	g.refresh()
	g.selectByID(g.selectedID)
	return nil
}

func (g *GUI) exportSelected() {
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	g.exportProfiles([]profile.Profile{p}, safeFileName(p.Name)+".json")
}

func (g *GUI) exportAll() {
	if len(g.profiles) == 0 {
		return
	}
	g.exportProfiles(g.profiles, "wireproxy-profiles.json")
}

func (g *GUI) exportProfiles(profiles []profile.Profile, fileName string) {
	path, err := g.profileFileDialog().SaveProfilesPath(fileName)
	if err != nil {
		g.showError("Export profiles", err)
		return
	}
	if path == "" {
		return
	}
	err = exportProfilesToPath(profiles, path)
	if err != nil {
		g.showError("Export profiles", err)
	}
}

func exportProfilesToPath(profiles []profile.Profile, path string) error {
	data, err := profile.EncodeBundle(profiles)
	if err != nil {
		return err
	}
	err = os.WriteFile(path, data, 0o600)
	if err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (g *GUI) saveSelected() {
	err := g.saveSelectedProfile()
	if err != nil {
		g.showError("Save profile", err)
	}
}

func (g *GUI) saveSelectedProfile() error {
	idx := g.selectedIndex()
	if idx < 0 {
		return nil
	}
	existing := g.profiles[idx]
	p, err := g.profileFromForm(existing)
	if err != nil {
		return err
	}
	if g.runtimeLocked(existing) && runtimeConfigChanged(existing, p) {
		return errRuntimeProfileEdit
	}
	if !profileFieldsChanged(existing, p) {
		return nil
	}
	p.Touch()
	g.profiles[idx] = p
	err = g.saveAll()
	if err != nil {
		return err
	}
	g.selectedID = p.ID
	g.appendLog(p.ID, time.Now(), "saved profile")
	g.refresh()
	return nil
}

func (g *GUI) deleteSelected() {
	idx := g.selectedIndex()
	if idx < 0 {
		return
	}
	p := g.profiles[idx]
	dialog.ShowConfirm(tr("Delete profile"), tr("Delete {{.Name}}?", map[string]any{"Name": p.Name}), func(ok bool) {
		if !ok {
			return
		}
		g.runner.Stop(p.ID)
		g.profiles = append(g.profiles[:idx], g.profiles[idx+1:]...)
		delete(g.statuses, p.ID)
		delete(g.logs, p.ID)
		delete(g.logTails, p.ID)
		delete(g.logOffsets, p.ID)
		g.selectedID = ""
		if len(g.profiles) > 0 {
			g.selectedID = g.profiles[0].ID
		}
		err := g.saveAll()
		if err != nil {
			g.showError("Delete profile", err)
			return
		}
		g.refresh()
		g.selectByID(g.selectedID)
	}, g.window)
}

func (g *GUI) connectSelected() {
	err := g.saveSelectedProfile()
	if err != nil {
		g.showError("Connect profile", err)
		return
	}
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	err = g.runningBindConflict(p)
	if err != nil {
		g.showError("Connect profile", err)
		return
	}
	err = g.startProfile(p)
	if err != nil {
		g.showError("Connect profile", err)
	}
}

func (g *GUI) disconnectSelected() {
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	if !g.runner.Stop(p.ID) {
		g.statuses[p.ID] = "stopped"
	} else {
		g.statuses[p.ID] = "stopping"
	}
	g.refresh()
}

func (g *GUI) connectAll() {
	err := g.saveSelectedProfile()
	if err != nil {
		g.showError("Connect All", err)
		return
	}
	err = duplicateBindError(g.profiles)
	if err != nil {
		g.showError("Connect All", err)
		return
	}

	var errs []error
	for _, p := range g.profiles {
		err = g.startProfile(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	if len(errs) > 0 {
		g.showError("Connect All", errors.Join(errs...))
	}
}

func (g *GUI) disconnectAll() {
	g.runner.StopAll()
	for _, p := range g.profiles {
		if g.runner.Running(p.ID) || runtimeLockedStatus(g.statuses[p.ID]) {
			g.statuses[p.ID] = "stopping"
		}
	}
	g.refresh()
}

func (g *GUI) startProfile(p profile.Profile) error {
	if g.runner.Running(p.ID) || runtimeLockedStatus(g.statuses[p.ID]) {
		return nil
	}
	g.statuses[p.ID] = "starting"
	g.appendLog(p.ID, time.Now(), "connecting profile")
	g.refresh()
	err := g.runner.Start(g.ctx, p)
	if err != nil {
		g.statuses[p.ID] = "error"
		g.appendLog(p.ID, time.Now(), err.Error())
		g.refresh()
		return err
	}
	g.statuses[p.ID] = "running"
	g.refresh()
	return nil
}

func (g *GUI) connectProfileFromTray(profileID string) {
	p, ok := g.profileByID(profileID)
	if !ok {
		return
	}
	err := g.runningBindConflict(p)
	if err != nil {
		g.showError("Connect profile", err)
		return
	}
	err = g.startProfile(p)
	if err != nil {
		g.showError("Connect profile", err)
	}
}

func (g *GUI) disconnectProfileFromTray(profileID string) {
	p, ok := g.profileByID(profileID)
	if !ok {
		return
	}
	if !g.runner.Stop(profileID) {
		g.statuses[profileID] = "stopped"
	} else {
		g.statuses[profileID] = "stopping"
	}
	g.appendLog(p.ID, time.Now(), "disconnect requested from tray")
	g.refresh()
}

func (g *GUI) profileFromForm(existing profile.Profile) (profile.Profile, error) {
	port, err := strconv.Atoi(strings.TrimSpace(g.portEntry.Text))
	if err != nil {
		return profile.Profile{}, profile.ErrSocksPortNotNumber
	}
	existing.Name = strings.TrimSpace(g.nameEntry.Text)
	existing.SocksHost = strings.TrimSpace(g.hostEntry.Text)
	existing.SocksPort = port
	existing.AutoStart = g.autoStartCheck.Checked
	existing.WireGuardConfig = strings.TrimSpace(g.configEntry.Text)
	existing.Normalize()
	if strings.TrimSpace(existing.Name) == "" {
		return profile.Profile{}, profile.ErrProfileNameRequired
	}
	if strings.TrimSpace(existing.SocksHost) == "" {
		return profile.Profile{}, profile.ErrSocksHostRequired
	}
	if existing.SocksPort < 1 || existing.SocksPort > 65535 {
		return profile.Profile{}, profile.ErrSocksPortOutOfRange
	}
	return existing, nil
}

func (g *GUI) saveAll() error {
	return g.store.Save(g.profiles)
}

func (g *GUI) profileFileDialog() profileFileDialog {
	if g.files != nil {
		return g.files
	}
	return nativeProfileFileDialog{}
}

func (g *GUI) showSelected() {
	idx := g.selectedIndex()
	if idx < 0 {
		g.setFormEnabled(false)
		g.nameEntry.SetText("")
		g.hostEntry.SetText("")
		g.portEntry.SetText("")
		g.autoStartCheck.SetChecked(false)
		g.configEntry.SetText("")
		g.setLogText("", true)
		g.statusLabel.SetText(tr("No profile selected"))
		return
	}

	p := g.profiles[idx]
	g.setFormEnabled(true)
	g.nameEntry.SetText(p.Name)
	g.hostEntry.SetText(p.SocksHost)
	g.portEntry.SetText(strconv.Itoa(p.SocksPort))
	g.autoStartCheck.SetChecked(p.AutoStart)
	g.configEntry.SetText(p.WireGuardConfig)
	g.statusLabel.SetText(statusSummaryText(p, g.profileStatus(p.ID)))
	g.setLogText(strings.Join(g.logs[p.ID], "\n"), false)
	g.refreshButtons()
}

func (g *GUI) setFormEnabled(enabled bool) {
	widgets := []fyne.Disableable{
		g.nameEntry,
		g.hostEntry,
		g.portEntry,
		g.autoStartCheck,
		g.configEntry,
		g.saveButton,
		g.deleteButton,
		g.connectButton,
		g.disconnectButton,
		g.exportButton,
	}
	for _, w := range widgets {
		if enabled {
			w.Enable()
		} else {
			w.Disable()
		}
	}
}

func (g *GUI) refresh() {
	if g.list != nil {
		g.list.Refresh()
	}
	g.refreshButtons()
	if g.selectedID != "" {
		idx := g.selectedIndex()
		if idx >= 0 {
			p := g.profiles[idx]
			g.statusLabel.SetText(statusSummaryText(p, g.profileStatus(p.ID)))
			g.setLogText(strings.Join(g.logs[p.ID], "\n"), false)
		}
	}
	g.refreshTrayMenu()
}

func (g *GUI) refreshButtons() {
	idx := g.selectedIndex()
	if idx < 0 {
		return
	}
	p := g.profiles[idx]
	locked := g.runner.Running(p.ID) || runtimeLockedStatus(g.statuses[p.ID])
	if locked {
		g.connectButton.Disable()
		g.disconnectButton.Enable()
		return
	}
	g.connectButton.Enable()
	g.disconnectButton.Disable()
}

func (g *GUI) currentProfile() (profile.Profile, bool) {
	idx := g.selectedIndex()
	if idx < 0 {
		return profile.Profile{}, false
	}
	return g.profiles[idx], true
}

func (g *GUI) profileByID(profileID string) (profile.Profile, bool) {
	for _, p := range g.profiles {
		if p.ID == profileID {
			return p, true
		}
	}
	return profile.Profile{}, false
}

func (g *GUI) selectedIndex() int {
	for i, p := range g.profiles {
		if p.ID == g.selectedID {
			return i
		}
	}
	return -1
}

func (g *GUI) hasProfile(profileID string) bool {
	for _, p := range g.profiles {
		if p.ID == profileID {
			return true
		}
	}
	return false
}

func (g *GUI) runningBindConflict(candidate profile.Profile) error {
	for _, p := range g.profiles {
		if p.ID == candidate.ID || p.BindAddress() != candidate.BindAddress() {
			continue
		}
		if g.runner.Running(p.ID) || runtimeLockedStatus(g.statuses[p.ID]) {
			return fmt.Errorf(
				"%w: %s is already used by active profile %q",
				profile.ErrDuplicateBindAddress,
				candidate.BindAddress(),
				p.Name,
			)
		}
	}
	return nil
}

func (g *GUI) runtimeLocked(p profile.Profile) bool {
	return g.runner.Running(p.ID) || runtimeLockedStatus(g.statuses[p.ID])
}

func runtimeLockedStatus(status string) bool {
	switch status {
	case "starting", "running", "stopping":
		return true
	default:
		return false
	}
}

func runtimeConfigChanged(before, after profile.Profile) bool {
	before.Normalize()
	after.Normalize()
	return before.WireGuardConfig != after.WireGuardConfig || before.BindAddress() != after.BindAddress()
}

func profileFieldsChanged(before, after profile.Profile) bool {
	before.Normalize()
	after.Normalize()
	return before.Name != after.Name ||
		before.WireGuardConfig != after.WireGuardConfig ||
		before.SocksHost != after.SocksHost ||
		before.SocksPort != after.SocksPort ||
		before.AutoStart != after.AutoStart
}

func duplicateBindError(profiles []profile.Profile) error {
	bind, first, second, ok := profile.DuplicateBindAddress(profiles)
	if !ok {
		return nil
	}
	return fmt.Errorf("%w: %s is used by %q and %q", profile.ErrDuplicateBindAddress, bind, first, second)
}

func (g *GUI) selectByID(id string) {
	for i, p := range g.profiles {
		if p.ID == id {
			if g.list != nil {
				g.list.Select(i)
			}
			g.showSelected()
			return
		}
	}
	g.showSelected()
}

func (g *GUI) appendLog(profileID string, at time.Time, message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	line := fmt.Sprintf("%s  %s", at.Format("15:04:05"), localizedLogMessage(message))
	g.logs[profileID] = append(g.logs[profileID], line)
	if len(g.logs[profileID]) > maxLogLines {
		g.logs[profileID] = g.logs[profileID][len(g.logs[profileID])-maxLogLines:]
	}
	if profileID == g.selectedID {
		g.setLogText(strings.Join(g.logs[profileID], "\n"), false)
	}
}

func (g *GUI) showError(title string, err error) {
	if err == nil {
		return
	}
	dialog.ShowError(displayError(displayErrorText(title, err)), g.window)
}

func displayErrorText(title string, err error) string {
	titleText := tr(title)
	message := trimErrorTitlePrefix(titleText, localizedErrorText(err))
	return tr("{{.Title}}: {{.Message}}", map[string]any{
		"Title":   titleText,
		"Message": message,
	})
}

func trimErrorTitlePrefix(title, message string) string {
	prefix := title + ": "
	if len(message) >= len(prefix) && strings.EqualFold(message[:len(prefix)], prefix) {
		return message[len(prefix):]
	}
	return message
}

func (g *GUI) showUsageGuide() {
	guide := dialog.NewCustom(tr("Usage guide"), tr("Close"), newUsageGuideContent(usageGuideText()), g.window)
	guide.Resize(fyne.NewSize(720, 520))
	guide.Show()
}

func usageGuideText() string {
	paragraphs := make([]string, 0, len(usageGuideParagraphs))
	for _, paragraph := range usageGuideParagraphs {
		paragraphs = append(paragraphs, tr(paragraph))
	}
	return strings.Join(paragraphs, "\n\n")
}

func newUsageGuideContent(text string) *widget.RichText {
	guide := widget.NewRichTextFromMarkdown(text)
	guide.Wrapping = fyne.TextWrapWord
	guide.Scroll = fyne.ScrollVerticalOnly
	return guide
}

func localizedLogMessage(message string) string {
	return localizedText(message)
}

func safeFileName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return "profile"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "profile"
	}
	return out
}
