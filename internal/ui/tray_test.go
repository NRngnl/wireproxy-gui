//go:build !js

package ui

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/egoist/mygo"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/svcinstall"
)

func newTestHost(profiles ...profile.Profile) (*host, *fakeApplication) {
	core := newFakeApplication(profiles...)
	return &host{w: &workbench{core: core}, ctx: context.Background()}, core
}

func requireMenuItem(t *testing.T, menu *mygo.Menu, label string) *mygo.MenuItem {
	t.Helper()
	for _, item := range menu.Items() {
		if item.Label == label {
			return item
		}
	}
	t.Fatalf("menu is missing %q", label)
	return nil
}

func requireChild(t *testing.T, parent *mygo.MenuItem, label string, disabled bool) {
	t.Helper()
	for _, item := range parent.Submenu {
		if item.Label == label {
			if item.Disabled != disabled {
				t.Fatalf("%q disabled = %t, want %t", label, item.Disabled, disabled)
			}
			return
		}
	}
	t.Fatalf("submenu %q is missing %q", parent.Label, label)
}

func TestTrayMenuShowsProfileSubmenusWithInfoAndActions(t *testing.T) {
	office := profile.New("office", sampleWireGuardConfig, 1080)
	home := profile.New("home", sampleWireGuardConfig, 1081)
	d, core := newTestHost(office, home)
	core.statuses[office.ID] = application.StatusRunning
	menu := d.trayMenu()
	officeItem := requireMenuItem(t, menu, "office")
	homeItem := requireMenuItem(t, menu, "home")
	requireChild(t, officeItem, "Status: connected", true)
	requireChild(t, officeItem, "SOCKS5 bind: 127.0.0.1:1080", true)
	requireChild(t, officeItem, "Connect", true)
	requireChild(t, officeItem, "Disconnect", false)
	requireChild(t, homeItem, "Connect", false)
	requireChild(t, homeItem, "Disconnect", true)
}

func TestTrayMenuShowsTailscaleProfileDetail(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	item.TailscaleConfig.Hostname = "proxy-node"
	d, _ := newTestHost(item)
	requireChild(t, requireMenuItem(t, d.trayMenu(), "tailnet"), "Tailscale node: proxy-node", true)
}

func TestServiceItemFollowsInstallerState(t *testing.T) {
	d, _ := newTestHost()
	d.svc = &fakeInstaller{supported: false}
	if !d.serviceItem().Disabled {
		t.Fatal("unsupported installer should disable the item")
	}
	d.svc = &fakeInstaller{supported: true}
	if got := d.serviceItem().Label; got != tr("Enable Start on Login") {
		t.Fatalf("label = %q", got)
	}
	d.svcOn = true
	if got := d.serviceItem().Label; got != tr("Disable Start on Login") {
		t.Fatalf("label = %q", got)
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
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", info, err)
	}
}

// fakeInstaller is a test double for svcinstall.Installer.
type fakeInstaller struct {
	mu sync.Mutex

	supported bool
	status    svcinstall.Status
	statusErr error

	installErr      error
	installCalls    int
	lastInstallOpts svcinstall.Options

	uninstallErr   error
	uninstallCalls int
}

func (f *fakeInstaller) Supported() bool { return f.supported }

func (f *fakeInstaller) Status(context.Context) (svcinstall.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeInstaller) Install(_ context.Context, opts svcinstall.Options) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installCalls++
	f.lastInstallOpts = opts
	return f.installErr
}

func (f *fakeInstaller) Uninstall(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uninstallCalls++
	return f.uninstallErr
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
