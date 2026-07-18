// Package application coordinates Wireproxy GUI use cases. It depends only on
// domain types and ports; desktop UI, persistence, and network backends are
// adapters supplied by the composition root.
package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

const maxLogEntries = 1000

var (
	ErrImportFileEmpty     = errors.New("import file is empty")
	ErrImportJSONInvalid   = errors.New("import JSON is invalid")
	ErrImportProfilesEmpty = errors.New("import JSON does not contain any valid profiles")
	ErrProfileNotFound     = errors.New("profile was not found")
	ErrRuntimeProfileEdit  = errors.New("disconnect the profile, and wait for it to finish disconnecting, before changing its backend, SOCKS5 bind address, WireGuard config, or Tailscale auth settings")
	ErrRuntimeExitNodeEdit = errors.New("wait until the Tailscale profile is connected or disconnected before changing exit-node settings")
	ErrRuntimeUnavailable  = errors.New("profile runtime is unavailable")
)

// Repository is the outbound persistence port used by profile use cases.
type Repository interface {
	Load() ([]profile.Profile, error)
	Save([]profile.Profile) error
}

// Codec is the outbound profile transfer port used for import and export.
type Codec interface {
	DecodeImport(fileName string, data []byte) ([]profile.Profile, error)
	EncodeExport([]profile.Profile) ([]byte, error)
}

// Runtime is the outbound port implemented by the embedded connection backends.
type Runtime interface {
	Events() <-chan connection.Event
	Running(profileID string) bool
	ExitNodes(context.Context, string) ([]connection.ExitNode, error)
	UpdateExitNode(context.Context, string, profile.TailscaleConfig) error
	Logout(context.Context, string) error
	Start(context.Context, profile.Profile) error
	Stop(profileID string) bool
	StopAll()
	StopAllAndWait(context.Context) error
}

type Status string

const (
	StatusStopped  Status = "stopped"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusStopping Status = "stopping"
	StatusError    Status = "error"
)

type Operation string

const (
	OperationNone       Operation = ""
	OperationConnect    Operation = "connect"
	OperationConnectAll Operation = "connect-all"
	OperationLogin      Operation = "login"
	OperationSave       Operation = "save"
	OperationAutoStart  Operation = "auto-start"
)

// Change tells presentation adapters that queryable application state changed.
// Err is populated only for asynchronous operation failures.
type Change struct {
	ProfileID    string
	RuntimeEvent connection.EventType
	Operation    Operation
	Err          error
}

type LogEntry struct {
	At      time.Time
	Message string
}

type SaveResult struct {
	Changed         bool
	ExitNodeUpdated bool
}

type pendingStart struct {
	cancel    context.CancelFunc
	operation Operation
}

type Service struct {
	repository Repository
	codec      Codec
	runtime    Runtime

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	profiles []profile.Profile
	statuses map[string]Status
	logs     map[string][]LogEntry
	starts   map[string]*pendingStart
	changes  chan Change

	shutdownOnce sync.Once
	shutdownErr  error
}

func New(repository Repository, codec Codec, runtime Runtime) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		repository: repository,
		codec:      codec,
		runtime:    runtime,
		ctx:        ctx,
		cancel:     cancel,
		statuses:   map[string]Status{},
		logs:       map[string][]LogEntry{},
		starts:     map[string]*pendingStart{},
		changes:    make(chan Change, 512),
	}
	if runtime != nil {
		go service.consumeRuntimeEvents()
	}
	return service
}

func (s *Service) Load() error {
	profiles, err := s.repository.Load()
	if err != nil {
		return err
	}
	for i := range profiles {
		profiles[i].Normalize()
	}

	s.mu.Lock()
	s.profiles = cloneProfiles(profiles)
	s.statuses = make(map[string]Status, len(profiles))
	s.logs = make(map[string][]LogEntry, len(profiles))
	s.starts = map[string]*pendingStart{}
	for _, item := range profiles {
		s.statuses[item.ID] = StatusStopped
	}
	s.mu.Unlock()
	s.notify(Change{})
	return nil
}

func (s *Service) Changes() <-chan Change {
	return s.changes
}

func (s *Service) Profiles() []profile.Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProfiles(s.profiles)
}

func (s *Service) Profile(profileID string) (profile.Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.profileIndexLocked(profileID)
	if index < 0 {
		return profile.Profile{}, false
	}
	return s.profiles[index], true
}

func (s *Service) Status(profileID string) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(profileID)
}

func (s *Service) Logs(profileID string) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]LogEntry(nil), s.logs[profileID]...)
}

func (s *Service) RuntimeLocked(profileID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeLockedLocked(profileID)
}

func (s *Service) Add(name string) (profile.Profile, error) {
	s.mu.Lock()
	item := profile.New(name, "", profile.NextAvailablePort(s.profiles))
	next := append(cloneProfiles(s.profiles), item)
	err := s.repository.Save(next)
	if err != nil {
		s.mu.Unlock()
		return profile.Profile{}, err
	}
	s.profiles = next
	s.statuses[item.ID] = StatusStopped
	s.mu.Unlock()
	s.notify(Change{ProfileID: item.ID})
	return item, nil
}

func (s *Service) Save(updated profile.Profile) (SaveResult, error) {
	updated.Normalize()
	err := updated.Validate()
	if err != nil {
		return SaveResult{}, err
	}

	s.mu.Lock()
	index := s.profileIndexLocked(updated.ID)
	if index < 0 {
		s.mu.Unlock()
		return SaveResult{}, ErrProfileNotFound
	}
	existing := s.profiles[index]
	locked := s.runtimeLockedLocked(existing.ID)
	if locked && profile.RuntimeConfigChanged(existing, updated) {
		s.mu.Unlock()
		return SaveResult{}, ErrRuntimeProfileEdit
	}

	exitNodeChanged := profile.ExitNodeConfigChanged(existing, updated)
	applyExitNode := exitNodeChanged && existing.IsTailscale() && s.runtime != nil &&
		s.runtime.Running(existing.ID) && s.statuses[existing.ID] == StatusRunning
	if locked && exitNodeChanged && !applyExitNode {
		s.mu.Unlock()
		return SaveResult{}, ErrRuntimeExitNodeEdit
	}
	if !profile.FieldsChanged(existing, updated) {
		s.mu.Unlock()
		return SaveResult{}, nil
	}

	if applyExitNode {
		err = s.runtime.UpdateExitNode(s.ctx, updated.ID, updated.TailscaleConfig)
		if err != nil {
			s.mu.Unlock()
			return SaveResult{}, fmt.Errorf("update Tailscale exit node: %w", err)
		}
	}

	updated.Touch()
	next := cloneProfiles(s.profiles)
	next[index] = updated
	err = s.repository.Save(next)
	if err != nil {
		if applyExitNode {
			restoreErr := s.runtime.UpdateExitNode(s.ctx, existing.ID, existing.TailscaleConfig)
			if restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore Tailscale exit node: %w", restoreErr))
			}
		}
		s.mu.Unlock()
		return SaveResult{}, err
	}
	s.profiles = next
	message := "saved profile"
	if applyExitNode {
		message = "updated Tailscale exit-node settings"
	}
	s.appendLogLocked(updated.ID, time.Now(), message)
	s.mu.Unlock()
	s.notify(Change{ProfileID: updated.ID})
	return SaveResult{Changed: true, ExitNodeUpdated: applyExitNode}, nil
}

func (s *Service) Delete(profileID string) error {
	s.mu.Lock()
	index := s.profileIndexLocked(profileID)
	if index < 0 {
		s.mu.Unlock()
		return ErrProfileNotFound
	}
	next := make([]profile.Profile, 0, len(s.profiles)-1)
	next = append(next, s.profiles[:index]...)
	next = append(next, s.profiles[index+1:]...)
	err := s.repository.Save(next)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if pending := s.starts[profileID]; pending != nil {
		pending.cancel()
		delete(s.starts, profileID)
	}
	if s.runtime != nil {
		s.runtime.Stop(profileID)
	}
	s.profiles = next
	delete(s.statuses, profileID)
	delete(s.logs, profileID)
	s.mu.Unlock()
	s.notify(Change{ProfileID: profileID})
	return nil
}

func (s *Service) Import(fileName string, data []byte) ([]profile.Profile, error) {
	imported, err := s.codec.DecodeImport(fileName, data)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	imported = prepareImported(imported, s.profiles)
	next := append(cloneProfiles(s.profiles), imported...)
	err = s.repository.Save(next)
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("save imported profiles: %w", err)
	}
	s.profiles = next
	for _, item := range imported {
		s.statuses[item.ID] = StatusStopped
		s.appendLogLocked(item.ID, time.Now(), "imported profile")
	}
	s.mu.Unlock()
	for _, item := range imported {
		s.notify(Change{ProfileID: item.ID})
	}
	return cloneProfiles(imported), nil
}

// Export encodes selected profiles. A nil profileIDs slice exports all profiles.
func (s *Service) Export(profileIDs []string) ([]byte, error) {
	s.mu.Lock()
	selected := cloneProfiles(s.profiles)
	if profileIDs != nil {
		selected = make([]profile.Profile, 0, len(profileIDs))
		for _, profileID := range profileIDs {
			index := s.profileIndexLocked(profileID)
			if index < 0 {
				s.mu.Unlock()
				return nil, ErrProfileNotFound
			}
			selected = append(selected, s.profiles[index])
		}
	}
	s.mu.Unlock()
	return s.codec.EncodeExport(selected)
}

func (s *Service) Connect(profileID string) error {
	return s.connect(profileID, OperationConnect, true)
}

func (s *Service) Login(profileID string) error {
	item, ok := s.Profile(profileID)
	if !ok {
		return ErrProfileNotFound
	}
	if !item.IsTailscale() {
		return connection.ErrNotTailscale
	}
	return s.connect(profileID, OperationLogin, true)
}

func (s *Service) connect(profileID string, operation Operation, checkConflict bool) error {
	s.mu.Lock()
	index := s.profileIndexLocked(profileID)
	if index < 0 {
		s.mu.Unlock()
		return ErrProfileNotFound
	}
	item := s.profiles[index]
	if checkConflict {
		err := s.runningBindConflictLocked(item)
		if err != nil {
			s.mu.Unlock()
			return err
		}
	}
	ctx, pending, started, err := s.beginStartLocked(item, operation)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !started {
		return nil
	}
	s.notify(Change{ProfileID: item.ID})
	go s.startRuntime(ctx, item, pending)
	return nil
}

func (s *Service) ConnectAll() error {
	s.mu.Lock()
	err := duplicateBindError(s.profiles)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	starts, err := s.beginStartsLocked(s.profiles, OperationConnectAll)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.launchStarts(starts)
	return nil
}

func (s *Service) AutoConnect() error {
	s.mu.Lock()
	auto := make([]profile.Profile, 0, len(s.profiles))
	for _, item := range s.profiles {
		if item.AutoStart {
			auto = append(auto, item)
		}
	}
	err := duplicateBindError(auto)
	if err != nil {
		for _, item := range auto {
			s.statuses[item.ID] = StatusError
			s.appendLogLocked(item.ID, time.Now(), err.Error())
		}
		s.mu.Unlock()
		for _, item := range auto {
			s.notify(Change{ProfileID: item.ID})
		}
		return err
	}
	starts, err := s.beginStartsLocked(auto, OperationAutoStart)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.launchStarts(starts)
	return nil
}

type startRequest struct {
	ctx     context.Context
	profile profile.Profile
	pending *pendingStart
}

func (s *Service) beginStartsLocked(profiles []profile.Profile, operation Operation) ([]startRequest, error) {
	starts := make([]startRequest, 0, len(profiles))
	for _, item := range profiles {
		ctx, pending, started, err := s.beginStartLocked(item, operation)
		if err != nil {
			return starts, err
		}
		if started {
			starts = append(starts, startRequest{ctx: ctx, profile: item, pending: pending})
		}
	}
	return starts, nil
}

func (s *Service) beginStartLocked(item profile.Profile, operation Operation) (context.Context, *pendingStart, bool, error) {
	if s.runtime == nil {
		return nil, nil, false, ErrRuntimeUnavailable
	}
	if s.runtime.Running(item.ID) || runtimeLockedStatus(s.statuses[item.ID]) {
		return nil, nil, false, nil
	}
	startCtx, cancel := context.WithCancel(s.ctx)
	pending := &pendingStart{cancel: cancel, operation: operation}
	s.starts[item.ID] = pending
	s.statuses[item.ID] = StatusStarting
	s.appendLogLocked(item.ID, time.Now(), "connecting profile")
	return startCtx, pending, true, nil
}

func (s *Service) launchStarts(starts []startRequest) {
	for _, start := range starts {
		s.notify(Change{ProfileID: start.profile.ID})
		go s.startRuntime(start.ctx, start.profile, start.pending)
	}
}

func (s *Service) startRuntime(ctx context.Context, item profile.Profile, pending *pendingStart) {
	err := s.runtime.Start(ctx, item)

	s.mu.Lock()
	if s.starts[item.ID] == pending {
		delete(s.starts, item.ID)
	}
	if s.profileIndexLocked(item.ID) < 0 {
		s.mu.Unlock()
		return
	}
	operation := pending.operation
	var authErr error
	switch {
	case err != nil && errors.Is(err, context.Canceled):
		if runtimeLockedStatus(s.statuses[item.ID]) {
			s.statuses[item.ID] = StatusStopped
			s.appendLogLocked(item.ID, time.Now(), "disconnected")
		}
	case err != nil:
		s.statuses[item.ID] = StatusError
		s.appendLogLocked(item.ID, time.Now(), err.Error())
	case s.statuses[item.ID] == StatusStarting:
		s.statuses[item.ID] = StatusRunning
		authErr = s.markTailscaleAuthenticatedLocked(item.ID)
	}
	s.mu.Unlock()

	s.notify(Change{ProfileID: item.ID})
	if err != nil && !errors.Is(err, context.Canceled) {
		s.notify(Change{ProfileID: item.ID, Operation: operation, Err: err})
	}
	if authErr != nil {
		s.notify(Change{ProfileID: item.ID, Operation: OperationSave, Err: authErr})
	}
}

func (s *Service) Disconnect(profileID string) bool {
	return s.disconnect(profileID, "")
}

func (s *Service) DisconnectFromTray(profileID string) bool {
	return s.disconnect(profileID, "disconnect requested from tray")
}

func (s *Service) disconnect(profileID, message string) bool {
	s.mu.Lock()
	if s.profileIndexLocked(profileID) < 0 {
		s.mu.Unlock()
		return false
	}
	canceling := false
	if pending := s.starts[profileID]; pending != nil {
		pending.cancel()
		canceling = true
	}
	stopped := false
	if s.runtime != nil {
		stopped = s.runtime.Stop(profileID)
	}
	if stopped || canceling {
		s.statuses[profileID] = StatusStopping
	} else {
		s.statuses[profileID] = StatusStopped
	}
	if message != "" {
		s.appendLogLocked(profileID, time.Now(), message)
	}
	s.mu.Unlock()
	s.notify(Change{ProfileID: profileID})
	return stopped || canceling
}

func (s *Service) DisconnectAll() {
	s.mu.Lock()
	for _, pending := range s.starts {
		pending.cancel()
	}
	for _, item := range s.profiles {
		if s.runtimeLockedLocked(item.ID) {
			s.statuses[item.ID] = StatusStopping
		}
	}
	if s.runtime != nil {
		s.runtime.StopAll()
	}
	profiles := cloneProfiles(s.profiles)
	s.mu.Unlock()
	for _, item := range profiles {
		s.notify(Change{ProfileID: item.ID})
	}
}

func (s *Service) ExitNodes(ctx context.Context, profileID string) ([]connection.ExitNode, error) {
	item, ok := s.Profile(profileID)
	if !ok {
		return nil, ErrProfileNotFound
	}
	if !item.IsTailscale() {
		return nil, connection.ErrNotTailscale
	}
	if s.runtime == nil {
		return nil, ErrRuntimeUnavailable
	}
	nodes, err := s.runtime.ExitNodes(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		s.mu.Lock()
		s.appendLogLocked(profileID, time.Now(), "no Tailscale exit nodes found")
		s.mu.Unlock()
		s.notify(Change{ProfileID: profileID})
	}
	return nodes, nil
}

func (s *Service) Logout(ctx context.Context, profileID string) error {
	s.mu.Lock()
	index := s.profileIndexLocked(profileID)
	if index < 0 {
		s.mu.Unlock()
		return ErrProfileNotFound
	}
	existing := s.profiles[index]
	if !existing.IsTailscale() {
		s.mu.Unlock()
		return connection.ErrNotTailscale
	}
	if s.runtimeLockedLocked(profileID) {
		s.mu.Unlock()
		return ErrRuntimeProfileEdit
	}
	if s.runtime == nil {
		s.mu.Unlock()
		return ErrRuntimeUnavailable
	}

	updated := existing
	updated.TailscaleConfig.AuthKey = ""
	updated.TailscaleConfig.Authenticated = false
	updated.Touch()
	next := cloneProfiles(s.profiles)
	next[index] = updated
	err := s.repository.Save(next)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.profiles = next
	s.mu.Unlock()

	err = s.runtime.Logout(ctx, profileID)
	if err != nil {
		s.mu.Lock()
		restored := cloneProfiles(s.profiles)
		restoreIndex := s.profileIndexLocked(profileID)
		if restoreIndex >= 0 {
			restored[restoreIndex] = existing
			s.profiles = restored
		}
		restoreErr := s.repository.Save(restored)
		s.mu.Unlock()
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore Tailscale authentication state: %w", restoreErr))
		}
		return err
	}

	s.mu.Lock()
	s.appendLogLocked(profileID, time.Now(), "logged out of Tailscale")
	s.mu.Unlock()
	s.notify(Change{ProfileID: profileID})
	return nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.cancel()
		if s.runtime != nil {
			s.shutdownErr = s.runtime.StopAllAndWait(ctx)
		}
	})
	return s.shutdownErr
}

func (s *Service) consumeRuntimeEvents() {
	events := s.runtime.Events()
	for {
		select {
		case <-s.ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			s.handleRuntimeEvent(event)
		}
	}
}

func (s *Service) handleRuntimeEvent(event connection.Event) {
	s.mu.Lock()
	if s.profileIndexLocked(event.ProfileID) < 0 {
		s.mu.Unlock()
		return
	}
	var authErr error
	switch event.Type {
	case connection.EventStarted:
		s.statuses[event.ProfileID] = StatusRunning
		authErr = s.markTailscaleAuthenticatedLocked(event.ProfileID)
	case connection.EventStopped:
		s.statuses[event.ProfileID] = StatusStopped
	case connection.EventError:
		s.statuses[event.ProfileID] = StatusError
	}
	s.appendLogLocked(event.ProfileID, event.At, event.Message)
	s.mu.Unlock()
	s.notify(Change{ProfileID: event.ProfileID, RuntimeEvent: event.Type})
	if authErr != nil {
		s.notify(Change{ProfileID: event.ProfileID, Operation: OperationSave, Err: authErr})
	}
}

func (s *Service) markTailscaleAuthenticatedLocked(profileID string) error {
	index := s.profileIndexLocked(profileID)
	if index < 0 {
		return nil
	}
	existing := s.profiles[index]
	if !existing.IsTailscale() {
		return nil
	}
	if existing.TailscaleConfig.Authenticated && existing.TailscaleConfig.AuthKey == "" {
		return nil
	}
	updated := existing
	updated.TailscaleConfig.AuthKey = ""
	updated.TailscaleConfig.Authenticated = true
	updated.Touch()
	next := cloneProfiles(s.profiles)
	next[index] = updated
	err := s.repository.Save(next)
	if err != nil {
		wrapped := fmt.Errorf("save authenticated Tailscale profile: %w", err)
		s.appendLogLocked(profileID, time.Now(), wrapped.Error())
		return err
	}
	s.profiles = next
	s.appendLogLocked(profileID, time.Now(), "Tailscale authenticated; auth key removed from saved profile")
	return nil
}

func (s *Service) statusLocked(profileID string) Status {
	status := s.statuses[profileID]
	if status == "" {
		status = StatusStopped
	}
	if s.runtime != nil && s.runtime.Running(profileID) && status != StatusStopping {
		return StatusRunning
	}
	return status
}

func (s *Service) runtimeLockedLocked(profileID string) bool {
	if s.runtime != nil && s.runtime.Running(profileID) {
		return true
	}
	return runtimeLockedStatus(s.statuses[profileID])
}

func runtimeLockedStatus(status Status) bool {
	switch status {
	case StatusStarting, StatusRunning, StatusStopping:
		return true
	default:
		return false
	}
}

func (s *Service) runningBindConflictLocked(candidate profile.Profile) error {
	for _, item := range s.profiles {
		if item.ID == candidate.ID || item.BindAddress() != candidate.BindAddress() {
			continue
		}
		if s.runtimeLockedLocked(item.ID) {
			return fmt.Errorf(
				"%w: %s is already used by active profile %q",
				profile.ErrDuplicateBindAddress,
				candidate.BindAddress(),
				item.Name,
			)
		}
	}
	return nil
}

func (s *Service) profileIndexLocked(profileID string) int {
	for index, item := range s.profiles {
		if item.ID == profileID {
			return index
		}
	}
	return -1
}

func (s *Service) appendLogLocked(profileID string, at time.Time, message string) {
	if message == "" {
		return
	}
	entries := s.logs[profileID]
	entries = append(entries, LogEntry{At: at, Message: message})
	if len(entries) > maxLogEntries {
		entries = entries[len(entries)-maxLogEntries:]
	}
	s.logs[profileID] = entries
}

func (s *Service) notify(change Change) {
	select {
	case s.changes <- change:
	default:
	}
}

func duplicateBindError(profiles []profile.Profile) error {
	bind, first, second, ok := profile.DuplicateBindAddress(profiles)
	if !ok {
		return nil
	}
	return fmt.Errorf("%w: %s is used by %q and %q", profile.ErrDuplicateBindAddress, bind, first, second)
}

func prepareImported(imported, existing []profile.Profile) []profile.Profile {
	usedPorts := map[int]bool{}
	for _, item := range existing {
		if item.SocksPort > 0 {
			usedPorts[item.SocksPort] = true
		}
	}

	nextPort := profile.DefaultSocksPort
	now := time.Now().UTC()
	for index := range imported {
		imported[index].ID = profile.NewID()
		imported[index].Normalize()
		if imported[index].IsTailscale() {
			imported[index].TailscaleConfig.Authenticated = false
		}
		if imported[index].Name == "" {
			imported[index].Name = "Imported profile"
		}
		imported[index].CreatedAt = now
		imported[index].UpdatedAt = now

		if imported[index].SocksPort < 1 || imported[index].SocksPort > 65535 || usedPorts[imported[index].SocksPort] {
			port, ok := nextAvailableImportPort(usedPorts, nextPort)
			if ok {
				imported[index].SocksPort = port
				nextPort = port + 1
			} else {
				imported[index].SocksPort = profile.DefaultSocksPort
			}
		}
		usedPorts[imported[index].SocksPort] = true
	}
	return imported
}

func nextAvailableImportPort(usedPorts map[int]bool, start int) (int, bool) {
	if start < 1 || start > 65535 {
		start = profile.DefaultSocksPort
	}
	for port := start; port <= 65535; port++ {
		if !usedPorts[port] {
			return port, true
		}
	}
	for port := 1; port < start; port++ {
		if !usedPorts[port] {
			return port, true
		}
	}
	return 0, false
}

func cloneProfiles(profiles []profile.Profile) []profile.Profile {
	return append([]profile.Profile(nil), profiles...)
}
