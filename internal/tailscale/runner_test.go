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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/types/key"
)

var (
	errTestUp                 = errors.New("up failed")
	errUnexpectedDial         = errors.New("unexpected dial")
	errUnexpectedListen       = errors.New("unexpected listen")
	errUnexpectedListenPacket = errors.New("unexpected listen packet")
	errForwardListen          = errors.New("forward listen failed")
	errTestTCPAcceptFailed    = errors.New("boom: accept failed")
	errTestUDPReadFromFailed  = errors.New("boom: read from failed")
)

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

func TestStartPortForwardsBindsEachConfiguredRule(t *testing.T) {
	runner := NewRunner()
	runner.stateDir = t.TempDir()
	listener := newFakeListener()

	type listenCall struct {
		network string
		addr    string
	}
	var mu sync.Mutex
	var listenCalls []listenCall
	var packetCalls []listenCall

	forwardListener := newFakeListener()
	forwardPacketConn := &fakePacketConn{closed: make(chan struct{})}
	nodeStatus := &ipnstate.Status{
		TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.5")},
	}
	node := &fakeTSNode{
		status: nodeStatus,
		client: &fakeLocalClient{prefs: ipn.NewPrefs(), status: &ipnstate.Status{}},
		listenFunc: func(network, addr string) (net.Listener, error) {
			mu.Lock()
			listenCalls = append(listenCalls, listenCall{network: network, addr: addr})
			mu.Unlock()
			return forwardListener, nil
		},
		listenPacketFunc: func(network, addr string) (net.PacketConn, error) {
			mu.Lock()
			packetCalls = append(packetCalls, listenCall{network: network, addr: addr})
			mu.Unlock()
			return forwardPacketConn, nil
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

	p := profile.NewTailscale("tailnet", 18090)
	p.TailscaleConfig.PortForwards = []profile.PortForward{
		{ListenPort: 9001, Protocol: profile.PortForwardTCP, TargetAddr: "127.0.0.1:9101"},
		{ListenPort: 9002, Protocol: profile.PortForwardUDP, TargetAddr: "127.0.0.1:9102"},
	}

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.Running(p.ID) {
		t.Fatal("profile should be running after Start")
	}

	mu.Lock()
	gotListen := append([]listenCall{}, listenCalls...)
	gotPacket := append([]listenCall{}, packetCalls...)
	mu.Unlock()

	if len(gotListen) != 1 || gotListen[0].network != "tcp" || gotListen[0].addr != ":9001" {
		t.Fatalf("Listen calls = %#v, want one call for tcp :9001", gotListen)
	}
	// Regression test for the bug where UDP port forwards used a bare
	// ":port" address: tsnet.Server.ListenPacket's documented contract
	// requires addr to be of the form "ip:port" with a valid IP (unlike
	// tsnet.Server.Listen, which explicitly documents that a bare ":port"
	// matches the node's own local IP for TCP). A bare ":9002" would fail
	// at runtime against the real tsnet.Server.ListenPacket with "address
	// must be a valid IP", even though the fake test double here doesn't
	// replicate that validation.
	if len(gotPacket) != 1 || gotPacket[0].network != "udp" {
		t.Fatalf("ListenPacket calls = %#v, want one call for udp", gotPacket)
	}
	wantPacketAddr := "100.64.0.5:9002"
	if gotPacket[0].addr != wantPacketAddr {
		t.Fatalf("ListenPacket addr = %q, want %q (not a bare \":port\")", gotPacket[0].addr, wantPacketAddr)
	}
	if strings.HasPrefix(gotPacket[0].addr, ":") {
		t.Fatalf("ListenPacket addr = %q, must not be a bare \":port\" form", gotPacket[0].addr)
	}

	err = runner.StopAllAndWait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestPortForwardBindFailureAbortsStart(t *testing.T) {
	runner := NewRunner()
	runner.stateDir = t.TempDir()
	listener := newFakeListener()
	node := &fakeTSNode{
		client: &fakeLocalClient{prefs: ipn.NewPrefs(), status: &ipnstate.Status{}},
		listenFunc: func(_, _ string) (net.Listener, error) {
			return nil, errForwardListen
		},
	}
	runner.listen = func(_, _ string) (net.Listener, error) {
		return listener, nil
	}
	runner.newNode = func(_ profile.Profile, _ string, _ func(string, ...any)) (tsNode, error) {
		return node, nil
	}
	p := profile.NewTailscale("tailnet", 18091)
	p.TailscaleConfig.PortForwards = []profile.PortForward{
		{ListenPort: 9003, Protocol: profile.PortForwardTCP, TargetAddr: "127.0.0.1:9103"},
	}

	err := runner.Start(context.Background(), p)
	if !errors.Is(err, errForwardListen) {
		t.Fatalf("expected wrapped forward listen error, got %v", err)
	}
	if runner.Running(p.ID) {
		t.Fatal("profile should not remain running after failed port forward bind")
	}
	if !listener.isClosed() {
		t.Fatal("SOCKS5 listener should be closed after failed port forward bind")
	}
	if node.closeCalls != 1 {
		t.Fatalf("node close calls = %d, want 1", node.closeCalls)
	}
}

func TestRelayTCPForwardRoundTripsPayload(t *testing.T) {
	runner := NewRunner()
	bg := context.Background()
	var lc net.ListenConfig

	fwdListener, err := lc.Listen(bg, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer fwdListener.Close()

	targetListener, err := lc.Listen(bg, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()

	targetAccepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := targetListener.Accept()
		if acceptErr != nil {
			return
		}
		targetAccepted <- conn
	}()

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	rule := profile.PortForward{
		ListenPort: 0,
		Protocol:   profile.PortForwardTCP,
		TargetAddr: targetListener.Addr().String(),
	}
	go runner.relayTCPForward(ctx, nil, fwdListener, rule, "profile-tcp-roundtrip", "TCP Roundtrip Profile")

	var dialer net.Dialer
	clientConn, err := dialer.DialContext(ctx, "tcp", fwdListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	err = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("tcp-forward-payload")
	_, err = clientConn.Write(payload)
	if err != nil {
		t.Fatal(err)
	}

	var targetConn net.Conn
	select {
	case targetConn = <-targetAccepted:
	case <-time.After(2 * time.Second):
		t.Fatal("target listener did not receive a connection")
	}
	defer targetConn.Close()
	err = targetConn.SetDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, len(payload))
	_, err = io.ReadFull(targetConn, buf)
	if err != nil {
		t.Fatalf("target did not receive relayed payload: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("target received %q, want %q", buf, payload)
	}

	reply := []byte("tcp-forward-reply")
	_, err = targetConn.Write(reply)
	if err != nil {
		t.Fatal(err)
	}
	replyBuf := make([]byte, len(reply))
	_, err = io.ReadFull(clientConn, replyBuf)
	if err != nil {
		t.Fatalf("client did not receive relayed reply: %v", err)
	}
	if string(replyBuf) != string(reply) {
		t.Fatalf("client received %q, want %q", replyBuf, reply)
	}
}

func TestRelayUDPForwardRoundTripsPayload(t *testing.T) {
	runner := NewRunner()
	bg := context.Background()

	fwdConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer fwdConn.Close()

	targetConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer targetConn.Close()

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	rule := profile.PortForward{
		ListenPort: 0,
		Protocol:   profile.PortForwardUDP,
		TargetAddr: targetConn.LocalAddr().String(),
	}
	go runner.relayUDPForward(ctx, nil, fwdConn, rule, "profile-udp-roundtrip", "UDP Roundtrip Profile")

	sourceConn, err := net.DialUDP("udp", nil, fwdConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer sourceConn.Close()

	payload := []byte("udp-forward-payload")
	_, err = sourceConn.Write(payload)
	if err != nil {
		t.Fatal(err)
	}

	err = targetConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, relaySrcAddr, err := targetConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("target did not receive relayed datagram: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Fatalf("target received %q, want %q", buf[:n], payload)
	}

	reply := []byte("udp-forward-reply")
	_, err = targetConn.WriteToUDP(reply, relaySrcAddr)
	if err != nil {
		t.Fatal(err)
	}

	err = sourceConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	replyBuf := make([]byte, 64)
	rn, err := sourceConn.Read(replyBuf)
	if err != nil {
		t.Fatalf("source did not receive relayed reply: %v", err)
	}
	if string(replyBuf[:rn]) != string(reply) {
		t.Fatalf("source received %q, want %q", replyBuf[:rn], reply)
	}
}

// TestRelayTCPForwardOperationalErrorIncludesProfileID is a regression test
// for the bug where relayTCPForward's/relayUDPForward's operational error
// events left ProfileID/ProfileName empty, causing
// Service.handleRuntimeEvent to silently drop every port-forward
// operational error event (it discards any event whose ProfileID does not
// match a known profile). It forces a real
// (non-net.ErrClosed) Accept() failure and asserts the emitted EventError
// carries the profile identity threaded through startPortForwards.
func TestRelayTCPForwardOperationalErrorIncludesProfileID(t *testing.T) {
	runner := NewRunner()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fwdListener := &erroringListener{err: errTestTCPAcceptFailed}
	rule := profile.PortForward{
		ListenPort: 9005,
		Protocol:   profile.PortForwardTCP,
		TargetAddr: "127.0.0.1:9105",
	}
	const profileID = "profile-tcp-operational-error"
	const profileName = "TCP Operational Error Profile"

	go runner.relayTCPForward(ctx, nil, fwdListener, rule, profileID, profileName)

	select {
	case event := <-runner.Events():
		if event.Type != EventError {
			t.Fatalf("event type = %v, want EventError", event.Type)
		}
		if event.ProfileID != profileID {
			t.Fatalf("event.ProfileID = %q, want %q", event.ProfileID, profileID)
		}
		if event.ProfileName != profileName {
			t.Fatalf("event.ProfileName = %q, want %q", event.ProfileName, profileName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forward error event")
	}
}

// TestRelayUDPForwardOperationalErrorIncludesProfileID forces a real
// (non-net.ErrClosed) ReadFrom() failure and asserts the emitted
// EventError carries the profile identity threaded through
// startPortForwards.
func TestRelayUDPForwardOperationalErrorIncludesProfileID(t *testing.T) {
	runner := NewRunner()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	fwdConn := &erroringPacketConn{err: errTestUDPReadFromFailed}
	rule := profile.PortForward{
		ListenPort: 9006,
		Protocol:   profile.PortForwardUDP,
		TargetAddr: "127.0.0.1:9106",
	}
	const profileID = "profile-udp-operational-error"
	const profileName = "UDP Operational Error Profile"

	go runner.relayUDPForward(ctx, nil, fwdConn, rule, profileID, profileName)

	select {
	case event := <-runner.Events():
		if event.Type != EventError {
			t.Fatalf("event type = %v, want EventError", event.Type)
		}
		if event.ProfileID != profileID {
			t.Fatalf("event.ProfileID = %q, want %q", event.ProfileID, profileID)
		}
		if event.ProfileName != profileName {
			t.Fatalf("event.ProfileName = %q, want %q", event.ProfileName, profileName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forward error event")
	}
}

func TestProcessCloseClosesForwardListeners(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	proc := newProcess(cancel)
	recorder := &closeRecorder{}

	if !proc.addForwardCloser(recorder) {
		t.Fatal("addForwardCloser should succeed on an open process")
	}

	proc.close()

	if !recorder.closed {
		t.Fatal("expected process.close() to close the registered forward closer")
	}
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

	actual, ok := node.(*realNode)
	if !ok {
		t.Fatalf("node = %T, want *realNode", node)
	}
	if got, want := actual.server.Dir, filepath.Join(stateDir, p.ID); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
	if actual.server.Hostname != "ts-host" {
		t.Fatalf("Hostname = %q, want ts-host", actual.server.Hostname)
	}
	if actual.server.AuthKey != "auth" {
		t.Fatalf("AuthKey = %q, want auth", actual.server.AuthKey)
	}
	if actual.server.ControlURL != "https://control.example.com" {
		t.Fatalf("ControlURL = %q, want https://control.example.com", actual.server.ControlURL)
	}
	if !actual.server.Ephemeral {
		t.Fatal("Ephemeral should be true")
	}
	if actual.server.UserLogf == nil {
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

	actual := node.(*realNode)
	if actual.server.Hostname != "tailnet" {
		t.Fatalf("Hostname = %q, want tailnet", actual.server.Hostname)
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

	_, err = os.Stat(statePath)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory should be removed, stat error = %v", err)
	}
	_, err = os.Stat(runner.stateDir)
	if err != nil {
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

// TestRunSocks5RoutesUDPAssociateThroughNode is a regression test for the SOCKS5
// UDP ASSOCIATE relay: go-socks5's handleAssociate only consults the WithDial
// callback (never WithDialAndRequest, which is CONNECT-only), so without
// wiring WithDial to node.Dial, UDP ASSOCIATE would still be accepted at the
// protocol layer but silently fall back to a bare net.Dial that never reaches
// the tailnet. This drives a real UDP ASSOCIATE handshake end to end and
// asserts the datagram was relayed through node.Dial with network "udp".
func TestRunSocks5RoutesUDPAssociateThroughNode(t *testing.T) {
	runner := NewRunner()
	bg := context.Background()
	var lc net.ListenConfig
	tcpListener, err := lc.Listen(bg, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()

	targetUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer targetUDP.Close()

	type dialRequest struct {
		network string
		address string
	}
	dialed := make(chan dialRequest, 1)
	var dialer net.Dialer
	node := &fakeTSNode{
		dialFunc: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed <- dialRequest{network: network, address: address}
			conn, dialErr := dialer.DialContext(ctx, network, address)
			if dialErr != nil {
				return nil, dialErr
			}
			go func() {
				<-ctx.Done()
				_ = conn.Close()
			}()
			return conn, nil
		},
	}

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.runSocks5(ctx, node, tcpListener)
	}()

	controlConn, err := dialer.DialContext(ctx, "tcp", tcpListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close()
	err = controlConn.SetDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	_, err = controlConn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	_, err = io.ReadFull(controlConn, greeting)
	if err != nil {
		t.Fatal(err)
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		t.Fatalf("SOCKS greeting reply = %#v, want version 5 no auth", greeting)
	}

	// UDP ASSOCIATE request: CMD=0x03, ATYP=IPv4 0.0.0.0:0 (client doesn't
	// know its own UDP source yet).
	associateRequest := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	_, err = controlConn.Write(associateRequest)
	if err != nil {
		t.Fatal(err)
	}
	associateReply := make([]byte, 10)
	_, err = io.ReadFull(controlConn, associateReply)
	if err != nil {
		t.Fatal(err)
	}
	if associateReply[0] != 0x05 || associateReply[1] != 0x00 {
		t.Fatalf("SOCKS associate reply = %#v, want success", associateReply)
	}
	relayPort := int(associateReply[8])<<8 | int(associateReply[9])
	relayAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: relayPort}

	udpClient, err := net.DialUDP("udp", nil, relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()

	targetAddr := targetUDP.LocalAddr().(*net.UDPAddr)
	payload := []byte("udp-through-tsnet")
	datagram := []byte{0, 0, 0, 0x01, 127, 0, 0, 1, byte(targetAddr.Port >> 8), byte(targetAddr.Port)}
	datagram = append(datagram, payload...)
	_, err = udpClient.Write(datagram)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-dialed:
		if got.network != "udp" {
			t.Fatalf("network = %q, want udp", got.network)
		}
		if got.address != targetAddr.String() {
			t.Fatalf("dial address = %q, want %q", got.address, targetAddr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("node was not dialed for UDP ASSOCIATE relay")
	}

	err = targetUDP.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, _, err := targetUDP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("target did not receive relayed UDP payload: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Fatalf("relayed payload = %q, want %q", buf[:n], payload)
	}

	cancel()
	_ = controlConn.Close()
	_ = tcpListener.Close()
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

	listenFunc       func(network, addr string) (net.Listener, error)
	listenPacketFunc func(network, addr string) (net.PacketConn, error)

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
	return nil, errUnexpectedDial
}

func (n *fakeTSNode) Listen(network, addr string) (net.Listener, error) {
	if n.listenFunc != nil {
		return n.listenFunc(network, addr)
	}
	return nil, errUnexpectedListen
}

func (n *fakeTSNode) ListenPacket(network, addr string) (net.PacketConn, error) {
	if n.listenPacketFunc != nil {
		return n.listenPacketFunc(network, addr)
	}
	return nil, errUnexpectedListenPacket
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
	c.prefs = prefs.Clone()
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

// erroringListener is a minimal net.Listener test double whose Accept
// always returns a fixed, non-net.ErrClosed error, used to force
// relayTCPForward's operational error path (as opposed to its
// clean-shutdown path for net.ErrClosed/context cancellation).
type erroringListener struct {
	err error
}

func (l *erroringListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *erroringListener) Close() error              { return nil }
func (l *erroringListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

// erroringPacketConn is a minimal net.PacketConn test double whose ReadFrom
// always returns a fixed, non-net.ErrClosed error, used to force
// relayUDPForward's operational error path.
type erroringPacketConn struct {
	err error
}

func (c *erroringPacketConn) ReadFrom(_ []byte) (int, net.Addr, error)  { return 0, nil, c.err }
func (c *erroringPacketConn) WriteTo(_ []byte, _ net.Addr) (int, error) { return 0, nil }
func (c *erroringPacketConn) Close() error                              { return nil }
func (c *erroringPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}
func (c *erroringPacketConn) SetDeadline(_ time.Time) error      { return nil }
func (c *erroringPacketConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *erroringPacketConn) SetWriteDeadline(_ time.Time) error { return nil }

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

// closeRecorder is a minimal io.Closer test helper that records whether
// Close was called, following the existing fakeListener helper style.
type closeRecorder struct {
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// fakePacketConn is a minimal net.PacketConn test double used as a stand-in
// for a bound tsNode.ListenPacket result in tests that only need to verify
// bind/close plumbing, not actual datagram relaying.
type fakePacketConn struct {
	closed chan struct{}
	once   sync.Once
}

func (c *fakePacketConn) ReadFrom(_ []byte) (int, net.Addr, error) {
	<-c.closed
	return 0, nil, net.ErrClosed
}

func (c *fakePacketConn) WriteTo(_ []byte, _ net.Addr) (int, error) {
	return 0, net.ErrClosed
}

func (c *fakePacketConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
	})
	return nil
}

func (c *fakePacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func (c *fakePacketConn) SetDeadline(_ time.Time) error      { return nil }
func (c *fakePacketConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *fakePacketConn) SetWriteDeadline(_ time.Time) error { return nil }
