package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

const sampleWG = `[Interface]
Address = 10.2.0.2/32
PrivateKey = placeholder-interface-value

[Peer]
PublicKey = placeholder-peer-value
AllowedIPs = 0.0.0.0/0
`

var (
	errDiskFull           = errors.New("disk full")
	errRuntimeStart       = errors.New("runtime start failed")
	errStateRemovalFailed = errors.New("state removal failed")
)

func TestLoadInitializesStoppedProfiles(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, _, _, _ := newLoadedService(t, item)

	profiles := service.Profiles()
	if len(profiles) != 1 || profiles[0].ID != item.ID {
		t.Fatalf("Profiles() = %#v", profiles)
	}
	if got := service.Status(item.ID); got != StatusStopped {
		t.Fatalf("Status() = %q, want stopped", got)
	}
}

func TestAddChoosesNextPortAndPersistsAtomically(t *testing.T) {
	existing := profile.New("existing", sampleWG, 1080)
	service, repository, _, _ := newLoadedService(t, existing)

	added, err := service.Add("new")
	if err != nil {
		t.Fatal(err)
	}
	if added.SocksPort != 1081 {
		t.Fatalf("added port = %d, want 1081", added.SocksPort)
	}
	if len(repository.snapshot()) != 2 || len(service.Profiles()) != 2 {
		t.Fatal("added profile was not persisted and committed")
	}

	repository.queueSaveError(errDiskFull)
	_, err = service.Add("failed")
	if err == nil {
		t.Fatal("expected save failure")
	}
	if len(service.Profiles()) != 2 {
		t.Fatalf("failed add changed application state: %#v", service.Profiles())
	}
}

func TestSaveLeavesUnchangedProfileUntouched(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	updatedAt := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	item.UpdatedAt = updatedAt
	service, repository, _, _ := newLoadedService(t, item)
	savesBefore := repository.saveCallCount()

	result, err := service.Save(item)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("unchanged save reported a change")
	}
	got, _ := service.Profile(item.ID)
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("unchanged save touched UpdatedAt: %s", got.UpdatedAt)
	}
	if repository.saveCallCount() != savesBefore || len(service.Logs(item.ID)) != 0 {
		t.Fatal("unchanged save persisted or logged")
	}
}

func TestSaveChangedProfileTouchesPersistsAndLogs(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	updatedAt := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	item.UpdatedAt = updatedAt
	service, _, _, _ := newLoadedService(t, item)
	item.Name = "renamed"

	result, err := service.Save(item)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := service.Profile(item.ID)
	if !result.Changed || got.Name != "renamed" || !got.UpdatedAt.After(updatedAt) {
		t.Fatalf("unexpected saved profile/result: %#v %#v", got, result)
	}
	assertLogContains(t, service, item.ID, "saved profile")
}

func TestSaveEnforcesRuntimeEditBoundaries(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.setRunning(item.ID, true)
	service.handleRuntimeEvent(connection.Event{Type: connection.EventStarted, ProfileID: item.ID, At: time.Now()})

	runtimeEdit := item
	runtimeEdit.SocksPort = 1081
	_, err := service.Save(runtimeEdit)
	if !errors.Is(err, ErrRuntimeProfileEdit) {
		t.Fatalf("runtime edit error = %v, want ErrRuntimeProfileEdit", err)
	}

	nonRuntimeEdit := item
	nonRuntimeEdit.Name = "renamed"
	nonRuntimeEdit.AutoStart = true
	_, err = service.Save(nonRuntimeEdit)
	if err != nil {
		t.Fatalf("non-runtime edit failed: %v", err)
	}
	got, _ := service.Profile(item.ID)
	if got.Name != "renamed" || !got.AutoStart || got.SocksPort != 1080 {
		t.Fatalf("non-runtime save = %#v", got)
	}
}

func TestSaveAppliesRunningTailscaleExitNode(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.setRunning(item.ID, true)
	service.handleRuntimeEvent(connection.Event{Type: connection.EventStarted, ProfileID: item.ID, At: time.Now()})
	updated, _ := service.Profile(item.ID)
	updated.TailscaleConfig.ExitNode = "stable-exit"
	updated.TailscaleConfig.ExitNodeAllowLANAccess = true

	result, err := service.Save(updated)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ExitNodeUpdated || runtime.updateExitNodeCallCount() != 1 {
		t.Fatalf("result/calls = %#v/%d", result, runtime.updateExitNodeCallCount())
	}
	_, config := runtime.updatedExitNodeAt(0)
	if config.ExitNode != "stable-exit" || !config.ExitNodeAllowLANAccess {
		t.Fatalf("runtime exit-node config = %#v", config)
	}
	assertLogContains(t, service, item.ID, "updated Tailscale exit-node settings")
}

func TestSaveBlocksExitNodeChangeDuringTransition(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	service, _, _, runtime := newLoadedService(t, item)
	service.mu.Lock()
	service.statuses[item.ID] = StatusStarting
	service.mu.Unlock()
	updated := item
	updated.TailscaleConfig.ExitNode = "stable-exit"

	_, err := service.Save(updated)
	if !errors.Is(err, ErrRuntimeExitNodeEdit) {
		t.Fatalf("exit-node edit error = %v, want ErrRuntimeExitNodeEdit", err)
	}
	if runtime.updateExitNodeCallCount() != 0 {
		t.Fatal("transitioning profile updated runtime exit node")
	}
}

func TestSaveRollsBackRuntimeExitNodeWhenPersistenceFails(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	service, repository, _, runtime := newLoadedService(t, item)
	runtime.setRunning(item.ID, true)
	service.handleRuntimeEvent(connection.Event{Type: connection.EventStarted, ProfileID: item.ID, At: time.Now()})
	repository.queueSaveError(errDiskFull)
	updated, _ := service.Profile(item.ID)
	updated.TailscaleConfig.ExitNode = "stable-exit"

	_, err := service.Save(updated)
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if runtime.updateExitNodeCallCount() != 2 {
		t.Fatalf("runtime update calls = %d, want apply and rollback", runtime.updateExitNodeCallCount())
	}
	got, _ := service.Profile(item.ID)
	if got.TailscaleConfig.ExitNode != "" {
		t.Fatalf("failed save changed application profile: %#v", got.TailscaleConfig)
	}
}

func TestImportPreparesProfilesAndRollsBackOnSaveFailure(t *testing.T) {
	existing := profile.New("existing", sampleWG, 1080)
	service, repository, codec, _ := newLoadedService(t, existing)
	imported := profile.NewTailscale("", 1080)
	oldID := imported.ID
	imported.TailscaleConfig.Authenticated = true
	codec.decoded = []profile.Profile{imported}

	got, err := service.Import("profiles.json", []byte("ignored"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID == oldID || got[0].Name != "Imported profile" || got[0].SocksPort != 1081 {
		t.Fatalf("prepared import = %#v", got)
	}
	if got[0].TailscaleConfig.Authenticated {
		t.Fatal("import retained machine-local Tailscale authentication")
	}
	assertLogContains(t, service, got[0].ID, "imported profile")

	repository.queueSaveError(errDiskFull)
	codec.decoded = []profile.Profile{profile.New("another", sampleWG, 1082)}
	_, err = service.Import("profiles.json", []byte("ignored"))
	if err == nil {
		t.Fatal("expected import save failure")
	}
	if len(service.Profiles()) != 2 {
		t.Fatalf("failed import changed state: %#v", service.Profiles())
	}
}

func TestExportUsesSelectedProfiles(t *testing.T) {
	first := profile.New("first", sampleWG, 1080)
	second := profile.New("second", sampleWG, 1081)
	service, _, codec, _ := newLoadedService(t, first, second)
	codec.encoded = []byte("encoded")

	data, err := service.Export([]string{second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "encoded" || len(codec.encodeInput) != 1 || codec.encodeInput[0].ID != second.ID {
		t.Fatalf("export data/input = %q/%#v", data, codec.encodeInput)
	}
}

func TestDeletePersistsBeforeStoppingRuntime(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, repository, _, runtime := newLoadedService(t, item)
	runtime.setRunning(item.ID, true)

	err := service.Delete(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(service.Profiles()) != 0 || len(repository.snapshot()) != 0 || runtime.stopCallCount() != 1 {
		t.Fatalf("delete state: service=%#v repo=%#v stops=%d", service.Profiles(), repository.snapshot(), runtime.stopCallCount())
	}
}

func TestConnectReturnsQuicklyAndTransitionsToRunning(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.startBlock = make(chan struct{})
	runtime.startStarted = make(chan string, 1)

	err := service.Connect(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.startStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime start did not begin")
	}
	if got := service.Status(item.ID); got != StatusStarting {
		t.Fatalf("status while Start blocks = %q", got)
	}
	assertLogContains(t, service, item.ID, "connecting profile")
	close(runtime.startBlock)
	waitFor(t, func() bool { return service.Status(item.ID) == StatusRunning }, "running status")
}

func TestDisconnectCancelsPendingStart(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.startBlock = make(chan struct{})
	runtime.startStarted = make(chan string, 1)
	err := service.Connect(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-runtime.startStarted

	if !service.Disconnect(item.ID) {
		t.Fatal("Disconnect should report a canceled pending start")
	}
	waitFor(t, func() bool { return service.Status(item.ID) == StatusStopped }, "stopped status")
	assertLogContains(t, service, item.ID, "disconnected")
}

func TestStoppedEventDuringStartIsNotOverwrittenBySuccessfulReturn(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.startBlock = make(chan struct{})
	runtime.startStarted = make(chan string, 1)
	runtime.startSetsRunning = false
	err := service.Connect(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	<-runtime.startStarted
	service.handleRuntimeEvent(connection.Event{
		Type:      connection.EventStopped,
		ProfileID: item.ID,
		Message:   "stopped during start",
		At:        time.Now(),
	})
	close(runtime.startBlock)
	waitFor(t, func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		return service.starts[item.ID] == nil
	}, "start completion")
	if got := service.Status(item.ID); got != StatusStopped {
		t.Fatalf("status = %q, want stopped", got)
	}
}

func TestConnectPublishesAsynchronousFailureWithOperation(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.startErr = errRuntimeStart
	err := service.Connect(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return service.Status(item.ID) == StatusError }, "start failure")
	deadline := time.After(2 * time.Second)
	for {
		select {
		case change := <-service.Changes():
			if errors.Is(change.Err, errRuntimeStart) {
				if change.Operation != OperationConnect || change.ProfileID != item.ID {
					t.Fatalf("failure change = %#v", change)
				}
				return
			}
		case <-deadline:
			t.Fatal("asynchronous failure notification was not published")
		}
	}
}

func TestConnectRejectsActiveBindConflict(t *testing.T) {
	active := profile.New("active", sampleWG, 1080)
	candidate := profile.New("candidate", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, active, candidate)
	runtime.setRunning(active.ID, true)

	err := service.Connect(candidate.ID)
	if !errors.Is(err, profile.ErrDuplicateBindAddress) || !strings.Contains(err.Error(), "active") {
		t.Fatalf("Connect error = %v", err)
	}
}

func TestConnectAllRejectsDuplicateBindAddresses(t *testing.T) {
	first := profile.New("first", sampleWG, 1080)
	second := profile.New("second", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, first, second)

	err := service.ConnectAll()
	if !errors.Is(err, profile.ErrDuplicateBindAddress) {
		t.Fatalf("ConnectAll error = %v", err)
	}
	if runtime.startCallCount() != 0 {
		t.Fatal("ConnectAll started a profile despite duplicate binds")
	}
}

func TestAutoConnectMarksDuplicateProfilesAsErrors(t *testing.T) {
	first := profile.New("first", sampleWG, 1080)
	first.AutoStart = true
	second := profile.New("second", sampleWG, 1080)
	second.AutoStart = true
	service, _, _, _ := newLoadedService(t, first, second)

	err := service.AutoConnect()
	if !errors.Is(err, profile.ErrDuplicateBindAddress) {
		t.Fatalf("AutoConnect error = %v", err)
	}
	for _, item := range []profile.Profile{first, second} {
		if service.Status(item.ID) != StatusError {
			t.Fatalf("auto profile %s status = %q", item.ID, service.Status(item.ID))
		}
		assertLogContains(t, service, item.ID, profile.ErrDuplicateBindAddress.Error())
	}
}

func TestSuccessfulTailscaleStartClearsAuthKeyAndPersists(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	item.TailscaleConfig.AuthKey = "tskey-auth-example"
	service, repository, _, runtime := newLoadedService(t, item)

	err := service.Login(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return service.Status(item.ID) == StatusRunning }, "Tailscale running status")
	got, _ := service.Profile(item.ID)
	if !got.TailscaleConfig.Authenticated || got.TailscaleConfig.AuthKey != "" {
		t.Fatalf("authenticated profile = %#v", got.TailscaleConfig)
	}
	saved := repository.snapshot()
	if len(saved) != 1 || !saved[0].TailscaleConfig.Authenticated || saved[0].TailscaleConfig.AuthKey != "" {
		t.Fatalf("persisted authenticated profile = %#v", saved)
	}
	if runtime.startCallCount() != 1 {
		t.Fatalf("runtime starts = %d", runtime.startCallCount())
	}
	assertLogContains(t, service, item.ID, "auth key removed")
}

func TestLogoutClearsAuthAndRestoresItOnRuntimeFailure(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	item.TailscaleConfig.Authenticated = true
	service, repository, _, runtime := newLoadedService(t, item)

	err := service.Logout(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := service.Profile(item.ID)
	if got.TailscaleConfig.Authenticated || runtime.logoutCallCount() != 1 {
		t.Fatalf("logout state = %#v calls=%d", got.TailscaleConfig, runtime.logoutCallCount())
	}
	assertLogContains(t, service, item.ID, "logged out of Tailscale")

	got.TailscaleConfig.Authenticated = true
	_, err = service.Save(got)
	if err != nil {
		t.Fatal(err)
	}
	runtime.logoutErr = errStateRemovalFailed
	err = service.Logout(context.Background(), item.ID)
	if err == nil {
		t.Fatal("expected runtime logout failure")
	}
	restored, _ := service.Profile(item.ID)
	if !restored.TailscaleConfig.Authenticated || !repository.snapshot()[0].TailscaleConfig.Authenticated {
		t.Fatalf("failed logout did not restore auth: %#v", restored.TailscaleConfig)
	}
}

func TestRuntimeEventsUpdateStatusAndLogs(t *testing.T) {
	item := profile.New("demo", sampleWG, 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.events <- connection.Event{
		Type:      connection.EventError,
		ProfileID: item.ID,
		Message:   "backend failed",
		At:        time.Now(),
	}
	waitFor(t, func() bool { return service.Status(item.ID) == StatusError }, "runtime error status")
	assertLogContains(t, service, item.ID, "backend failed")
}

func TestExitNodesLogsEmptyResult(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.exitNodes = []connection.ExitNode{}

	nodes, err := service.ExitNodes(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("ExitNodes() = %#v", nodes)
	}
	assertLogContains(t, service, item.ID, "no Tailscale exit nodes found")
}

func TestDisconnectAllMarksActiveStatusesStopping(t *testing.T) {
	starting := profile.New("starting", sampleWG, 1080)
	running := profile.New("running", sampleWG, 1081)
	stopped := profile.New("stopped", sampleWG, 1082)
	service, _, _, runtime := newLoadedService(t, starting, running, stopped)
	service.mu.Lock()
	service.statuses[starting.ID] = StatusStarting
	service.statuses[running.ID] = StatusRunning
	service.mu.Unlock()
	runtime.setRunning(running.ID, true)

	service.DisconnectAll()
	if service.Status(starting.ID) != StatusStopping || service.Status(running.ID) != StatusStopping {
		t.Fatalf("active statuses = %q/%q", service.Status(starting.ID), service.Status(running.ID))
	}
	if service.Status(stopped.ID) != StatusStopped {
		t.Fatalf("stopped status = %q", service.Status(stopped.ID))
	}
}

func TestShutdownStopsProfilesOnce(t *testing.T) {
	service, _, _, runtime := newLoadedService(t)
	err := service.Shutdown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = service.Shutdown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.stopAllAndWaitCallCount() != 1 {
		t.Fatalf("StopAllAndWait calls = %d, want 1", runtime.stopAllAndWaitCallCount())
	}
}

func TestPrepareImportedNeverAssignsOutOfRangePort(t *testing.T) {
	existing := make([]profile.Profile, 0, 65535)
	for port := 1; port <= 65535; port++ {
		existing = append(existing, profile.Profile{
			ID:              profile.NewID(),
			Name:            "used",
			WireGuardConfig: sampleWG,
			SocksHost:       profile.DefaultSocksHost,
			SocksPort:       port,
		})
	}
	imported := []profile.Profile{{Name: "imported", WireGuardConfig: sampleWG, SocksPort: 70000}}
	got := prepareImported(imported, existing)
	if len(got) != 1 || got[0].SocksPort < 1 || got[0].SocksPort > 65535 {
		t.Fatalf("prepared import = %#v", got)
	}
}

func assertLogContains(t *testing.T, service *Service, profileID, want string) {
	t.Helper()
	for _, entry := range service.Logs(profileID) {
		if strings.Contains(entry.Message, want) {
			return
		}
	}
	t.Fatalf("logs for %s do not contain %q: %#v", profileID, want, service.Logs(profileID))
}

func waitFor(t *testing.T, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func newLoadedService(t *testing.T, profiles ...profile.Profile) (*Service, *fakeRepository, *fakeCodec, *fakeRuntime) {
	t.Helper()
	repository := &fakeRepository{profiles: append([]profile.Profile(nil), profiles...)}
	codec := &fakeCodec{}
	runtime := newFakeRuntime()
	service := New(repository, codec, runtime)
	t.Cleanup(service.cancel)
	err := service.Load()
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, codec, runtime
}

type fakeRepository struct {
	mu         sync.Mutex
	profiles   []profile.Profile
	loadErr    error
	saveErrors []error
	saveCalls  int
}

func (r *fakeRepository) Load() ([]profile.Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]profile.Profile(nil), r.profiles...), r.loadErr
}

func (r *fakeRepository) Save(profiles []profile.Profile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saveCalls++
	if len(r.saveErrors) > 0 {
		err := r.saveErrors[0]
		r.saveErrors = r.saveErrors[1:]
		if err != nil {
			return err
		}
	}
	r.profiles = append([]profile.Profile(nil), profiles...)
	return nil
}

func (r *fakeRepository) queueSaveError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saveErrors = append(r.saveErrors, err)
}

func (r *fakeRepository) snapshot() []profile.Profile {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]profile.Profile(nil), r.profiles...)
}

func (r *fakeRepository) saveCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saveCalls
}

type fakeCodec struct {
	decoded     []profile.Profile
	decodeErr   error
	encoded     []byte
	encodeErr   error
	encodeInput []profile.Profile
}

func (c *fakeCodec) DecodeImport(string, []byte) ([]profile.Profile, error) {
	return append([]profile.Profile(nil), c.decoded...), c.decodeErr
}

func (c *fakeCodec) EncodeExport(profiles []profile.Profile) ([]byte, error) {
	c.encodeInput = append([]profile.Profile(nil), profiles...)
	return append([]byte(nil), c.encoded...), c.encodeErr
}

type fakeRuntime struct {
	events chan connection.Event

	mu                  sync.Mutex
	running             map[string]bool
	startErr            error
	startBlock          chan struct{}
	startStarted        chan string
	startSetsRunning    bool
	startProfiles       []profile.Profile
	stopCalls           []string
	exitNodes           []connection.ExitNode
	exitNodesErr        error
	updateExitNodeCalls []string
	updateExitNodes     []profile.TailscaleConfig
	updateExitNodeErr   error
	logoutCalls         []string
	logoutErr           error
	stopAllAndWaitCalls int
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		events:           make(chan connection.Event, 32),
		running:          map[string]bool{},
		startSetsRunning: true,
	}
}

func (r *fakeRuntime) Events() <-chan connection.Event { return r.events }

func (r *fakeRuntime) Running(profileID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[profileID]
}

func (r *fakeRuntime) ExitNodes(context.Context, string) ([]connection.ExitNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]connection.ExitNode(nil), r.exitNodes...), r.exitNodesErr
}

func (r *fakeRuntime) UpdateExitNode(_ context.Context, profileID string, config profile.TailscaleConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateExitNodeCalls = append(r.updateExitNodeCalls, profileID)
	r.updateExitNodes = append(r.updateExitNodes, config)
	return r.updateExitNodeErr
}

func (r *fakeRuntime) Logout(_ context.Context, profileID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logoutCalls = append(r.logoutCalls, profileID)
	return r.logoutErr
}

func (r *fakeRuntime) Start(ctx context.Context, item profile.Profile) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	r.mu.Lock()
	r.startProfiles = append(r.startProfiles, item)
	block := r.startBlock
	err := r.startErr
	started := r.startStarted
	setsRunning := r.startSetsRunning
	r.mu.Unlock()
	if started != nil {
		select {
		case started <- item.ID:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	if setsRunning {
		r.mu.Lock()
		r.running[item.ID] = true
		r.mu.Unlock()
	}
	return nil
}

func (r *fakeRuntime) Stop(profileID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopCalls = append(r.stopCalls, profileID)
	if !r.running[profileID] {
		return false
	}
	delete(r.running, profileID)
	return true
}

func (r *fakeRuntime) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for profileID := range r.running {
		delete(r.running, profileID)
	}
}

func (r *fakeRuntime) StopAllAndWait(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopAllAndWaitCalls++
	return nil
}

func (r *fakeRuntime) setRunning(profileID string, running bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if running {
		r.running[profileID] = true
		return
	}
	delete(r.running, profileID)
}

func (r *fakeRuntime) startCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.startProfiles)
}

func (r *fakeRuntime) stopCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.stopCalls)
}

func (r *fakeRuntime) updateExitNodeCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.updateExitNodeCalls)
}

func (r *fakeRuntime) updatedExitNodeAt(index int) (string, profile.TailscaleConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updateExitNodeCalls[index], r.updateExitNodes[index]
}

func (r *fakeRuntime) logoutCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.logoutCalls)
}

func (r *fakeRuntime) stopAllAndWaitCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopAllAndWaitCalls
}
