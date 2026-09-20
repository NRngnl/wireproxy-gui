package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/svcinstall"
)

// fakeInstaller is a minimal test double for svcinstall.Installer.
type fakeInstaller struct {
	supported bool
	status    svcinstall.Status
	statusErr error

	installErr   error
	installCalls []svcinstall.Options

	uninstallErr   error
	uninstallCalls int
}

func (f *fakeInstaller) Supported() bool { return f.supported }

func (f *fakeInstaller) Status(context.Context) (svcinstall.Status, error) {
	return f.status, f.statusErr
}

func (f *fakeInstaller) Install(_ context.Context, opts svcinstall.Options) error {
	f.installCalls = append(f.installCalls, opts)
	return f.installErr
}

func (f *fakeInstaller) Uninstall(context.Context) error {
	f.uninstallCalls++
	return f.uninstallErr
}

// fakeApplication is a minimal test double for tui.Application. It never
// wires a real internal/application.Service so model tests stay isolated
// from runtime and persistence behavior.
type fakeApplication struct {
	changes  chan application.Change
	profiles []profile.Profile
	statuses map[string]application.Status
	logs     map[string][]application.LogEntry
	locked   map[string]bool

	connectCalls       []string
	disconnectCalls    []string
	connectAllCalls    int
	disconnectAllCalls int
	connectErr         error

	saveCalls   []profile.Profile
	saveErr     error
	deleteCalls []string
	deleteErr   error
	loginCalls  []string
	loginErr    error
}

func newFakeApplication(profiles ...profile.Profile) *fakeApplication {
	statuses := make(map[string]application.Status, len(profiles))
	for _, item := range profiles {
		statuses[item.ID] = application.StatusStopped
	}
	return &fakeApplication{
		changes:  make(chan application.Change, 8),
		profiles: append([]profile.Profile(nil), profiles...),
		statuses: statuses,
		logs:     map[string][]application.LogEntry{},
		locked:   map[string]bool{},
	}
}

func (f *fakeApplication) Changes() <-chan application.Change { return f.changes }

func (f *fakeApplication) Profiles() []profile.Profile {
	return append([]profile.Profile(nil), f.profiles...)
}

func (f *fakeApplication) Profile(id string) (profile.Profile, bool) {
	for _, item := range f.profiles {
		if item.ID == id {
			return item, true
		}
	}
	return profile.Profile{}, false
}

func (f *fakeApplication) Status(id string) application.Status {
	if status, ok := f.statuses[id]; ok {
		return status
	}
	return application.StatusStopped
}

func (f *fakeApplication) Logs(id string) []application.LogEntry {
	return append([]application.LogEntry(nil), f.logs[id]...)
}
func (f *fakeApplication) RuntimeLocked(id string) bool { return f.locked[id] }

func (f *fakeApplication) Add(name string) (profile.Profile, error) {
	item := profile.New(name, "", profile.NextAvailablePort(f.profiles))
	f.profiles = append(f.profiles, item)
	f.statuses[item.ID] = application.StatusStopped
	return item, nil
}

func (f *fakeApplication) Save(item profile.Profile) (application.SaveResult, error) {
	f.saveCalls = append(f.saveCalls, item)
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
	f.deleteCalls = append(f.deleteCalls, id)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for index := range f.profiles {
		if f.profiles[index].ID == id {
			f.profiles = append(f.profiles[:index], f.profiles[index+1:]...)
			return nil
		}
	}
	return application.ErrProfileNotFound
}

func (f *fakeApplication) Import(string, []byte) ([]profile.Profile, error) { return nil, nil }
func (f *fakeApplication) Export([]string) ([]byte, error)                  { return nil, nil }

func (f *fakeApplication) Connect(id string) error {
	f.connectCalls = append(f.connectCalls, id)
	if f.connectErr != nil {
		return f.connectErr
	}
	return nil
}

func (f *fakeApplication) Login(id string) error {
	f.loginCalls = append(f.loginCalls, id)
	return f.loginErr
}

func (f *fakeApplication) ConnectAll() error {
	f.connectAllCalls++
	return nil
}

func (f *fakeApplication) AutoConnect() error { return nil }

func (f *fakeApplication) Disconnect(id string) bool {
	f.disconnectCalls = append(f.disconnectCalls, id)
	return true
}

func (f *fakeApplication) DisconnectFromTray(string) bool { return true }

func (f *fakeApplication) DisconnectAll() {
	f.disconnectAllCalls++
}

func (f *fakeApplication) ExitNodes(context.Context, string) ([]connection.ExitNode, error) {
	return nil, nil
}
func (f *fakeApplication) Logout(context.Context, string) error { return nil }
func (f *fakeApplication) Shutdown(context.Context) error       { return nil }

var _ Application = (*fakeApplication)(nil)

func newTestProfile(name string, port int) profile.Profile {
	return profile.New(name, "", port)
}

func TestUpdateMovesSelectionOnArrowKeys(t *testing.T) {
	app := newFakeApplication(
		newTestProfile("alpha", 1080),
		newTestProfile("beta", 1081),
		newTestProfile("gamma", 1082),
	)
	m := newModel(app, nil)

	if m.list.selected != 0 {
		t.Fatalf("initial selection = %d, want 0", m.list.selected)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*model)
	if m.list.selected != 1 {
		t.Fatalf("after down, selection = %d, want 1", m.list.selected)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	m = updated.(*model)
	if m.list.selected != 2 {
		t.Fatalf("after j, selection = %d, want 2", m.list.selected)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*model)
	if m.list.selected != 1 {
		t.Fatalf("after up, selection = %d, want 1", m.list.selected)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	m = updated.(*model)
	if m.list.selected != 0 {
		t.Fatalf("after k, selection = %d, want 0", m.list.selected)
	}
}

func TestUpdateHandlesChangeMessageWithoutPanic(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)

	// Simulate a status change happening in the core before the change
	// notification is delivered.
	item := app.profiles[0]
	app.statuses[item.ID] = application.StatusRunning

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Update panicked on changeMsg: %v", r)
		}
	}()

	updated, cmd := m.Update(changeMsg{change: application.Change{ProfileID: item.ID}})
	m = updated.(*model)
	if cmd != nil {
		t.Fatalf("expected nil cmd for changeMsg, got %v", cmd)
	}
	if m.list.items[0].status != application.StatusRunning {
		t.Fatalf("status not refreshed after change message")
	}
	_ = m.View()
}

func TestViewRendersEmptyStateWhenNoProfiles(t *testing.T) {
	app := newFakeApplication()
	m := newModel(app, nil)

	view := m.View()
	if view == "" {
		t.Fatalf("expected non-empty empty-state view")
	}
	if !contains(view, "No profiles yet") {
		t.Fatalf("expected empty-state message, got: %q", view)
	}
}

func TestViewRendersLoadErrorBanner(t *testing.T) {
	app := newFakeApplication()
	m := newModel(app, errors.New("boom"))

	view := m.View()
	if !contains(view, "Failed to load profiles") {
		t.Fatalf("expected load error banner, got: %q", view)
	}
	if !contains(view, "boom") {
		t.Fatalf("expected load error text in banner, got: %q", view)
	}
}

func TestUpdateQuitsOnQKey(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatalf("expected tea.Quit command on 'q', got nil")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from quit command, got %T", msg)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestSelectingProfileRepopulatesLogViewport(t *testing.T) {
	app := newFakeApplication(
		newTestProfile("alpha", 1080),
		newTestProfile("beta", 1081),
	)
	alphaID := app.profiles[0].ID
	betaID := app.profiles[1].ID
	app.logs[alphaID] = []application.LogEntry{{Message: "alpha-line"}}
	app.logs[betaID] = []application.LogEntry{{Message: "beta-line"}}

	m := newModel(app, nil)
	if !contains(m.logView.View(), "alpha-line") {
		t.Fatalf("expected initial log viewport to contain alpha-line, got: %q", m.logView.View())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*model)
	if m.list.selectedID != betaID {
		t.Fatalf("expected selection to move to beta, got %q", m.list.selectedID)
	}
	if !contains(m.logView.View(), "beta-line") {
		t.Fatalf("expected log viewport to repopulate with beta-line, got: %q", m.logView.View())
	}
	if contains(m.logView.View(), "alpha-line") {
		t.Fatalf("expected log viewport to no longer contain alpha-line, got: %q", m.logView.View())
	}
}

func TestChangeMsgAppendsLogAndAutoScrollsWhenAtBottom(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	id := app.profiles[0].ID
	app.logs[id] = []application.LogEntry{{Message: "line-1"}}

	m := newModel(app, nil)
	if !m.logView.AtBottom() {
		t.Fatalf("expected viewport to start at bottom")
	}

	// Simulate a new log line arriving in the core before the change
	// notification is delivered.
	app.logs[id] = append(app.logs[id], application.LogEntry{Message: "line-2"})

	updated, _ := m.Update(changeMsg{change: application.Change{ProfileID: id}})
	m = updated.(*model)

	if !contains(m.logView.View(), "line-2") {
		t.Fatalf("expected viewport to contain appended line-2, got: %q", m.logView.View())
	}
	if !m.logView.AtBottom() {
		t.Fatalf("expected viewport to auto-scroll to bottom after change when previously at bottom")
	}
}

func TestConnectKeyCallsConnectWithSelectedProfile(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	id := app.profiles[0].ID
	m := newModel(app, nil)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})

	if len(app.connectCalls) != 1 || app.connectCalls[0] != id {
		t.Fatalf("expected Connect(%q) to be called once, got calls: %v", id, app.connectCalls)
	}
}

func TestConnectKeyNoOpWhenRuntimeLocked(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	id := app.profiles[0].ID
	app.locked[id] = true
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*model)

	if len(app.connectCalls) != 0 {
		t.Fatalf("expected Connect not to be called while runtime locked, got calls: %v", app.connectCalls)
	}
	if m.toast == "" {
		t.Fatalf("expected a toast message to be set when runtime locked")
	}
	if !contains(m.View(), m.toast) {
		t.Fatalf("expected View() to render the toast message, got: %q", m.View())
	}
}

func TestConnectErrorPopulatesVisibleErrorState(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	app.connectErr = errors.New("boom")
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(*model)

	if m.lastActionErr == nil {
		t.Fatalf("expected lastActionErr to be set after Connect failure")
	}
	view := m.View()
	if !contains(view, "boom") {
		t.Fatalf("expected View() to render the connect error, got: %q", view)
	}
}

func TestAddKeyOpensFormAndSubmitCallsSave(t *testing.T) {
	app := newFakeApplication()
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*model)

	if m.mode != modeForm {
		t.Fatalf("expected mode=modeForm after 'a', got %v", m.mode)
	}
	if m.form == nil {
		t.Fatalf("expected a non-nil form after 'a'")
	}
	if !m.form.IsNew() {
		t.Fatalf("expected the 'a' form to be IsNew()==true")
	}

	// Simulate the huh.Form reaching a completed state, as it would once
	// the user fills in required fields and submits.
	m.form.Form().State = huh.StateCompleted

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*model)

	if m.mode != modeList {
		t.Fatalf("expected mode=modeList after form submission, got %v", m.mode)
	}
	if len(app.saveCalls) != 1 {
		t.Fatalf("expected Save to be called once, got %d calls", len(app.saveCalls))
	}
}

func TestAddFormSaveErrorSurfacesAsLastActionErr(t *testing.T) {
	app := newFakeApplication()
	app.saveErr = errors.New("save failed")
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*model)
	m.form.Form().State = huh.StateCompleted

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(*model)

	if m.lastActionErr == nil {
		t.Fatalf("expected lastActionErr to be set after Save failure")
	}
	if m.mode != modeList {
		t.Fatalf("expected the form to close after Save failure, mode=%v", m.mode)
	}
}

func TestEscapeCancelsOpenForm(t *testing.T) {
	app := newFakeApplication()
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(*model)
	if m.mode != modeForm {
		t.Fatalf("expected mode=modeForm after 'a'")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*model)

	if m.mode != modeList {
		t.Fatalf("expected esc to return to modeList, got %v", m.mode)
	}
	if len(app.saveCalls) != 0 {
		t.Fatalf("expected Save not to be called after cancelling with esc")
	}
}

func TestDeleteKeyThenYCallsDelete(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	id := app.profiles[0].ID
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(*model)

	if m.mode != modeConfirm {
		t.Fatalf("expected mode=modeConfirm after 'x', got %v", m.mode)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(*model)

	if m.mode != modeList {
		t.Fatalf("expected mode=modeList after confirming delete, got %v", m.mode)
	}
	if len(app.deleteCalls) != 1 || app.deleteCalls[0] != id {
		t.Fatalf("expected Delete(%q) to be called once, got calls: %v", id, app.deleteCalls)
	}
}

func TestDeleteKeyThenOtherKeyCancels(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(*model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(*model)

	if m.mode != modeList {
		t.Fatalf("expected mode=modeList after cancelling delete, got %v", m.mode)
	}
	if len(app.deleteCalls) != 0 {
		t.Fatalf("expected Delete not to be called after cancelling, got calls: %v", app.deleteCalls)
	}
}

func TestServiceKeyOpensPanelUnsupported(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)
	m.svc = &fakeInstaller{supported: false}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	m = updated.(*model)

	if m.mode != modeService {
		t.Fatalf("expected mode=modeService after 'S', got %v", m.mode)
	}
	if m.serviceModel == nil || m.serviceModel.Supported {
		t.Fatalf("expected an unsupported service panel")
	}
	view := m.View()
	if !contains(view, "not yet supported") {
		t.Fatalf("expected unsupported message in view, got: %q", view)
	}

	// Any key closes an unsupported panel.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*model)
	if m.mode != modeList {
		t.Fatalf("expected esc to close the panel, got mode=%v", m.mode)
	}
}

func TestServiceKeyLoadsStatusAndInstalls(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)
	fake := &fakeInstaller{supported: true, status: svcinstall.Status{Installed: false}}
	m.svc = fake

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	m = updated.(*model)
	if m.mode != modeService {
		t.Fatalf("expected mode=modeService, got %v", m.mode)
	}
	if cmd == nil {
		t.Fatalf("expected a status-loading tea.Cmd")
	}

	// Simulate the async Status() result arriving.
	statusMsg := cmd()
	updated, _ = m.Update(statusMsg)
	m = updated.(*model)
	if m.serviceModel.Loading {
		t.Fatalf("expected Loading=false after StatusResultMsg")
	}

	// Confirm install.
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(*model)
	if !m.serviceModel.Busy {
		t.Fatalf("expected Busy=true while Install is in flight")
	}
	if cmd == nil {
		t.Fatalf("expected an install tea.Cmd")
	}

	actionMsg := cmd()
	updated, _ = m.Update(actionMsg)
	m = updated.(*model)

	if len(fake.installCalls) != 1 {
		t.Fatalf("expected exactly one Install call, got %d", len(fake.installCalls))
	}
	if fake.installCalls[0].Force {
		t.Fatalf("expected a non-forced Install call")
	}
	if m.mode != modeList {
		t.Fatalf("expected panel to close after a successful install, got mode=%v", m.mode)
	}
	if !contains(m.toast, "enabled") {
		t.Fatalf("expected a success toast mentioning 'enabled', got: %q", m.toast)
	}
}

func TestServiceKeyLockHeldOffersForceRetry(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)
	fake := &fakeInstaller{
		supported:  true,
		status:     svcinstall.Status{Installed: false},
		installErr: svcinstall.ErrLockHeld,
	}
	m.svc = fake

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	updated, _ := m.Update(cmd())
	m = updated.(*model)

	// First 'y': attempts a non-forced install, which fails with
	// ErrLockHeld.
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	updated, _ = m.Update(cmd())
	m = updated.(*model)
	if !m.serviceModel.LockErr {
		t.Fatalf("expected LockErr=true after ErrLockHeld")
	}
	if len(fake.installCalls) != 1 || fake.installCalls[0].Force {
		t.Fatalf("expected exactly one non-forced Install call, got: %v", fake.installCalls)
	}

	// Second 'y': forces the retry.
	fake.installErr = nil
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatalf("expected a forced install tea.Cmd")
	}
	updated, _ = m.Update(cmd())
	m = updated.(*model)

	if len(fake.installCalls) != 2 || !fake.installCalls[1].Force {
		t.Fatalf("expected a second, forced Install call, got: %v", fake.installCalls)
	}
	if m.mode != modeList {
		t.Fatalf("expected panel to close after the forced install succeeds, got mode=%v", m.mode)
	}
}

func TestServiceKeyUninstallsWhenInstalled(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)
	fake := &fakeInstaller{supported: true, status: svcinstall.Status{Installed: true, Enabled: true, Running: true}}
	m.svc = fake

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	updated, _ := m.Update(cmd())
	m = updated.(*model)

	view := m.View()
	if !contains(view, "Disable") {
		t.Fatalf("expected the panel to offer to disable an installed service, got: %q", view)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(*model)
	if cmd == nil {
		t.Fatalf("expected an uninstall tea.Cmd")
	}
	updated, _ = m.Update(cmd())
	m = updated.(*model)

	if fake.uninstallCalls != 1 {
		t.Fatalf("expected exactly one Uninstall call, got %d", fake.uninstallCalls)
	}
	if m.mode != modeList {
		t.Fatalf("expected panel to close after a successful uninstall, got mode=%v", m.mode)
	}
	if !contains(m.toast, "disabled") {
		t.Fatalf("expected a success toast mentioning 'disabled', got: %q", m.toast)
	}
}

func TestServiceKeyDeclineDoesNotCallInstall(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)
	fake := &fakeInstaller{supported: true, status: svcinstall.Status{Installed: false}}
	m.svc = fake

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	updated, _ := m.Update(cmd())
	m = updated.(*model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(*model)

	if len(fake.installCalls) != 0 {
		t.Fatalf("expected Install not to be called after declining, got calls: %v", fake.installCalls)
	}
	if m.mode != modeList {
		t.Fatalf("expected declining to close the panel, got mode=%v", m.mode)
	}
}

func TestServiceStalePanelResultIsDiscardedAfterReopen(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)
	fake := &fakeInstaller{supported: true, status: svcinstall.Status{Installed: false}}
	m.svc = fake

	// First open: dispatch Status, but do not deliver its result yet.
	_, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	if firstCmd == nil {
		t.Fatalf("expected a status-loading tea.Cmd on first open")
	}

	// Close, then reopen: this bumps serviceGen and creates a fresh panel.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*model)
	if m.mode != modeList {
		t.Fatalf("expected esc to close the panel, got mode=%v", m.mode)
	}

	fake.status = svcinstall.Status{Installed: true, Enabled: true, Running: true}
	_, secondCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	if secondCmd == nil {
		t.Fatalf("expected a status-loading tea.Cmd on second open")
	}

	// Deliver the FIRST (stale) command's result now, after the second
	// open. It must be discarded, not applied to the reopened panel.
	staleMsg := firstCmd()
	updated, _ = m.Update(staleMsg)
	m = updated.(*model)
	if !m.serviceModel.Loading {
		t.Fatalf("expected the reopened panel to still be Loading after a stale result was discarded")
	}

	// Now deliver the SECOND (current) command's result; it must be
	// applied.
	currentMsg := secondCmd()
	updated, _ = m.Update(currentMsg)
	m = updated.(*model)
	if m.serviceModel.Loading {
		t.Fatalf("expected the current result to be applied")
	}
	if !m.serviceModel.Status.Installed {
		t.Fatalf("expected the current (Installed=true) status to be applied, got %+v", m.serviceModel.Status)
	}
}

func TestHelpKeyShowsAndTogglesOffHelp(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = updated.(*model)

	if m.mode != modeHelp {
		t.Fatalf("expected mode=modeHelp after '?', got %v", m.mode)
	}
	view := m.View()
	if !contains(view, "quit") {
		t.Fatalf("expected help view to render keybinding help, got: %q", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = updated.(*model)

	if m.mode != modeList {
		t.Fatalf("expected '?' to toggle help off, got mode=%v", m.mode)
	}
}

func TestHelpKeyClosesOnEscape(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	m = updated.(*model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*model)

	if m.mode != modeList {
		t.Fatalf("expected esc to close help, got mode=%v", m.mode)
	}
}

func TestNewKeyNoOpOnWireGuardProfile(t *testing.T) {
	app := newFakeApplication(newTestProfile("alpha", 1080))
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(*model)

	if m.mode != modeList {
		t.Fatalf("expected 'n' on a WireGuard profile to stay in modeList, got %v", m.mode)
	}
	if m.toast == "" {
		t.Fatalf("expected a toast explaining exit-node selection is Tailscale-only")
	}
}

func TestLoginKeyCallsLoginDirectly(t *testing.T) {
	tsProfile := profile.Profile{ID: "ts1", Kind: profile.BackendTailscale, Name: "ts", SocksHost: "127.0.0.1", SocksPort: 1080}
	app := newFakeApplication(tsProfile)
	m := newModel(app, nil)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	m = updated.(*model)

	if len(app.loginCalls) != 1 || app.loginCalls[0] != tsProfile.ID {
		t.Fatalf("expected Login(%q) to be called once, got calls: %v", tsProfile.ID, app.loginCalls)
	}
	if m.mode != modeList {
		t.Fatalf("expected Login not to open a form/mode, got mode=%v", m.mode)
	}
}
