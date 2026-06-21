package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func TestStartDispatchesByProfileBackend(t *testing.T) {
	wireguard := newTestBackend()
	tailscale := newTestBackend()
	runner := NewWithBackends(wireguard, tailscale)
	wireguardProfile := profile.Profile{ID: "wg", Kind: profile.BackendWireGuard, Name: "wg"}
	tailscaleProfile := profile.NewTailscale("tailnet", 1081)

	err := runner.Start(context.Background(), wireguardProfile)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Start(context.Background(), tailscaleProfile)
	if err != nil {
		t.Fatal(err)
	}

	if len(wireguard.starts) != 1 || wireguard.starts[0].ID != wireguardProfile.ID {
		t.Fatalf("WireGuard backend starts = %#v", wireguard.starts)
	}
	if len(tailscale.starts) != 1 || tailscale.starts[0].ID != tailscaleProfile.ID {
		t.Fatalf("Tailscale backend starts = %#v", tailscale.starts)
	}
}

func TestStartRejectsUnknownBackend(t *testing.T) {
	runner := NewWithBackends(newTestBackend(), newTestBackend())
	p := profile.Profile{ID: "bad", Kind: "bad", Name: "bad"}

	err := runner.Start(context.Background(), p)
	if !errors.Is(err, profile.ErrBackendKindInvalid) {
		t.Fatalf("expected ErrBackendKindInvalid, got %v", err)
	}
}

func TestStopUsesStartedBackend(t *testing.T) {
	wireguard := newTestBackend()
	tailscale := newTestBackend()
	runner := NewWithBackends(wireguard, tailscale)
	p := profile.NewTailscale("tailnet", 1081)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.Stop(p.ID) {
		t.Fatal("expected Stop to stop started profile")
	}

	if len(tailscale.stops) != 1 || tailscale.stops[0] != p.ID {
		t.Fatalf("Tailscale backend stops = %#v", tailscale.stops)
	}
	if len(wireguard.stops) != 0 {
		t.Fatalf("WireGuard backend should not be stopped, got %#v", wireguard.stops)
	}
}

func TestExitNodesUsesStartedTailscaleBackend(t *testing.T) {
	wireguard := newTestBackend()
	tailscale := newTestBackend()
	tailscale.exitNodes = []connection.ExitNode{{ID: "stable-exit", Name: "exit-a"}}
	runner := NewWithBackends(wireguard, tailscale)
	p := profile.NewTailscale("tailnet", 1081)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := runner.ExitNodes(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(nodes) != 1 || nodes[0].ID != "stable-exit" {
		t.Fatalf("ExitNodes = %#v, want stable-exit", nodes)
	}
	if len(tailscale.exitNodeCalls) != 1 || tailscale.exitNodeCalls[0] != p.ID {
		t.Fatalf("Tailscale exit node calls = %#v", tailscale.exitNodeCalls)
	}
	if len(wireguard.exitNodeCalls) != 0 {
		t.Fatalf("WireGuard backend should not list exit nodes, got %#v", wireguard.exitNodeCalls)
	}
}

func TestUpdateExitNodeUsesStartedTailscaleBackend(t *testing.T) {
	wireguard := newTestBackend()
	tailscale := newTestBackend()
	runner := NewWithBackends(wireguard, tailscale)
	p := profile.NewTailscale("tailnet", 1081)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.UpdateExitNode(context.Background(), p.ID, profile.TailscaleConfig{
		ExitNode:               "stable-exit",
		ExitNodeAllowLANAccess: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(tailscale.updateExitNodeCalls) != 1 || tailscale.updateExitNodeCalls[0] != p.ID {
		t.Fatalf("Tailscale update exit node calls = %#v", tailscale.updateExitNodeCalls)
	}
	if got := tailscale.updateExitNodeConfigs[0]; got.ExitNode != "stable-exit" || !got.ExitNodeAllowLANAccess {
		t.Fatalf("Tailscale update exit node config = %#v", got)
	}
	if len(wireguard.updateExitNodeCalls) != 0 {
		t.Fatalf("WireGuard backend should not update exit nodes, got %#v", wireguard.updateExitNodeCalls)
	}
}

func TestLogoutUsesTailscaleBackendWhenProfileIsStopped(t *testing.T) {
	wireguard := newTestBackend()
	tailscale := newTestBackend()
	runner := NewWithBackends(wireguard, tailscale)

	err := runner.Logout(context.Background(), "tailnet-profile")
	if err != nil {
		t.Fatal(err)
	}

	if len(tailscale.logoutCalls) != 1 || tailscale.logoutCalls[0] != "tailnet-profile" {
		t.Fatalf("Tailscale logout calls = %#v", tailscale.logoutCalls)
	}
	if len(wireguard.logoutCalls) != 0 {
		t.Fatalf("WireGuard backend should not receive stopped-profile logout, got %#v", wireguard.logoutCalls)
	}
}

func TestExitNodesWithoutRunningTailscaleBackendReturnsUnavailable(t *testing.T) {
	runner := NewWithBackends(newTestBackend(), newTestBackend())

	_, err := runner.ExitNodes(context.Background(), "missing")
	if !errors.Is(err, connection.ErrExitNodesUnavailable) {
		t.Fatalf("expected ErrExitNodesUnavailable, got %v", err)
	}
}

func TestStoppedEventDuringStartDoesNotLeaveStaleBacking(t *testing.T) {
	wireguard := newTestBackend()
	tailscale := newTestBackend()
	p := profile.NewTailscale("tailnet", 1081)
	blockStart := make(chan struct{})
	tailscale.startBlock = blockStart
	tailscale.startEvent = connection.Event{
		Type:      connection.EventStopped,
		ProfileID: p.ID,
	}
	runner := NewWithBackends(wireguard, tailscale)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.Start(context.Background(), p)
	}()

	select {
	case event := <-runner.Events():
		if event.Type != connection.EventStopped || event.ProfileID != p.ID {
			t.Fatalf("forwarded event = %#v, want stopped for %s", event, p.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopped event was not forwarded")
	}
	close(blockStart)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return")
	}

	_, err := runner.ExitNodes(context.Background(), p.ID)
	if !errors.Is(err, connection.ErrExitNodesUnavailable) {
		t.Fatalf("expected ErrExitNodesUnavailable after stopped event, got %v", err)
	}
	if len(tailscale.exitNodeCalls) != 0 {
		t.Fatalf("stale backing routed ExitNodes to Tailscale backend: %#v", tailscale.exitNodeCalls)
	}
}

type testBackend struct {
	events                chan connection.Event
	starts                []profile.Profile
	stops                 []string
	exitNodeCalls         []string
	exitNodes             []connection.ExitNode
	exitNodesErr          error
	updateExitNodeCalls   []string
	updateExitNodeConfigs []profile.TailscaleConfig
	updateExitNodeErr     error
	logoutCalls           []string
	logoutErr             error
	running               map[string]bool
	startEvent            connection.Event
	startBlock            <-chan struct{}
}

func newTestBackend() *testBackend {
	return &testBackend{
		events:  make(chan connection.Event, 16),
		running: map[string]bool{},
	}
}

func (b *testBackend) Events() <-chan connection.Event {
	return b.events
}

func (b *testBackend) Running(profileID string) bool {
	return b.running[profileID]
}

func (b *testBackend) ExitNodes(_ context.Context, profileID string) ([]connection.ExitNode, error) {
	b.exitNodeCalls = append(b.exitNodeCalls, profileID)
	if b.exitNodesErr != nil {
		return nil, b.exitNodesErr
	}
	return b.exitNodes, nil
}

func (b *testBackend) UpdateExitNode(_ context.Context, profileID string, cfg profile.TailscaleConfig) error {
	b.updateExitNodeCalls = append(b.updateExitNodeCalls, profileID)
	b.updateExitNodeConfigs = append(b.updateExitNodeConfigs, cfg)
	return b.updateExitNodeErr
}

func (b *testBackend) Logout(_ context.Context, profileID string) error {
	b.logoutCalls = append(b.logoutCalls, profileID)
	return b.logoutErr
}

func (b *testBackend) Start(_ context.Context, p profile.Profile) error {
	b.starts = append(b.starts, p)
	b.running[p.ID] = true
	if b.startEvent.Type != "" {
		if b.startEvent.Type == connection.EventStopped || b.startEvent.Type == connection.EventError {
			delete(b.running, p.ID)
		}
		b.events <- b.startEvent
	}
	if b.startBlock != nil {
		<-b.startBlock
	}
	return nil
}

func (b *testBackend) Stop(profileID string) bool {
	b.stops = append(b.stops, profileID)
	if !b.running[profileID] {
		return false
	}
	delete(b.running, profileID)
	return true
}

func (b *testBackend) StopAll() {
	for profileID := range b.running {
		b.Stop(profileID)
	}
}

func (b *testBackend) StopAllAndWait(context.Context) error {
	b.StopAll()
	return nil
}
