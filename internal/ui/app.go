package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/buildinfo"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

const (
	shutdownWaitTimeout = 5 * time.Second
	configEditorMinRows = 3
)

var usageGuideParagraphs = []string{
	"Add a WireGuard or Tailscale profile, then choose a SOCKS5 host and port for that profile.",
	"Each connected, connecting, or disconnecting profile must use a unique SOCKS5 bind address, such as 127.0.0.1:1080 and 127.0.0.1:1081.",
	"Connect starts the embedded backend and SOCKS5 listener for the selected profile. WireGuard profiles use the embedded WireGuard engine. Tailscale profiles use an embedded tsnet node. The app does not launch the wireproxy, tailscale, or tailscaled command-line tools. Connect All starts every saved profile. Runtime status is shown in the selected profile log.",
	"Tailscale auth is not the same as WireGuard config. Paste an auth key and click Login or Connect to register this app as a Tailscale device; leave Auth key empty for a browser sign-in URL in the profile log. If Tailscale requires device approval, the profile log will ask you to approve the device in the admin console and the app will detect approval automatically. After authentication succeeds, the app removes the saved auth key, marks the profile authenticated, and keeps the auth field locked until you click Logout. Logout removes this profile's stored Tailscale state and unlocks auth. Auth key, control URL, hostname, backend, and SOCKS5 bind fields are locked while that profile is connected, connecting, or disconnecting. Exit-node mode, selected exit node, and LAN access can be changed while connected; click Save to apply them without reconnecting.",
	"Exit-node choices are not live-updating. Connect the Tailscale profile, click Refresh to load available exit-node devices from that tailnet, and click Refresh again after tailnet devices or approvals change. You can also type a node ID, hostname, or Tailscale IP manually. Automatic exit asks Tailscale to choose an available exit node.",
	"The profile log follows the newest line by default. Scrolling up pauses following so you can read earlier output; scrolling back to the bottom follows new lines again.",
	"The tray menu has one profile row per profile. The colored dot shows connection status, and each submenu shows status text, SOCKS5 bind address, backend detail, and Connect or Disconnect actions. The icon is green only while connected; other states use a red icon and the status text shows disconnected, connecting, disconnecting, or error.",
	"Disconnect a connected or connecting profile, and wait for it to finish disconnecting, before changing its backend, tunnel configuration, or SOCKS5 bind address. Profile name and startup preference can be changed while connected.",
	"Import and Export open the operating system's native file dialog and use the selected filesystem path. Export writes profiles as JSON. Import accepts exported JSON bundles or WireGuard .conf files. Tailscale node state and the local authenticated marker are not exported, so imported Tailscale profiles require Login again.",
	"Closing the window hides it when tray support is available. Use Quit from the tray menu to stop all profiles, wait briefly for them to close, and exit.",
}

var runOnUI = fyne.Do

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
	core   Application
	ctx    context.Context
	cancel context.CancelFunc
	files  profileFileDialog
	tray   desktop.App

	selectedID string
	logTails   map[string]bool
	logOffsets map[string]fyne.Position

	list           *widget.List
	kindSelect     *widget.Select
	nameEntry      *widget.Entry
	hostEntry      *widget.Entry
	portEntry      *widget.Entry
	autoStartCheck *widget.Check
	configEntry    *widget.Entry
	configLabel    *widget.Label
	tailscaleForm  *widget.Form
	tsHostname     *widget.Entry
	tsAuthKey      *widget.Entry
	tsLoginButton  *widget.Button
	tsControlURL   *widget.Entry
	tsExitNode     *widget.SelectEntry
	tsExitRefresh  *widget.Button
	tsExitValues   map[string]string
	tsAutoExit     *widget.Check
	tsAllowLAN     *widget.Check
	tsEphemeral    *widget.Check
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

// Application is the inbound application boundary consumed by the Fyne adapter.
// Implementations own profile state and business rules; GUI owns widget state only.
type Application interface {
	Changes() <-chan application.Change
	Profiles() []profile.Profile
	Profile(string) (profile.Profile, bool)
	Status(string) application.Status
	Logs(string) []application.LogEntry
	RuntimeLocked(string) bool
	Add(string) (profile.Profile, error)
	Save(profile.Profile) (application.SaveResult, error)
	Delete(string) error
	Import(string, []byte) ([]profile.Profile, error)
	Export([]string) ([]byte, error)
	Connect(string) error
	Login(string) error
	ConnectAll() error
	AutoConnect() error
	Disconnect(string) bool
	DisconnectFromTray(string) bool
	DisconnectAll()
	ExitNodes(context.Context, string) ([]connection.ExitNode, error)
	Logout(context.Context, string) error
	Shutdown(context.Context) error
}

type profileFileDialog interface {
	OpenProfilePath() (string, error)
	SaveProfilesPath(fileName string) (string, error)
}

type displayError string

func (e displayError) Error() string {
	return string(e)
}

func Run(core Application, loadErr error) {
	fyneApp := app.NewWithID("com.github.nrngnl.wireproxy-gui")
	applyAppTheme(fyneApp)
	ctx, cancel := context.WithCancel(context.Background())

	gui := &GUI{
		app:    fyneApp,
		core:   core,
		ctx:    ctx,
		cancel: cancel,
		files:  nativeProfileFileDialog{},
	}
	profiles := core.Profiles()
	if len(profiles) > 0 {
		gui.selectedID = profiles[0].ID
	}
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

func (g *GUI) build() {
	g.window = g.app.NewWindow(tr(buildinfo.WindowTitle()))
	g.window.Resize(fyne.NewSize(1120, 760))

	g.kindSelect = widget.NewSelect([]string{tr("WireGuard"), tr("Tailscale")}, func(_ string) {
		g.updateBackendVisibility(g.selectedBackendKind())
	})
	g.nameEntry = widget.NewEntry()
	g.hostEntry = widget.NewEntry()
	g.portEntry = widget.NewEntry()
	g.autoStartCheck = widget.NewCheck(tr("Connect when app opens"), nil)

	g.configEntry = newWireGuardConfigEntry()
	g.configLabel = widget.NewLabel(tr("WireGuard configuration"))
	g.setupTailscaleForm()

	g.setupLogView()

	g.statusLabel = widget.NewLabel(tr("No profile selected"))

	g.saveButton = widget.NewButtonWithIcon(tr("Save"), theme.DocumentSaveIcon(), g.saveSelected)
	g.deleteButton = widget.NewButtonWithIcon(tr("Delete"), theme.DeleteIcon(), g.deleteSelected)
	g.connectButton = widget.NewButtonWithIcon(tr("Connect"), theme.MediaPlayIcon(), g.connectSelected)
	g.disconnectButton = widget.NewButtonWithIcon(tr("Disconnect"), theme.MediaStopIcon(), g.disconnectSelected)
	g.exportButton = widget.NewButtonWithIcon(tr("Export"), theme.UploadIcon(), g.exportSelected)

	g.list = widget.NewList(
		func() int { return len(g.core.Profiles()) },
		newProfileListItem,
		g.updateProfileListItem,
	)
	g.list.OnSelected = func(id widget.ListItemID) {
		profiles := g.core.Profiles()
		if id >= 0 && id < len(profiles) {
			g.selectedID = profiles[id].ID
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
			{Text: tr("Backend"), Widget: g.kindSelect},
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
		container.NewVBox(g.statusLabel, form, g.configLabel),
		actionBar,
		nil,
		nil,
		newConfigLogSplit(
			container.NewStack(g.configEntry, g.tailscaleForm),
			container.NewBorder(widget.NewLabel(tr("Profile log")), nil, nil, nil, g.logScroll),
		),
	)

	split := container.NewHSplit(left, detail)
	split.Offset = 0.30
	g.window.SetContent(split)

	g.showSelected()
	if len(g.core.Profiles()) > 0 {
		g.list.Select(0)
	}
}

func newWireGuardConfigEntry() *widget.Entry {
	entry := widget.NewMultiLineEntry()
	entry.SetMinRowsVisible(configEditorMinRows)
	entry.Wrapping = fyne.TextWrapOff
	return entry
}

func (g *GUI) setupTailscaleForm() {
	g.tsHostname = widget.NewEntry()
	g.tsHostname.SetPlaceHolder(tr("Defaults to profile name"))
	g.tsAuthKey = widget.NewPasswordEntry()
	g.tsAuthKey.SetPlaceHolder(tr("Optional; paste auth key, or leave empty for browser sign-in"))
	g.tsLoginButton = widget.NewButtonWithIcon(tr("Login"), theme.LoginIcon(), g.tailscaleAuthActionSelected)
	g.tsControlURL = widget.NewEntry()
	g.tsControlURL.SetPlaceHolder(tr("Optional; leave empty for Tailscale"))
	g.tsExitValues = map[string]string{}
	g.tsExitNode = widget.NewSelectEntry(nil)
	g.tsExitNode.PlaceHolder = tr("Refresh to choose a device, or type a node/IP")
	g.tsExitRefresh = widget.NewButtonWithIcon(tr("Refresh"), theme.ViewRefreshIcon(), g.refreshExitNodeOptions)
	g.tsAutoExit = widget.NewCheck(tr("Use automatic exit node"), func(_ bool) {
		g.updateExitNodeControlState()
	})
	g.tsAllowLAN = widget.NewCheck(tr("Allow LAN access while using exit node"), nil)
	g.tsEphemeral = widget.NewCheck(tr("Register as ephemeral node"), nil)
	authKeyControl := container.NewBorder(nil, nil, nil, g.tsLoginButton, g.tsAuthKey)
	exitNodeControl := container.NewBorder(nil, nil, nil, g.tsExitRefresh, g.tsExitNode)
	g.tailscaleForm = &widget.Form{
		Items: []*widget.FormItem{
			{
				Text:     tr("Tailscale hostname"),
				Widget:   g.tsHostname,
				HintText: tr("Used when registering this app as a Tailscale device."),
			},
			{
				Text:     tr("Auth key"),
				Widget:   authKeyControl,
				HintText: tr("Paste an auth key, then click Login. Leave it empty to use the browser sign-in URL in the profile log. After authentication succeeds, the saved auth key is removed and this field stays locked until Logout. Logout removes this profile's stored Tailscale state and unlocks auth."),
			},
			{
				Text:     tr("Control URL"),
				Widget:   g.tsControlURL,
				HintText: tr("Only needed for a custom control server."),
			},
			{
				Text:     tr("Exit node"),
				Widget:   exitNodeControl,
				HintText: tr("Connect first, then refresh to list devices from this tailnet. Save while connected to apply exit-node changes immediately."),
			},
			{
				Text:     tr("Exit node mode"),
				Widget:   g.tsAutoExit,
				HintText: tr("Automatic exit asks Tailscale to choose an available exit node."),
			},
			{Text: tr("LAN access"), Widget: g.tsAllowLAN},
			{Text: tr("Node lifetime"), Widget: g.tsEphemeral},
		},
	}
}

func (g *GUI) selectedBackendKind() profile.BackendKind {
	if g.kindSelect == nil {
		return profile.BackendWireGuard
	}
	switch g.kindSelect.Selected {
	case tr("Tailscale"):
		return profile.BackendTailscale
	default:
		return profile.BackendWireGuard
	}
}

func backendKindLabel(kind profile.BackendKind) string {
	if (profile.Profile{Kind: kind}).IsTailscale() {
		return tr("Tailscale")
	}
	return tr("WireGuard")
}

func (g *GUI) updateBackendVisibility(kind profile.BackendKind) {
	if g.configEntry == nil || g.tailscaleForm == nil || g.configLabel == nil {
		return
	}
	if (profile.Profile{Kind: kind}).IsTailscale() {
		g.configLabel.SetText(tr("Tailscale configuration"))
		g.configEntry.Hide()
		g.tailscaleForm.Show()
		g.updateExitNodeControlState()
		return
	}
	g.configLabel.SetText(tr("WireGuard configuration"))
	g.tailscaleForm.Hide()
	g.configEntry.Show()
}

func (g *GUI) setTailscaleForm(config profile.TailscaleConfig) {
	config.Normalize()
	g.tsHostname.SetText(config.Hostname)
	if config.Authenticated {
		g.tsAuthKey.SetText("")
	} else {
		g.tsAuthKey.SetText(config.AuthKey)
	}
	g.tsControlURL.SetText(config.ControlURL)
	g.tsExitNode.SetText(config.ExitNode)
	g.tsAutoExit.SetChecked(config.AutoExitNode)
	g.tsAllowLAN.SetChecked(config.ExitNodeAllowLANAccess)
	g.tsEphemeral.SetChecked(config.Ephemeral)
	g.updateTailscaleAuthControlState(config)
}

func (g *GUI) updateTailscaleAuthControlState(config profile.TailscaleConfig) {
	if g.tsAuthKey == nil || g.tsLoginButton == nil {
		return
	}
	if config.Authenticated {
		g.tsAuthKey.SetPlaceHolder(tr("Authenticated"))
		g.tsLoginButton.SetText(tr("Logout"))
		g.tsLoginButton.SetIcon(theme.LogoutIcon())
		return
	}
	g.tsAuthKey.SetPlaceHolder(tr("Optional; paste auth key, or leave empty for browser sign-in"))
	g.tsLoginButton.SetText(tr("Login"))
	g.tsLoginButton.SetIcon(theme.LoginIcon())
}

func (g *GUI) updateExitNodeControlState() {
	enabled := g.tsAutoExit != nil && !g.tsAutoExit.Checked && !g.tsAutoExit.Disabled()
	g.setExitNodeControlsEnabled(enabled)
}

func (g *GUI) setExitNodeControlsEnabled(enabled bool) {
	if g.tsExitNode != nil {
		if enabled {
			g.tsExitNode.Enable()
		} else {
			g.tsExitNode.Disable()
		}
	}
	if g.tsExitRefresh != nil {
		if enabled {
			g.tsExitRefresh.Enable()
		} else {
			g.tsExitRefresh.Disable()
		}
	}
}

func (g *GUI) refreshExitNodeOptions() {
	p, ok := g.currentProfile()
	if !ok || !p.IsTailscale() {
		return
	}
	profileID := p.ID
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		nodes, err := g.core.ExitNodes(ctx, profileID)
		runOnUI(func() {
			if err != nil {
				g.showError("Refresh exit nodes", err)
				return
			}
			g.applyExitNodeOptions(nodes)
		})
	}()
}

func (g *GUI) applyExitNodeOptions(nodes []connection.ExitNode) {
	if g.tsExitNode == nil {
		return
	}
	current := g.selectedExitNodeValue()
	values := make(map[string]string, len(nodes))
	options := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if strings.TrimSpace(node.ID) == "" {
			continue
		}
		label := exitNodeOptionLabel(node)
		if label == "" {
			continue
		}
		for {
			if _, exists := values[label]; !exists {
				break
			}
			label = label + " " + node.ID
		}
		values[label] = node.ID
		options = append(options, label)
	}
	g.tsExitValues = values
	g.tsExitNode.SetOptions(options)
	if current == "" {
		return
	}
	if label, ok := exitNodeLabelForValue(values, current); ok {
		g.tsExitNode.SetText(label)
		return
	}
	g.tsExitNode.SetText(current)
}

func (g *GUI) selectedExitNodeValue() string {
	if g.tsExitNode == nil {
		return ""
	}
	value := strings.TrimSpace(g.tsExitNode.Text)
	if g.tsExitValues != nil {
		if mapped, ok := g.tsExitValues[value]; ok {
			return mapped
		}
	}
	return value
}

func exitNodeOptionLabel(node connection.ExitNode) string {
	label := strings.TrimSpace(node.Name)
	if label == "" {
		label = strings.TrimSpace(node.ID)
	}
	if label == "" && len(node.TailscaleIPs) > 0 {
		label = node.TailscaleIPs[0]
	}
	return label
}

func exitNodeLabelForValue(values map[string]string, value string) (string, bool) {
	for label, mapped := range values {
		if mapped == value {
			return label, true
		}
	}
	return "", false
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
	profiles := g.core.Profiles()
	if id < 0 || id >= len(profiles) {
		statusIcon.SetResource(disconnectedTrayIcon)
		name.SetText("")
		bind.SetText("")
		return
	}

	p := profiles[id]
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
			runOnUI(func() {
				g.window.Show()
				g.window.RequestFocus()
			})
		}),
	}
	profiles := g.core.Profiles()
	if len(profiles) > 0 {
		items = append(items, fyne.NewMenuItemSeparator())
		for _, p := range profiles {
			items = append(items, g.profileTrayMenuItem(p))
		}
	}
	items = append(items,
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem(tr("Connect All"), func() {
			runOnUI(g.connectAll)
		}),
		fyne.NewMenuItem(tr("Disconnect All"), func() {
			runOnUI(g.disconnectAll)
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
		runOnUI(func() {
			g.connectProfileFromTray(p.ID)
		})
	})
	disconnect := fyne.NewMenuItem(tr("Disconnect"), func() {
		runOnUI(func() {
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
		disabledMenuItem(profileNetworkDetailText(p)),
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
		runOnUI(func() {
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
	return string(g.core.Status(profileID))
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
	if strings.TrimSpace(address) == "" {
		return tr("not configured")
	}
	return address
}

func profileNetworkDetailText(p profile.Profile) string {
	if p.IsTailscale() {
		hostname := p.TailscaleConfig.Hostname
		if hostname == "" {
			hostname = p.Name
		}
		return tr("Tailscale node: {{.Name}}", map[string]any{"Name": hostname})
	}
	return tr("WireGuard IP: {{.Address}}", map[string]any{"Address": wireGuardAddressText(p)})
}

func (g *GUI) events() {
	go func() {
		changes := g.core.Changes()
		for {
			select {
			case <-g.ctx.Done():
				return
			case change, ok := <-changes:
				if !ok {
					return
				}
				runOnUI(func() {
					g.handleChange(change)
				})
			}
		}
	}()
}

func (g *GUI) handleChange(change application.Change) {
	g.refresh()
	if change.Err != nil {
		if title := operationErrorTitle(change.Operation); title != "" {
			g.showError(title, change.Err)
		}
	}
	if change.RuntimeEvent == connection.EventStarted && change.ProfileID == g.selectedID {
		if p, ok := g.profileByID(change.ProfileID); ok && p.IsTailscale() {
			g.refreshExitNodeOptions()
		}
	}
}

func operationErrorTitle(operation application.Operation) string {
	switch operation {
	case application.OperationConnect:
		return "Connect profile"
	case application.OperationConnectAll:
		return "Connect All"
	case application.OperationLogin:
		return "Login to Tailscale"
	case application.OperationSave:
		return "Save profile"
	default:
		return ""
	}
}

func (g *GUI) startAutoProfiles() {
	_ = g.core.AutoConnect()
	g.refresh()
}

func (g *GUI) shutdown() {
	if g.cancel != nil {
		g.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWaitTimeout)
	err := g.core.Shutdown(ctx)
	cancel()
	if err != nil {
		fyne.LogError(tr("Error stopping profiles during shutdown"), err)
	}
}

func (g *GUI) addProfile() {
	p, err := g.core.Add(tr("New profile"))
	if err != nil {
		g.showError("Save profile", err)
		return
	}
	g.selectedID = p.ID
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
	imported, err := g.core.Import(filepath.Base(path), data)
	if err != nil {
		return err
	}
	if len(imported) > 0 {
		g.selectedID = imported[0].ID
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
	profiles := g.core.Profiles()
	if len(profiles) == 0 {
		return
	}
	g.exportProfiles(profiles, "wireproxy-profiles.json")
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
	profileIDs := make([]string, 0, len(profiles))
	for _, item := range profiles {
		profileIDs = append(profileIDs, item.ID)
	}
	data, err := g.core.Export(profileIDs)
	if err == nil {
		err = exportProfilesToPath(data, path)
	}
	if err != nil {
		g.showError("Export profiles", err)
	}
}

func exportProfilesToPath(data []byte, path string) error {
	err := os.WriteFile(path, data, 0o600)
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
	existing, ok := g.currentProfile()
	if !ok {
		return nil
	}
	p, err := g.profileFromForm(existing)
	if err != nil {
		return err
	}
	_, err = g.core.Save(p)
	if err != nil {
		return err
	}
	g.selectedID = p.ID
	g.refresh()
	return nil
}

func (g *GUI) deleteSelected() {
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	dialog.ShowConfirm(tr("Delete profile"), tr("Delete {{.Name}}?", map[string]any{"Name": p.Name}), func(ok bool) {
		if !ok {
			return
		}
		err := g.core.Delete(p.ID)
		if err != nil {
			g.showError("Delete profile", err)
			return
		}
		delete(g.logTails, p.ID)
		delete(g.logOffsets, p.ID)
		g.selectedID = ""
		profiles := g.core.Profiles()
		if len(profiles) > 0 {
			g.selectedID = profiles[0].ID
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
	err = g.core.Connect(p.ID)
	if err != nil {
		g.showError("Connect profile", err)
	}
}

func (g *GUI) tailscaleAuthActionSelected() {
	p, ok := g.currentProfile()
	if ok && p.IsTailscale() && p.TailscaleConfig.Authenticated {
		g.logoutTailscaleSelected()
		return
	}
	g.loginTailscaleSelected()
}

func (g *GUI) loginTailscaleSelected() {
	err := g.saveSelectedProfile()
	if err != nil {
		g.showError("Login to Tailscale", err)
		return
	}
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	if !p.IsTailscale() {
		g.showError("Login to Tailscale", displayError("select a Tailscale profile before login"))
		return
	}
	err = g.core.Login(p.ID)
	if err != nil {
		g.showError("Login to Tailscale", err)
	}
}

func (g *GUI) logoutTailscaleSelected() {
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	if !p.IsTailscale() {
		g.showError("Logout from Tailscale", displayError("select a Tailscale profile before logout"))
		return
	}
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	err := g.core.Logout(ctx, p.ID)
	if err != nil {
		g.showError("Logout from Tailscale", err)
		return
	}
	g.selectedID = p.ID
	g.refresh()
	g.showSelected()
}

func (g *GUI) disconnectSelected() {
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	g.core.Disconnect(p.ID)
	g.refresh()
}

func (g *GUI) connectAll() {
	err := g.saveSelectedProfile()
	if err != nil {
		g.showError("Connect All", err)
		return
	}
	err = g.core.ConnectAll()
	if err != nil {
		g.showError("Connect All", err)
	}
}

func (g *GUI) disconnectAll() {
	g.core.DisconnectAll()
	g.refresh()
}

func (g *GUI) connectProfileFromTray(profileID string) {
	p, ok := g.profileByID(profileID)
	if !ok {
		return
	}
	err := g.core.Connect(p.ID)
	if err != nil {
		g.showError("Connect profile", err)
	}
}

func (g *GUI) disconnectProfileFromTray(profileID string) {
	_, ok := g.profileByID(profileID)
	if !ok {
		return
	}
	g.core.DisconnectFromTray(profileID)
	g.refresh()
}

func (g *GUI) profileFromForm(existing profile.Profile) (profile.Profile, error) {
	port, err := strconv.Atoi(strings.TrimSpace(g.portEntry.Text))
	if err != nil {
		return profile.Profile{}, profile.ErrSocksPortNotNumber
	}
	existing.Kind = g.selectedBackendKind()
	existing.Name = strings.TrimSpace(g.nameEntry.Text)
	existing.SocksHost = strings.TrimSpace(g.hostEntry.Text)
	existing.SocksPort = port
	existing.AutoStart = g.autoStartCheck.Checked
	if existing.IsTailscale() {
		exitNode := g.selectedExitNodeValue()
		if g.tsAutoExit.Checked {
			exitNode = ""
		}
		authenticated := existing.TailscaleConfig.Authenticated
		authKey := ""
		if !authenticated {
			authKey = strings.TrimSpace(g.tsAuthKey.Text)
		}
		existing.WireGuardConfig = ""
		existing.TailscaleConfig = profile.TailscaleConfig{
			Hostname:               strings.TrimSpace(g.tsHostname.Text),
			AuthKey:                authKey,
			Authenticated:          authenticated,
			ControlURL:             strings.TrimSpace(g.tsControlURL.Text),
			ExitNode:               exitNode,
			AutoExitNode:           g.tsAutoExit.Checked,
			ExitNodeAllowLANAccess: g.tsAllowLAN.Checked,
			Ephemeral:              g.tsEphemeral.Checked,
		}
	} else {
		existing.Kind = profile.BackendWireGuard
		existing.WireGuardConfig = strings.TrimSpace(g.configEntry.Text)
		existing.TailscaleConfig = profile.TailscaleConfig{}
	}
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
	return existing, existing.Validate()
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
		g.kindSelect.SetSelected(backendKindLabel(profile.BackendWireGuard))
		g.nameEntry.SetText("")
		g.hostEntry.SetText("")
		g.portEntry.SetText("")
		g.autoStartCheck.SetChecked(false)
		g.configEntry.SetText("")
		g.setTailscaleForm(profile.TailscaleConfig{})
		g.setLogText("", true)
		g.statusLabel.SetText(tr("No profile selected"))
		g.updateBackendVisibility(profile.BackendWireGuard)
		return
	}

	p, ok := g.currentProfile()
	if !ok {
		return
	}
	g.setFormEnabled(true)
	g.kindSelect.SetSelected(backendKindLabel(p.Kind))
	g.nameEntry.SetText(p.Name)
	g.hostEntry.SetText(p.SocksHost)
	g.portEntry.SetText(strconv.Itoa(p.SocksPort))
	g.autoStartCheck.SetChecked(p.AutoStart)
	g.configEntry.SetText(p.WireGuardConfig)
	g.setTailscaleForm(p.TailscaleConfig)
	g.updateBackendVisibility(p.Kind)
	g.statusLabel.SetText(statusSummaryText(p, g.profileStatus(p.ID)))
	g.setLogText(profileLogText(g.core.Logs(p.ID)), false)
	g.refreshButtons()
	g.updateRuntimeFieldState()
}

func (g *GUI) setFormEnabled(enabled bool) {
	widgets := []fyne.Disableable{
		g.kindSelect,
		g.nameEntry,
		g.hostEntry,
		g.portEntry,
		g.autoStartCheck,
		g.configEntry,
		g.tsHostname,
		g.tsAuthKey,
		g.tsLoginButton,
		g.tsControlURL,
		g.tsExitNode,
		g.tsExitRefresh,
		g.tsAutoExit,
		g.tsAllowLAN,
		g.tsEphemeral,
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
	g.updateBackendVisibility(g.selectedBackendKind())
	g.updateRuntimeFieldState()
}

func (g *GUI) refresh() {
	if g.list != nil {
		g.list.Refresh()
	}
	g.refreshButtons()
	if g.selectedID != "" {
		if p, ok := g.currentProfile(); ok {
			g.statusLabel.SetText(statusSummaryText(p, g.profileStatus(p.ID)))
			g.setLogText(profileLogText(g.core.Logs(p.ID)), false)
		}
	}
	g.updateRuntimeFieldState()
	g.refreshTrayMenu()
}

func (g *GUI) refreshButtons() {
	idx := g.selectedIndex()
	if idx < 0 {
		return
	}
	p, ok := g.currentProfile()
	if !ok {
		return
	}
	locked := g.core.RuntimeLocked(p.ID)
	if locked {
		g.connectButton.Disable()
		g.disconnectButton.Enable()
		return
	}
	g.connectButton.Enable()
	g.disconnectButton.Disable()
}

func (g *GUI) updateRuntimeFieldState() {
	p, ok := g.currentProfile()
	if !ok || g.nameEntry == nil || g.nameEntry.Disabled() {
		return
	}
	locked := g.core.RuntimeLocked(p.ID)
	restartWidgets := []fyne.Disableable{
		g.kindSelect,
		g.hostEntry,
		g.portEntry,
		g.configEntry,
		g.tsHostname,
		g.tsControlURL,
		g.tsEphemeral,
	}
	for _, w := range restartWidgets {
		if w == nil {
			continue
		}
		if locked {
			w.Disable()
		} else {
			w.Enable()
		}
	}

	g.updateTailscaleAuthControlState(p.TailscaleConfig)
	authKeyLocked := locked || p.TailscaleConfig.Authenticated
	if g.tsAuthKey != nil {
		if authKeyLocked {
			g.tsAuthKey.Disable()
		} else {
			g.tsAuthKey.Enable()
		}
	}
	if g.tsLoginButton != nil {
		if locked {
			g.tsLoginButton.Disable()
		} else {
			g.tsLoginButton.Enable()
		}
	}

	status := g.core.Status(p.ID)
	exitNodeLocked := status == application.StatusStarting || status == application.StatusStopping
	for _, w := range []fyne.Disableable{g.tsAutoExit, g.tsAllowLAN} {
		if w == nil {
			continue
		}
		if exitNodeLocked {
			w.Disable()
		} else {
			w.Enable()
		}
	}
	if exitNodeLocked {
		g.setExitNodeControlsEnabled(false)
		return
	}
	g.updateExitNodeControlState()
}

func (g *GUI) currentProfile() (profile.Profile, bool) {
	return g.core.Profile(g.selectedID)
}

func (g *GUI) profileByID(profileID string) (profile.Profile, bool) {
	return g.core.Profile(profileID)
}

func (g *GUI) selectedIndex() int {
	for i, p := range g.core.Profiles() {
		if p.ID == g.selectedID {
			return i
		}
	}
	return -1
}

func (g *GUI) selectByID(id string) {
	for i, p := range g.core.Profiles() {
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

func profileLogText(entries []application.LogEntry) string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Message) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s", entry.At.Format("15:04:05"), localizedLogMessage(entry.Message)))
	}
	return strings.Join(lines, "\n")
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
