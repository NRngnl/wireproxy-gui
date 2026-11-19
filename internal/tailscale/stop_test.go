package tailscale

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

type slowCloseNode struct {
	*fakeTSNode
	release chan struct{}
}

func (n *slowCloseNode) Close() error {
	<-n.release
	return nil
}

func TestStopDoesNotWaitForEngineTeardown(t *testing.T) {
	runner := NewRunner()
	runner.stateDir = t.TempDir()
	node := &slowCloseNode{
		fakeTSNode: &fakeTSNode{client: &fakeLocalClient{prefs: ipn.NewPrefs(), status: &ipnstate.Status{}}},
		release:    make(chan struct{}),
	}
	runner.listen = func(string, string) (net.Listener, error) { return newFakeListener(), nil }
	runner.newNode = func(profile.Profile, string, func(string, ...any)) (tsNode, error) { return node, nil }
	runner.serveSocks5 = func(ctx context.Context, _ profile.Profile, _ tsNode, _ net.Listener) error {
		<-ctx.Done()
		return ctx.Err()
	}
	p := profile.NewTailscale("tailnet", 18090)
	err := runner.Start(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { runner.Stop(p.ID); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop waited for node teardown")
	}
	close(node.release)
	err = runner.StopAllAndWait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
}
