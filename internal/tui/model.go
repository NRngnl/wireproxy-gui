package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/charmbracelet/lipgloss"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/svcinstall"
	"github.com/NRngnl/wireproxy-gui/internal/tui/screens"
)

// mode identifies which UI surface currently owns keyboard input. Exactly
// one mode is active at a time; when a mode other than modeList is active,
// navigation/action keys are routed to that mode's own handler instead of
// the profile list.
type mode int

const (
	modeList mode = iota
	modeForm
	modeConfirm
	modeExitNode
	modeImportExport
	modeHelp
	modeService
)

// importExportKind distinguishes the two uses of the shared path-prompt
// modal (modeImportExport).
type importExportKind int

const (
	importExportImport importExportKind = iota
	importExportExport
)

// model is the root Bubble Tea model for the terminal UI. It holds the
// application core, the currently selected profile, and layout state.
type model struct {
	core    Application
	loadErr error

	program *tea.Program

	list list

	// logView renders the currently selected profile's log tail.
	logView viewport.Model

	// logTails tracks, per profile ID, whether the log viewport should
	// auto-follow new log lines (true) or stay put because the user has
	// manually scrolled up (false). This mirrors internal/ui/app.go's
	// logTails/logOffsets follow/pause-on-scroll behavior.
	logTails map[string]bool

	// lastActionErr holds the error from the most recent connect/disconnect
	// action, rendered as a transient banner until superseded.
	lastActionErr error

	// toast holds a transient informational message (e.g. "profile is
	// starting, please wait") rendered alongside lastActionErr.
	toast string

	// mode records which UI surface currently owns keyboard input. Only
	// one of the fields below is meaningful at a time, matching mode.
	mode mode

	form              *screens.EditForm
	confirm           *screens.Confirm
	confirmKind       confirmKind
	exitNode          *screens.ExitNodePicker
	exitNodeProfileID string
	pathPrompt        *screens.PathPrompt
	importExport      importExportKind

	// svc is the per-user login-service installer (internal/svcinstall).
	// It is a leaf dependency the tui package imports directly (see
	// internal/architecture's allow-list), independent of the Application
	// port; a nil svc (e.g. in tests that don't set one) is treated the
	// same as an unsupported platform.
	svc          svcinstall.Installer
	serviceModel *screens.ServicePanel
	// serviceGen is bumped every time the service panel is opened, and
	// stamped onto the async Status/Install/Uninstall commands dispatched
	// for that panel-open. An arriving StatusResultMsg/ActionResultMsg
	// whose Gen doesn't match the current serviceGen is discarded rather
	// than applied, so closing and reopening the panel before a prior
	// async result arrives can never misapply a stale result to the new
	// panel instance.
	serviceGen int

	width  int
	height int

	quitting bool
}

// confirmKind distinguishes what an active modeConfirm confirmation applies
// to, since Confirm itself is a generic yes/no prompt with no notion of the
// action it guards.
type confirmKind int

const (
	confirmDelete confirmKind = iota
)

// list is a minimal wrapper around the profile sidebar state so model.go
// stays free of bubbles/list implementation details beyond selection.
// internal/tui/screens holds the richer bubbles/list-based sidebar for
// future integration by the composition root; model.go keeps its own
// lightweight projection here because internal/tui may not import
// internal/tui/screens (see internal/architecture).
type list struct {
	items      []profileItem
	selected   int
	selectedID string
}

// profileItem is model.go's own minimal profile projection, kept separate
// from screens.ProfileItem for the architecture reason noted above.
type profileItem struct {
	profile profile.Profile
	status  application.Status
}

func (i profileItem) id() string { return i.profile.ID }

// statusGlyph returns the status glyph and rendering style for a profile's
// application.Status, matching the color vocabulary used by the tray icons
// in internal/ui/app.go: green connected/running, gray
// disconnected/stopped, amber connecting/starting/disconnecting/stopping,
// red error.
func statusGlyph(status application.Status) (string, lipgloss.Style) {
	switch status {
	case application.StatusRunning:
		return "\u25cf", statusConnectedStyle
	case application.StatusStopped:
		return "\u25cb", statusDisconnectedStyle
	case application.StatusStarting, application.StatusStopping:
		return "\u25d0", statusConnectingStyle
	case application.StatusError:
		return "!", statusErrorStyle
	default:
		return "\u25cb", statusDisconnectedStyle
	}
}

func (i profileItem) title() string {
	glyph, style := statusGlyph(i.status)
	return style.Render(glyph) + " " + i.profile.Name
}

func (i profileItem) description() string {
	return i.profile.BindAddress()
}

func newModel(core Application, loadErr error) *model {
	m := &model{
		core:     core,
		loadErr:  loadErr,
		logView:  viewport.New(80, 20),
		logTails: map[string]bool{},
		svc:      svcinstall.New(),
	}
	m.refreshList()
	m.refreshLogView(true)
	return m
}

// formatLogEntries renders LogEntry values as the timestamped lines shown in
// the log viewport, matching internal/ui/app.go's profileLogText format.
func formatLogEntries(entries []application.LogEntry) string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Message) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s", entry.At.Format("15:04:05"), entry.Message))
	}
	return strings.Join(lines, "\n")
}

// selectedLogTail reports whether the log viewport should auto-follow new
// lines for the currently selected profile, defaulting to true (matches
// internal/ui/app.go's selectedLogTail).
func (m *model) selectedLogTail() bool {
	if m.list.selectedID == "" {
		return true
	}
	tail, ok := m.logTails[m.list.selectedID]
	return !ok || tail
}

func (m *model) setSelectedLogTail(tail bool) {
	if m.list.selectedID == "" {
		return
	}
	if m.logTails == nil {
		m.logTails = map[string]bool{}
	}
	m.logTails[m.list.selectedID] = tail
}

// refreshLogView repopulates the log viewport content from the currently
// selected profile's logs. When forceTail is true (e.g. on selection
// change) the viewport always jumps to the bottom and resumes following;
// otherwise it preserves the follow/pause state recorded for the selected
// profile, mirroring internal/ui/app.go's setLogText behavior.
func (m *model) refreshLogView(forceTail bool) {
	if m.list.selectedID == "" {
		m.logView.SetContent("")
		return
	}
	entries := m.core.Logs(m.list.selectedID)
	m.logView.SetContent(formatLogEntries(entries))
	if forceTail {
		m.setSelectedLogTail(true)
		m.logView.GotoBottom()
		return
	}
	if m.selectedLogTail() {
		m.logView.GotoBottom()
	}
}

// handleLogChange updates the log viewport in response to an incoming
// changeMsg for the currently selected profile: it appends the latest log
// lines and auto-scrolls to bottom only if the viewport was already at the
// bottom (i.e. the user has not manually scrolled up), mirroring
// internal/ui/app.go's follow/pause-on-scroll behavior.
func (m *model) handleLogChange() {
	wasAtBottom := m.logView.AtBottom()
	entries := m.core.Logs(m.list.selectedID)
	m.logView.SetContent(formatLogEntries(entries))
	if wasAtBottom {
		m.setSelectedLogTail(true)
		m.logView.GotoBottom()
	} else {
		m.setSelectedLogTail(false)
	}
}

// refreshList rebuilds the in-memory profile list from the application
// core, preserving the selected profile ID when possible.
func (m *model) refreshList() {
	profiles := m.core.Profiles()
	items := make([]profileItem, 0, len(profiles))
	for _, p := range profiles {
		items = append(items, profileItem{profile: p, status: m.core.Status(p.ID)})
	}
	m.list.items = items

	if len(items) == 0 {
		m.list.selected = 0
		m.list.selectedID = ""
		return
	}

	for i, item := range items {
		if item.id() == m.list.selectedID {
			m.list.selected = i
			return
		}
	}

	if m.list.selected >= len(items) {
		m.list.selected = len(items) - 1
	}
	if m.list.selected < 0 {
		m.list.selected = 0
	}
	m.list.selectedID = items[m.list.selected].id()
}

// Init implements tea.Model.
func (m *model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.applyLayout()
		return m, nil

	case changeMsg:
		m.refreshList()
		if msg.change.ProfileID != "" && msg.change.ProfileID == m.list.selectedID {
			m.handleLogChange()
		}
		return m, nil

	case screens.ExitNodesLoadedMsg:
		if m.mode == modeExitNode && m.exitNode != nil && msg.ProfileID == m.exitNodeProfileID {
			m.exitNode.SetExitNodes(msg.Nodes)
		}
		return m, nil

	case screens.ExitNodesErrMsg:
		if m.mode == modeExitNode && msg.ProfileID == m.exitNodeProfileID {
			m.closeModal()
			m.lastActionErr = msg.Err
		}
		return m, nil

	case tea.KeyMsg:
		if m.mode != modeList {
			return m.updateActiveMode(msg)
		}

		switch {
		case key.Matches(msg, keys.Quit):
			m.quitting = true
			return m, tea.Quit
		case key.Matches(msg, keys.Up):
			m.moveSelection(-1)
			return m, nil
		case key.Matches(msg, keys.Down):
			m.moveSelection(1)
			return m, nil
		case key.Matches(msg, keys.GoTop):
			m.clearTransientMessages()
			m.logView.GotoTop()
			m.setSelectedLogTail(false)
			return m, nil
		case key.Matches(msg, keys.GoBottom):
			m.clearTransientMessages()
			m.logView.GotoBottom()
			m.setSelectedLogTail(true)
			return m, nil
		case key.Matches(msg, keys.Connect):
			m.doConnect()
			return m, nil
		case key.Matches(msg, keys.ConnectAll):
			m.doConnectAll()
			return m, nil
		case key.Matches(msg, keys.Disconnect):
			m.doDisconnect()
			return m, nil
		case key.Matches(msg, keys.DisconnectAll):
			m.clearTransientMessages()
			m.core.DisconnectAll()
			return m, nil
		case key.Matches(msg, keys.Add):
			return m.openAddForm()
		case key.Matches(msg, keys.Edit):
			return m.openEditForm()
		case key.Matches(msg, keys.Delete):
			return m.openDeleteConfirm()
		case key.Matches(msg, keys.New):
			return m.openExitNodePicker()
		case key.Matches(msg, keys.Login):
			m.doLogin()
			return m, nil
		case key.Matches(msg, keys.Logout):
			return m.doLogout()
		case key.Matches(msg, keys.Import):
			return m.openImportPrompt()
		case key.Matches(msg, keys.Export):
			return m.openExportPrompt()
		case key.Matches(msg, keys.Help):
			m.clearTransientMessages()
			m.mode = modeHelp
			return m, nil
		case key.Matches(msg, keys.Service):
			return m.openServicePanel()
		}

	case screens.StatusResultMsg:
		if m.mode == modeService && m.serviceModel != nil && msg.Gen == m.serviceGen {
			m.serviceModel.Loading = false
			m.serviceModel.Status = msg.Status
			m.serviceModel.Err = msg.Err
			m.serviceModel.LockErr = false
		}
		return m, nil

	case screens.ActionResultMsg:
		if m.mode == modeService && m.serviceModel != nil && msg.Gen == m.serviceGen {
			m.serviceModel.Busy = false
			if msg.Err != nil {
				if errors.Is(msg.Err, svcinstall.ErrLockHeld) {
					m.serviceModel.Err = msg.Err
					m.serviceModel.LockErr = true
					return m, nil
				}
				m.serviceModel.Err = msg.Err
				m.serviceModel.LockErr = false
				return m, nil
			}
			// Success: close the panel and surface a toast, matching the
			// existing delete/exit-node/import-export success pattern.
			if msg.Installed {
				m.toast = "Start on login enabled"
			} else {
				m.toast = "Start on login disabled"
			}
			m.closeModal()
		}
		return m, nil
	}

	return m, nil
}

// updateActiveMode routes a key message to whichever modal/form surface is
// currently active (m.mode != modeList), instead of the profile list's own
// navigation/action keys.
func (m *model) updateActiveMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeForm:
		return m.updateForm(msg)
	case modeConfirm:
		return m.updateConfirm(msg)
	case modeExitNode:
		return m.updateExitNode(msg)
	case modeImportExport:
		return m.updateImportExport(msg)
	case modeHelp:
		return m.updateHelp(msg)
	case modeService:
		return m.updateService(msg)
	}
	return m, nil
}

// closeModal resets the mode back to modeList and clears all modal state,
// returning to the plain profile list view.
func (m *model) closeModal() {
	m.mode = modeList
	m.form = nil
	m.confirm = nil
	m.exitNode = nil
	m.exitNodeProfileID = ""
	m.pathPrompt = nil
	m.serviceModel = nil
}

// openServicePanel opens the per-user login-service status/install panel
// (internal/svcinstall), following the same mode-based state-machine
// pattern as the other modal surfaces. Fetching Status is dispatched as a
// tea.Cmd so the subprocess call it makes never blocks the Bubble Tea
// event loop.
func (m *model) openServicePanel() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	supported := m.svc != nil && m.svc.Supported()
	m.serviceModel = screens.NewServicePanel(supported)
	m.mode = modeService
	m.serviceGen++
	if !supported {
		return m, nil
	}
	return m, screens.LoadStatusCmd(m.svc, context.Background(), m.serviceGen)
}

// updateService forwards a key message to the active service panel,
// dispatching Install/Uninstall as tea.Cmds when the user confirms an
// action, per ServicePanel.HandleKey's contract.
func (m *model) updateService(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	install, uninstall, forceInstall, closePanel := m.serviceModel.HandleKey(msg)

	if closePanel {
		m.closeModal()
		return m, nil
	}

	if install || forceInstall {
		m.serviceModel.Busy = true
		opts := svcinstall.Options{Force: forceInstall}
		return m, screens.InstallCmd(m.svc, context.Background(), opts, m.serviceGen)
	}
	if uninstall {
		m.serviceModel.Busy = true
		return m, screens.UninstallCmd(m.svc, context.Background(), m.serviceGen)
	}
	return m, nil
}

// openAddForm opens the add-profile form for a brand-new profile.Profile
// obtained via core.Add.
func (m *model) openAddForm() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	newProfile, err := m.core.Add("New profile")
	if err != nil {
		m.lastActionErr = err
		return m, nil
	}
	m.refreshList()
	m.list.selectedID = newProfile.ID
	for i, item := range m.list.items {
		if item.id() == newProfile.ID {
			m.list.selected = i
			break
		}
	}
	m.form = screens.NewEditForm(newProfile, true)
	m.mode = modeForm
	return m, m.form.Form().Init()
}

// openEditForm opens the edit form for the currently selected profile.
func (m *model) openEditForm() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return m, nil
	}
	existing, ok := m.core.Profile(m.list.selectedID)
	if !ok {
		m.toast = "profile no longer exists"
		return m, nil
	}
	m.form = screens.NewEditForm(existing, false)
	m.mode = modeForm
	return m, m.form.Form().Init()
}

// updateForm forwards a key message to the active add/edit form and
// handles submission/cancellation once the underlying huh.Form settles
// into a terminal state.
func (m *model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Escape) {
		m.closeModal()
		return m, nil
	}

	formModel, cmd := m.form.Form().Update(msg)
	form, ok := formModel.(*huh.Form)
	if ok {
		*m.form.Form() = *form
	}

	switch m.form.Form().State {
	case huh.StateCompleted:
		result := m.form.Result()
		_, err := m.core.Save(result)
		if err != nil {
			m.lastActionErr = err
			m.closeModal()
			return m, nil
		}
		m.closeModal()
		m.refreshList()
		m.list.selectedID = result.ID
		for i, item := range m.list.items {
			if item.id() == result.ID {
				m.list.selected = i
				break
			}
		}
		m.toast = "Saved"
		return m, nil
	case huh.StateAborted:
		m.closeModal()
		return m, nil
	}

	return m, cmd
}

// openDeleteConfirm opens a yes/no confirmation for deleting the currently
// selected profile.
func (m *model) openDeleteConfirm() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return m, nil
	}
	item, ok := m.core.Profile(m.list.selectedID)
	if !ok {
		m.toast = "profile no longer exists"
		return m, nil
	}
	m.confirm = screens.NewConfirm(fmt.Sprintf("Delete profile %q?", item.Name))
	m.confirmKind = confirmDelete
	m.mode = modeConfirm
	return m, nil
}

// updateConfirm forwards a key message to the active confirmation prompt
// and, once resolved, performs (or cancels) the guarded action.
func (m *model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	resolved, confirmed := m.confirm.HandleKey(msg)
	if !resolved {
		return m, nil
	}

	kind := m.confirmKind
	selectedID := m.list.selectedID
	m.closeModal()

	if !confirmed {
		return m, nil
	}

	switch kind {
	case confirmDelete:
		if err := m.core.Delete(selectedID); err != nil {
			m.lastActionErr = err
			return m, nil
		}
		if m.list.selectedID == selectedID {
			m.list.selectedID = ""
		}
		m.refreshList()
		m.refreshLogView(true)
		m.toast = "Deleted"
	}

	return m, nil
}

// openExitNodePicker opens the Tailscale exit-node picker for the currently
// selected profile. It is a no-op (with a toast) for WireGuard profiles,
// since exit-node selection is Tailscale-only.
func (m *model) openExitNodePicker() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return m, nil
	}
	item, ok := m.core.Profile(m.list.selectedID)
	if !ok {
		m.toast = "profile no longer exists"
		return m, nil
	}
	if !item.IsTailscale() {
		m.toast = "exit-node selection is Tailscale-only"
		return m, nil
	}

	m.exitNode = screens.NewExitNodePicker(m.width, m.height)
	m.exitNode.SetLoading(true)
	m.exitNodeProfileID = item.ID
	m.mode = modeExitNode
	return m, screens.LoadExitNodesCmd(m.core, context.Background(), item.ID)
}

// updateExitNode forwards a key message to the active exit-node picker,
// handling list navigation and selection.
func (m *model) updateExitNode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Escape) {
		m.closeModal()
		return m, nil
	}

	if m.exitNode.Loading {
		return m, nil
	}

	if msg.Type == tea.KeyEnter {
		selected, ok := m.exitNode.Selected()
		if !ok {
			m.closeModal()
			return m, nil
		}
		profileID := m.exitNodeProfileID
		existing, ok := m.core.Profile(profileID)
		m.closeModal()
		if !ok {
			m.toast = "profile no longer exists"
			return m, nil
		}
		existing.TailscaleConfig.ExitNode = selected.ID
		if _, err := m.core.Save(existing); err != nil {
			m.lastActionErr = err
			return m, nil
		}
		m.refreshList()
		m.toast = "Exit node updated"
		return m, nil
	}

	var cmd tea.Cmd
	m.exitNode.List, cmd = m.exitNode.List.Update(msg)
	return m, cmd
}

// doLogin handles the Login keybinding for the currently selected profile,
// calling core.Login directly with no intervening form.
func (m *model) doLogin() {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return
	}
	if err := m.core.Login(m.list.selectedID); err != nil {
		m.lastActionErr = err
		return
	}
	m.refreshList()
	m.toast = "Login started"
}

// doLogout handles the Logout keybinding for the currently selected
// profile, calling core.Logout directly with no intervening form.
func (m *model) doLogout() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return m, nil
	}
	if err := m.core.Logout(context.Background(), m.list.selectedID); err != nil {
		m.lastActionErr = err
		return m, nil
	}
	m.refreshList()
	m.toast = "Logged out"
	return m, nil
}

// openImportPrompt opens the path-prompt modal for importing profiles from
// a file.
func (m *model) openImportPrompt() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	m.pathPrompt = screens.NewPathPrompt("Import from file:", "")
	m.importExport = importExportImport
	m.mode = modeImportExport
	return m, nil
}

// openExportPrompt opens the path-prompt modal for exporting all current
// profiles to a file. Exporting all profiles is the v1 default scope; a
// dedicated "export selected" affordance is left for a future iteration.
func (m *model) openExportPrompt() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	m.pathPrompt = screens.NewPathPrompt("Export to file:", "wireproxy-profiles.json")
	m.importExport = importExportExport
	m.mode = modeImportExport
	return m, nil
}

// updateImportExport forwards a key message to the active path-prompt
// modal and, on Enter, performs the import or export.
func (m *model) updateImportExport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Escape) {
		m.closeModal()
		return m, nil
	}

	if msg.Type == tea.KeyEnter {
		path := strings.TrimSpace(m.pathPrompt.Submit())
		kind := m.importExport
		m.closeModal()
		if path == "" {
			return m, nil
		}

		switch kind {
		case importExportImport:
			imported, err := screens.ImportFromPath(m.core, path)
			if err != nil {
				m.lastActionErr = err
				return m, nil
			}
			m.refreshList()
			if len(imported) > 0 {
				m.list.selectedID = imported[0].ID
				for i, item := range m.list.items {
					if item.id() == imported[0].ID {
						m.list.selected = i
						break
					}
				}
			}
			m.toast = fmt.Sprintf("Imported %d profile(s) from %s", len(imported), filepath.Base(path))
		case importExportExport:
			profiles := m.core.Profiles()
			ids := make([]string, 0, len(profiles))
			for _, p := range profiles {
				ids = append(ids, p.ID)
			}
			if err := screens.ExportToPath(m.core, path, ids); err != nil {
				m.lastActionErr = err
				return m, nil
			}
			m.toast = "Exported"
		}
		return m, nil
	}

	cmd := m.pathPrompt.Update(msg)
	return m, cmd
}

// updateHelp handles the key that closes the full-help overlay: '?' toggles
// it off again, and esc also closes it. Any other key is ignored while the
// overlay is open.
func (m *model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, keys.Help) || key.Matches(msg, keys.Escape) {
		m.mode = modeList
		return m, nil
	}
	return m, nil
}

// clearTransientMessages clears any error banner or toast message. It is
// called whenever a new action begins, so a stale message from a prior
// action does not linger on screen.
func (m *model) clearTransientMessages() {
	m.lastActionErr = nil
	m.toast = ""
}

// doConnect handles the Connect keybinding for the currently selected
// profile, respecting RuntimeLocked to avoid firing a redundant call into a
// profile that is already starting or stopping.
func (m *model) doConnect() {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return
	}
	if m.core.RuntimeLocked(m.list.selectedID) {
		m.toast = "profile is starting/stopping, please wait"
		return
	}
	if err := m.core.Connect(m.list.selectedID); err != nil {
		m.lastActionErr = err
		return
	}
	m.refreshList()
}

// doConnectAll handles the ConnectAll keybinding.
func (m *model) doConnectAll() {
	m.clearTransientMessages()
	if err := m.core.ConnectAll(); err != nil {
		m.lastActionErr = err
		return
	}
	m.refreshList()
}

// doDisconnect handles the Disconnect keybinding for the currently selected
// profile, respecting RuntimeLocked to avoid firing a redundant call into a
// profile that is already starting or stopping.
func (m *model) doDisconnect() {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return
	}
	if m.core.RuntimeLocked(m.list.selectedID) {
		m.toast = "profile is starting/stopping, please wait"
		return
	}
	m.core.Disconnect(m.list.selectedID)
	m.refreshList()
}

func (m *model) moveSelection(delta int) {
	if len(m.list.items) == 0 {
		return
	}
	next := m.list.selected + delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.list.items) {
		next = len(m.list.items) - 1
	}
	if next == m.list.selected {
		return
	}
	m.list.selected = next
	m.list.selectedID = m.list.items[next].id()
	m.refreshLogView(true)
}

// applyLayout resizes the log viewport to fit the current terminal
// dimensions. A fixed side-by-side layout is used per Task 4's scope; Task
// 9 covers resize polish.
func (m *model) applyLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	const logWidthFraction = 0.4
	logWidth := int(float64(m.width) * logWidthFraction)
	if logWidth < 20 {
		logWidth = 20
	}
	logHeight := m.height - 8
	if logHeight < 3 {
		logHeight = 3
	}
	m.logView.Width = logWidth
	m.logView.Height = logHeight
}

// View implements tea.Model.
func (m *model) View() string {
	if m.quitting {
		return ""
	}

	var banner string
	if m.loadErr != nil {
		banner += errorBannerStyle.Render(fmt.Sprintf("Failed to load profiles: %v", m.loadErr)) + "\n\n"
	}
	if m.lastActionErr != nil {
		banner += errorBannerStyle.Render(fmt.Sprintf("Action failed: %v", m.lastActionErr)) + "\n\n"
	}
	if m.toast != "" {
		banner += toastStyle.Render(m.toast) + "\n\n"
	}

	if m.mode == modeHelp {
		return banner + titleStyle.Render("Wireproxy GUI — Help") + "\n\n" + screens.FullHelpView(keys, m.helpWidth())
	}

	if overlay, ok := m.modalView(); ok {
		return banner + overlay
	}

	if len(m.list.items) == 0 {
		return banner + emptyStateStyle.Render(
			"No profiles yet. Press 'a' to add a WireGuard or Tailscale profile.",
		)
	}

	var body string
	for i, item := range m.list.items {
		line := item.title() + "  " + item.description()
		if i == m.list.selected {
			line = selectedItemStyle.Render("> " + item.title() + "  " + item.description())
		} else {
			line = "  " + line
		}
		body += line + "\n"
	}

	listPane := sidebarStyle.Render(body)
	detailPane := detailStyle.Render(m.renderDetail())
	logPane := logViewportStyle.Render(m.logView.View())

	rightColumn := lipgloss.JoinVertical(lipgloss.Left, detailPane, logPane)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, listPane, rightColumn)

	return banner + titleStyle.Render("Wireproxy GUI — Profiles") + "\n\n" + panes
}

// helpWidth returns a reasonable width to wrap the full-help overlay to.
func (m *model) helpWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

// modalView renders the currently active modal/form (add/edit form, delete
// confirmation, exit-node picker, or import/export path prompt), if any,
// in place of the profile list view. It returns ok=false when modeList is
// active and the plain list view should be rendered instead.
func (m *model) modalView() (string, bool) {
	switch m.mode {
	case modeForm:
		title := "Edit profile"
		if m.form.IsNew() {
			title = "Add profile"
		}
		return titleStyle.Render("Wireproxy GUI — "+title) + "\n\n" + m.form.Form().View(), true
	case modeConfirm:
		return titleStyle.Render("Wireproxy GUI — Confirm") + "\n\n" + m.confirm.View(), true
	case modeExitNode:
		return titleStyle.Render("Wireproxy GUI — Exit node") + "\n\n" + m.exitNode.View(), true
	case modeImportExport:
		return titleStyle.Render("Wireproxy GUI — Path") + "\n\n" + m.pathPrompt.View(), true
	case modeService:
		return titleStyle.Render("Wireproxy GUI — Start on Login") + "\n\n" + m.serviceModel.View(), true
	}
	return "", false
}

// renderDetail renders the right-hand detail pane content for the currently
// selected profile: name, backend kind, bind address, and status.
func (m *model) renderDetail() string {
	if m.list.selected < 0 || m.list.selected >= len(m.list.items) {
		return "No profile selected"
	}
	item := m.list.items[m.list.selected]

	kind := "WireGuard"
	if item.profile.IsTailscale() {
		kind = "Tailscale"
	}

	glyph, style := statusGlyph(item.status)
	statusLine := style.Render(glyph) + " " + string(item.status)

	lines := []string{
		detailLabelStyle.Render("Name:") + " " + item.profile.Name,
		detailLabelStyle.Render("Kind:") + " " + kind,
		detailLabelStyle.Render("Bind Address:") + " " + item.profile.BindAddress(),
		detailLabelStyle.Render("Status:") + " " + statusLine,
	}
	return strings.Join(lines, "\n")
}
