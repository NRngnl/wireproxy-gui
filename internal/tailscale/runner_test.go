package tailscale

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"example.com/wireproxy-gui/internal/connection"
	"example.com/wireproxy-gui/internal/profile"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/types/key"
)

var errTestUp = errors.New("up failed")

func TestStartRejectsWireGuardProfile(t *testing.T) {
	runner := NewRunner()
	runner.listen = func(_, _ string) (net.Listener, error) {
		t.Fatal("Start should reject before listening")
		return nil, nil
	}

	err := runner.Start(context.Background(), profile.New("wg", "", 1080))
	if !errors.Is(err, ErrNotTailscale) {
		t.Fatalf("expected ErrNotTailscale, got %v", err)
	}
}

func TestStartBindsBeforeNodeUp(t *testing.T) {
	runner := NewRunner()
	runner.stateDir = t.TempDir()
	listener := newFakeListener()
	listened := false
	upSawListen := false
	node := &fakeTSNode{
		client: &fakeLocalClient{prefs: ipn.NewPrefs(), status: &ipnstate.Status{}},
		upFunc: func() {
			upSawListen = listened
		},
	}
	runner.listen = func(network, address string) (net.Listener, error) {
		if network != "tcp" || address != "127.0.0.1:18080" {
			t.Fatalf("listen = %s %s, want tcp 127.0.0.1:18080", network, address)
		}
		listened = true
		return listener, nil
	}
	runner.newNode = func(_ profile.Profile, _ string, _ func(string, ...any)) (tsNode, error) {
		return node, nil
	}
	runner.serveSocks5 = func(ctx context.Context, _ tsNode, listener net.Listener) error {
		<-ctx.Done()
		_ = listener.Close()
		return ctx.Err()
	}
	p := profile.NewTailscale("tailnet", 18080)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !upSawListen {
		t.Fatal("node.Up ran before the SOCKS5 listener was bound")
	}
	if !runner.Running(p.ID) {
		t.Fatal("profile should be running after Start")
	}

	err = runner.StopAllAndWait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !listener.isClosed() {
		t.Fatal("listener should be closed after StopAllAndWait")
	}
	if node.closeCalls == 0 {
		t.Fatal("node should be closed after StopAllAndWait")
	}
}

func TestStartCleansUpWhenNodeUpFails(t *testing.T) {
	runner := NewRunner()
	listener := newFakeListener()
	node := &fakeTSNode{
		upErr:  errTestUp,
		client: &fakeLocalClient{prefs: ipn.NewPrefs(), status: &ipnstate.Status{}},
	}
	runner.listen = func(_, _ string) (net.Listener, error) {
		return listener, nil
	}
	runner.newNode = func(_ profile.Profile, _ string, _ func(string, ...any)) (tsNode, error) {
		return node, nil
	}
	p := profile.NewTailscale("tailnet", 18081)

	err := runner.Start(context.Background(), p)
	if !errors.Is(err, errTestUp) {
		t.Fatalf("expected wrapped up error, got %v", err)
	}
	if runner.Running(p.ID) {
		t.Fatal("profile should not remain running after failed startup")
	}
	if !listener.isClosed() {
		t.Fatal("listener should be closed after failed startup")
	}
	if node.closeCalls != 1 {
		t.Fatalf("node close calls = %d, want 1", node.closeCalls)
	}
}

func TestStartLogsMachineAuthApprovalWaitWhileNodeUpIsPending(t *testing.T) {
	runner := NewRunner()
	listener := newFakeListener()
	upWait := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() {
		unblock.Do(func() {
			close(upWait)
		})
	})
	client := &fakeLocalClient{
		prefs: ipn.NewPrefs(),
		status: &ipnstate.Status{
			BackendState: ipn.NeedsMachineAuth.String(),
		},
	}
	node := &fakeTSNode{
		client: client,
		upWait: upWait,
		status: &ipnstate.Status{
			TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")},
		},
	}
	runner.listen = func(_, _ string) (net.Listener, error) {
		return listener, nil
	}
	runner.newNode = func(_ profile.Profile, _ string, _ func(string, ...any)) (tsNode, error) {
		return node, nil
	}
	runner.serveSocks5 = func(ctx context.Context, _ tsNode, listener net.Listener) error {
		<-ctx.Done()
		_ = listener.Close()
		return ctx.Err()
	}
	p := profile.NewTailscale("tailnet", 18086)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.Start(context.Background(), p)
	}()

	waitForRunnerLog(t, runner.Events(), tailscaleMachineAuthMessage)
	unblock.Do(func() {
		close(upWait)
	})
	waitForRunnerLog(t, runner.Events(), tailscaleDeviceApprovedMessage)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after node.Up completed")
	}

	err := runner.StopAllAndWait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestStopAfterNodeReadyBeforeStartedDoesNotEmitStarted(t *testing.T) {
	runner := NewRunner()
	listener := newFakeListener()
	p := profile.NewTailscale("tailnet", 18085)
	client := &fakeLocalClient{
		prefs:  ipn.NewPrefs(),
		status: &ipnstate.Status{},
		editFunc: func() {
			if !runner.Stop(p.ID) {
				t.Fatal("expected Stop to cancel reserved profile")
			}
		},
	}
	node := &fakeTSNode{client: client}
	runner.listen = func(_, _ string) (net.Listener, error) {
		return listener, nil
	}
	runner.newNode = func(_ profile.Profile, _ string, _ func(string, ...any)) (tsNode, error) {
		return node, nil
	}

	err := runner.Start(context.Background(), p)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if runner.Running(p.ID) {
		t.Fatal("profile should not remain running after canceled startup")
	}
	if !listener.isClosed() {
		t.Fatal("listener should be closed after canceled startup")
	}
	if node.closeCalls != 1 {
		t.Fatalf("node close calls = %d, want 1", node.closeCalls)
	}
	assertNoStartedEvent(t, runner.Events())
}

func TestNewTSNetNodeUsesProfileConfiguration(t *testing.T) {
	p := profile.NewTailscale("tailnet", 18082)
	p.ID = "profile-id"
	p.TailscaleConfig = profile.TailscaleConfig{
		Hostname:   "ts-host",
		AuthKey:    "auth",
		ControlURL: "https://control.example.com",
		Ephemeral:  true,
	}
	stateDir := t.TempDir()

	node, err := newTSNetNode(p, stateDir, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	real, ok := node.(*realNode)
	if !ok {
		t.Fatalf("node = %T, want *realNode", node)
	}
	if got, want := real.server.Dir, filepath.Join(stateDir, p.ID); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
	if real.server.Hostname != "ts-host" {
		t.Fatalf("Hostname = %q, want ts-host", real.server.Hostname)
	}
	if real.server.AuthKey != "auth" {
		t.Fatalf("AuthKey = %q, want auth", real.server.AuthKey)
	}
	if real.server.ControlURL != "https://control.example.com" {
		t.Fatalf("ControlURL = %q, want https://control.example.com", real.server.ControlURL)
	}
	if !real.server.Ephemeral {
		t.Fatal("Ephemeral should be true")
	}
	if real.server.UserLogf == nil {
		t.Fatal("UserLogf should be set")
	}
}

func TestNewTSNetNodeDefaultsHostnameFromProfileName(t *testing.T) {
	p := profile.NewTailscale("tailnet", 18083)

	node, err := newTSNetNode(p, t.TempDir(), func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	real := node.(*realNode)
	if real.server.Hostname != "tailnet" {
		t.Fatalf("Hostname = %q, want tailnet", real.server.Hostname)
	}
}

func TestTailscaleAuthStateMessages(t *testing.T) {
	tests := []struct {
		name       string
		status     *ipnstate.Status
		want       string
		wantKey    string
		wantNoText bool
	}{
		{
			name:    "login URL",
			status:  &ipnstate.Status{BackendState: ipn.NeedsLogin.String(), AuthURL: "https://login.tailscale.com/a/abc"},
			want:    "Tailscale login required: open https://login.tailscale.com/a/abc",
			wantKey: "login-url|https://login.tailscale.com/a/abc",
		},
		{
			name:    "login waiting",
			status:  &ipnstate.Status{BackendState: ipn.NeedsLogin.String()},
			want:    tailscaleLoginWaitingMessage,
			wantKey: "NeedsLogin",
		},
		{
			name:    "machine auth",
			status:  &ipnstate.Status{BackendState: ipn.NeedsMachineAuth.String()},
			want:    tailscaleMachineAuthMessage,
			wantKey: "NeedsMachineAuth",
		},
		{
			name:    "running",
			status:  &ipnstate.Status{BackendState: ipn.Running.String()},
			want:    tailscaleDeviceApprovedMessage,
			wantKey: "Running",
		},
		{
			name:       "other state",
			status:     &ipnstate.Status{BackendState: ipn.Stopped.String()},
			wantKey:    "Stopped",
			wantNoText: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, key := tailscaleAuthStateMessage(tt.status)
			if key != tt.wantKey {
				t.Fatalf("key = %q, want %q", key, tt.wantKey)
			}
			if tt.wantNoText {
				if got != "" {
					t.Fatalf("message = %q, want empty", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("message = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTailscaleUserLogMessageRewritesAuthURL(t *testing.T) {
	got := tailscaleUserLogMessage("To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/abc")
	want := "Tailscale login required: open https://login.tailscale.com/a/abc"
	if got != want {
		t.Fatalf("rewritten log = %q, want %q", got, want)
	}

	if got := tailscaleUserLogMessage("AuthLoop: state is Running; done"); got != "" {
		t.Fatalf("AuthLoop log should be suppressed, got %q", got)
	}
}

func TestTailscaleAuthLogEmitterDebouncesTransientMachineAuth(t *testing.T) {
	var logs []string
	emitter := newTailscaleAuthLogEmitter(func(message string) {
		logs = append(logs, message)
	})
	now := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	machineAuth := tailscaleAuthStateNotice(&ipnstate.Status{
		BackendState: ipn.NeedsMachineAuth.String(),
	})
	emitter.emitNoticeAt(machineAuth, now)
	emitter.emitNoticeAt(tailscaleAuthStateNotice(&ipnstate.Status{
		BackendState: ipn.Running.String(),
	}), now.Add(time.Second))

	if len(logs) != 0 {
		t.Fatalf("transient machine auth should not log approval request: %#v", logs)
	}
}

func TestTailscaleAuthLogEmitterResetsPendingMachineAuthAfterStateChange(t *testing.T) {
	var logs []string
	emitter := newTailscaleAuthLogEmitter(func(message string) {
		logs = append(logs, message)
	})
	now := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	machineAuth := tailscaleAuthStateNotice(&ipnstate.Status{
		BackendState: ipn.NeedsMachineAuth.String(),
	})
	running := tailscaleAuthStateNotice(&ipnstate.Status{
		BackendState: ipn.Running.String(),
	})

	emitter.emitNoticeAt(machineAuth, now)
	emitter.emitNoticeAt(running, now.Add(time.Second))
	emitter.emitNoticeAt(machineAuth, now.Add(tailscaleStatusNoticeDelay+time.Second))
	emitter.emitNoticeAt(running, now.Add(tailscaleStatusNoticeDelay+1500*time.Millisecond))

	if len(logs) != 0 {
		t.Fatalf("new transient machine auth should not inherit old debounce timer: %#v", logs)
	}
}

func TestTailscaleAuthLogEmitterLogsPersistentMachineAuthAndApproval(t *testing.T) {
	var logs []string
	emitter := newTailscaleAuthLogEmitter(func(message string) {
		logs = append(logs, message)
	})
	now := time.Date(2026, 6, 21, 1, 2, 3, 0, time.UTC)
	machineAuth := tailscaleAuthStateNotice(&ipnstate.Status{
		BackendState: ipn.NeedsMachineAuth.String(),
	})
	emitter.emitNoticeAt(machineAuth, now)
	emitter.emitNoticeAt(machineAuth, now.Add(tailscaleStatusNoticeDelay))
	emitter.emitNoticeAt(tailscaleAuthStateNotice(&ipnstate.Status{
		BackendState: ipn.Running.String(),
	}), now.Add(tailscaleStatusNoticeDelay+time.Second))

	want := []string{
		tailscaleMachineAuthMessage,
		tailscaleDeviceApprovedMessage,
	}
	if !reflect.DeepEqual(logs, want) {
		t.Fatalf("logs = %#v, want %#v", logs, want)
	}
}

func TestTailscaleAuthLogEmitterDeduplicatesLoginURL(t *testing.T) {
	var logs []string
	emitter := newTailscaleAuthLogEmitter(func(message string) {
		logs = append(logs, message)
	})
	emitter.emitUserLog("To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/abc")
	emitter.emitStatus(&ipnstate.Status{
		BackendState: ipn.NoState.String(),
		AuthURL:      "https://login.tailscale.com/a/abc",
	})

	want := []string{"Tailscale login required: open https://login.tailscale.com/a/abc"}
	if !reflect.DeepEqual(logs, want) {
		t.Fatalf("logs = %#v, want %#v", logs, want)
	}
}

func TestTailscaleAuthLogEmitterRewritesSavedStateAuthKeyNotice(t *testing.T) {
	var logs []string
	emitter := newTailscaleAuthLogEmitter(func(message string) {
		logs = append(logs, message)
	})
	emitter.emitUserLog("Authkey is set; but state is Running. Ignoring authkey. Re-run with TSNET_FORCE_LOGIN=1 to force use of authkey.")

	want := []string{tailscaleAuthKeyIgnoredMessage}
	if !reflect.DeepEqual(logs, want) {
		t.Fatalf("logs = %#v, want %#v", logs, want)
	}
}

func TestRealNodeCloseBeforeUpReturnsError(t *testing.T) {
	node := &realNode{server: &tsnet.Server{}}

	err := node.Close()
	if err == nil {
		t.Fatal("expected close error for unstarted server")
	}
}

func TestConfigureExitNodeUsesAutomaticExitNode(t *testing.T) {
	client := &fakeLocalClient{prefs: ipn.NewPrefs(), status: &ipnstate.Status{}}
	node := &fakeTSNode{client: client}

	err := configureExitNode(context.Background(), node, profile.TailscaleConfig{
		AutoExitNode:           true,
		ExitNodeAllowLANAccess: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := client.edit
	if got == nil {
		t.Fatal("expected prefs edit")
	}
	if got.AutoExitNode != ipn.AnyExitNode {
		t.Fatalf("AutoExitNode = %q, want %q", got.AutoExitNode, ipn.AnyExitNode)
	}
	if !got.ExitNodeAllowLANAccess {
		t.Fatal("ExitNodeAllowLANAccess should be set")
	}
	if !got.ExitNodeIDSet || !got.ExitNodeIPSet || !got.AutoExitNodeSet || !got.ExitNodeAllowLANAccessSet {
		t.Fatalf("unexpected prefs mask: %#v", got)
	}
	if client.statusCalls != 1 {
		t.Fatalf("Status calls = %d, want 1 when Start status is nil", client.statusCalls)
	}
}

func TestConfigureExitNodeUsesSpecificExitNodeIP(t *testing.T) {
	client := &fakeLocalClient{prefs: ipn.NewPrefs(), status: &ipnstate.Status{}}
	node := &fakeTSNode{client: client}

	err := configureExitNode(context.Background(), node, profile.TailscaleConfig{
		ExitNode: "100.64.0.1",
	}, &ipnstate.Status{})
	if err != nil {
		t.Fatal(err)
	}

	got := client.edit
	wantIP := netip.MustParseAddr("100.64.0.1")
	if got == nil || got.ExitNodeIP != wantIP {
		t.Fatalf("ExitNodeIP = %v, want %v", got, wantIP)
	}
	if got.AutoExitNode != "" {
		t.Fatalf("AutoExitNode = %q, want empty", got.AutoExitNode)
	}
	if client.statusCalls != 0 {
		t.Fatalf("Status calls = %d, want 0 when Start status is provided", client.statusCalls)
	}
}

func TestConfigureExitNodeUsesSpecificExitNodeID(t *testing.T) {
	status := &ipnstate.Status{
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			{}: {
				ID:             tailcfg.StableNodeID("stable-exit"),
				ExitNodeOption: true,
			},
		},
	}
	client := &fakeLocalClient{prefs: ipn.NewPrefs(), status: status}
	node := &fakeTSNode{client: client}

	err := configureExitNode(context.Background(), node, profile.TailscaleConfig{
		ExitNode: "stable-exit",
	}, status)
	if err != nil {
		t.Fatal(err)
	}

	got := client.edit
	if got == nil || got.ExitNodeID != tailcfg.StableNodeID("stable-exit") {
		t.Fatalf("ExitNodeID = %v, want stable-exit", got)
	}
	if got.ExitNodeIP.IsValid() {
		t.Fatalf("ExitNodeIP = %v, want invalid", got.ExitNodeIP)
	}
}

func TestConfigureExitNodeClearsExistingExitNode(t *testing.T) {
	prefs := ipn.NewPrefs()
	prefs.AutoExitNode = ipn.AnyExitNode
	prefs.ExitNodeIP = netip.MustParseAddr("100.64.0.1")
	prefs.ExitNodeAllowLANAccess = true
	client := &fakeLocalClient{prefs: prefs, status: &ipnstate.Status{}}
	node := &fakeTSNode{client: client}

	err := configureExitNode(context.Background(), node, profile.TailscaleConfig{}, &ipnstate.Status{})
	if err != nil {
		t.Fatal(err)
	}

	got := client.edit
	if got == nil {
		t.Fatal("expected prefs edit")
	}
	if got.AutoExitNode != "" {
		t.Fatalf("AutoExitNode = %q, want empty", got.AutoExitNode)
	}
	if got.ExitNodeIP.IsValid() {
		t.Fatalf("ExitNodeIP = %v, want invalid", got.ExitNodeIP)
	}
	if got.ExitNodeAllowLANAccess {
		t.Fatal("ExitNodeAllowLANAccess should be cleared")
	}
}

func TestExitNodesReturnsOnlineTailnetExitNodeOptions(t *testing.T) {
	runner := NewRunner()
	status := &ipnstate.Status{
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			{}: {
				ID:             tailcfg.StableNodeID("stable-exit"),
				DNSName:        "exit-a.tailnet.ts.net.",
				HostName:       "exit-host",
				TailscaleIPs:   []netip.Addr{netip.MustParseAddr("100.64.0.1")},
				Online:         true,
				ExitNodeOption: true,
			},
		},
	}
	p := profile.NewTailscale("tailnet", 18084)
	proc := newProcess(func() {})
	proc.setNode(&fakeTSNode{client: &fakeLocalClient{prefs: ipn.NewPrefs(), status: status}})
	runner.procs[p.ID] = proc

	nodes, err := runner.ExitNodes(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}

	want := []connection.ExitNode{
		{
			ID:           "stable-exit",
			Name:         "exit-a",
			Online:       true,
			TailscaleIPs: []string{"100.64.0.1"},
		},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("ExitNodes = %#v, want %#v", nodes, want)
	}
}

func TestExitNodesRequiresRunningProfile(t *testing.T) {
	runner := NewRunner()

	_, err := runner.ExitNodes(context.Background(), "missing")
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestUpdateExitNodeAppliesPrefsForRunningProfile(t *testing.T) {
	runner := NewRunner()
	status := &ipnstate.Status{
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			{}: {
				ID:             tailcfg.StableNodeID("stable-exit"),
				ExitNodeOption: true,
			},
		},
	}
	client := &fakeLocalClient{prefs: ipn.NewPrefs(), status: status}
	proc := newProcess(func() {})
	proc.setNode(&fakeTSNode{client: client})
	p := profile.NewTailscale("tailnet", 18084)
	runner.procs[p.ID] = proc

	err := runner.UpdateExitNode(context.Background(), p.ID, profile.TailscaleConfig{
		ExitNode:               "stable-exit",
		ExitNodeAllowLANAccess: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := client.edit
	if got == nil {
		t.Fatal("expected prefs edit")
	}
	if got.ExitNodeID != tailcfg.StableNodeID("stable-exit") {
		t.Fatalf("ExitNodeID = %q, want stable-exit", got.ExitNodeID)
	}
	if !got.ExitNodeAllowLANAccess {
		t.Fatal("ExitNodeAllowLANAccess should be true")
	}
}

func TestUpdateExitNodeRequiresRunningProfile(t *testing.T) {
	runner := NewRunner()

	err := runner.UpdateExitNode(context.Background(), "missing", profile.TailscaleConfig{})
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning, got %v", err)
	}
}

func TestLogoutRemovesProfileStateDirectory(t *testing.T) {
	runner := NewRunner()
	runner.stateDir = t.TempDir()
	profileID := "tailnet-profile"
	statePath := filepath.Join(runner.stateDir, profileID)
	err := os.MkdirAll(statePath, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(statePath, "state.json"), []byte("state"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = runner.Logout(context.Background(), profileID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory should be removed, stat error = %v", err)
	}
	if _, err := os.Stat(runner.stateDir); err != nil {
		t.Fatalf("state root should remain, stat error = %v", err)
	}
}

func TestLogoutRejectsRunningProfile(t *testing.T) {
	runner := NewRunner()
	p := profile.NewTailscale("tailnet", 18084)
	runner.procs[p.ID] = newProcess(func() {})

	err := runner.Logout(context.Background(), p.ID)
	if !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("expected ErrAlreadyConnected, got %v", err)
	}
}

func TestLogoutRejectsUnsafeProfileID(t *testing.T) {
	runner := NewRunner()
	runner.stateDir = t.TempDir()

	err := runner.Logout(context.Background(), "../outside")
	if !errors.Is(err, ErrInvalidProfileID) {
		t.Fatalf("expected ErrInvalidProfileID, got %v", err)
	}
}

func TestNewTSNetNodeRejectsUnsafeProfileID(t *testing.T) {
	p := profile.NewTailscale("tailnet", 18084)
	p.ID = "../outside"

	_, err := newTSNetNode(p, t.TempDir(), func(string, ...any) {})
	if !errors.Is(err, ErrInvalidProfileID) {
		t.Fatalf("expected ErrInvalidProfileID, got %v", err)
	}
}

func TestRunSocks5PreservesHostnameDestination(t *testing.T) {
	runner := NewRunner()
	listener := newPipeListener()
	defer listener.Close()

	type dialRequest struct {
		network string
		address string
	}
	dialed := make(chan dialRequest, 1)
	node := &fakeTSNode{
		dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed <- dialRequest{network: network, address: address}
			client, server := net.Pipe()
			go func() {
				<-ctx.Done()
				_ = server.Close()
			}()
			return client, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.runSocks5(ctx, node, listener)
	}()

	conn := listener.Dial()
	defer conn.Close()
	err := conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	_, err = io.ReadFull(conn, reply)
	if err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("SOCKS greeting reply = %#v, want version 5 no auth", reply)
	}

	host := []byte("example.com")
	request := append([]byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}, host...)
	request = append(request, 0x01, 0xbb)
	_, err = conn.Write(request)
	if err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 10)
	_, err = io.ReadFull(conn, response)
	if err != nil {
		t.Fatal(err)
	}
	if response[0] != 0x05 || response[1] != 0x00 {
		t.Fatalf("SOCKS connect reply = %#v, want success", response)
	}

	select {
	case got := <-dialed:
		if got.network != "tcp" {
			t.Fatalf("network = %q, want tcp", got.network)
		}
		if got.address != "example.com:443" {
			t.Fatalf("dial address = %q, want example.com:443", got.address)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("node was not dialed")
	}

	cancel()
	_ = conn.Close()
	_ = listener.Close()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("runSocks5 error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSocks5 did not stop")
	}
}

type fakeTSNode struct {
	upErr    error
	upFunc   func()
	upWait   <-chan struct{}
	status   *ipnstate.Status
	client   localClient
	dialFunc func(context.Context, string, string) (net.Conn, error)

	closeCalls int
}

func (n *fakeTSNode) Up(ctx context.Context) (*ipnstate.Status, error) {
	if n.upFunc != nil {
		n.upFunc()
	}
	if n.upWait != nil {
		select {
		case <-n.upWait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if n.upErr != nil {
		return nil, n.upErr
	}
	if n.status != nil {
		return n.status, nil
	}
	return &ipnstate.Status{}, nil
}

func (n *fakeTSNode) LocalClient() (localClient, error) {
	return n.client, nil
}

func (n *fakeTSNode) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if n.dialFunc != nil {
		return n.dialFunc(ctx, network, address)
	}
	return nil, errors.New("unexpected dial")
}

func (n *fakeTSNode) Close() error {
	n.closeCalls++
	return nil
}

type fakeLocalClient struct {
	prefs       *ipn.Prefs
	edit        *ipn.MaskedPrefs
	status      *ipnstate.Status
	statusCalls int
	editFunc    func()
}

func (c *fakeLocalClient) GetPrefs(context.Context) (*ipn.Prefs, error) {
	if c.prefs == nil {
		return ipn.NewPrefs(), nil
	}
	return c.prefs.Clone(), nil
}

func (c *fakeLocalClient) EditPrefs(_ context.Context, prefs *ipn.MaskedPrefs) (*ipn.Prefs, error) {
	copied := *prefs
	c.edit = &copied
	c.prefs = prefs.Prefs.Clone()
	if c.editFunc != nil {
		c.editFunc()
	}
	return c.prefs, nil
}

func (c *fakeLocalClient) Status(context.Context) (*ipnstate.Status, error) {
	c.statusCalls++
	if c.status == nil {
		return &ipnstate.Status{}, nil
	}
	return c.status, nil
}

type fakeListener struct {
	closed chan struct{}
	once   sync.Once
}

func newFakeListener() *fakeListener {
	return &fakeListener{closed: make(chan struct{})}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *fakeListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *fakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func (l *fakeListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{
		conns:  make(chan net.Conn, 1),
		closed: make(chan struct{}),
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func (l *pipeListener) Dial() net.Conn {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
	case <-l.closed:
		_ = server.Close()
	}
	return client
}

func assertNoStartedEvent(t *testing.T, events <-chan Event) {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Type == EventStarted {
				t.Fatalf("unexpected started event: %#v", event)
			}
		default:
			return
		}
	}
}

func waitForRunnerLog(t *testing.T, events <-chan Event, message string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == EventLog && event.Message == message {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for log %q", message)
		}
	}
}
