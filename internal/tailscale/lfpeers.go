package tailscale

import (
	"context"
	"net/netip"
	"sync"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

type peerWatcher interface {
	WatchPeers(ctx context.Context, changed func()) error
}

type peerView struct {
	client  localClient
	mu      sync.Mutex
	status  *ipnstate.Status
	fresh   bool
	watched bool
	changed chan struct{}
}

func newPeerView(ctx context.Context, node tsNode, client localClient) *peerView {
	v := &peerView{client: client, changed: make(chan struct{})}
	watcher, ok := node.(peerWatcher)
	if ok {
		v.watched = true
		go func() {
			_ = watcher.WatchPeers(ctx, func() { v.invalidate(true) })
			v.invalidate(false)
		}()
	}
	return v
}

func (v *peerView) invalidate(watched bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.fresh = false
	v.watched = watched
	close(v.changed)
	v.changed = make(chan struct{})
}

func (v *peerView) get(ctx context.Context) (*ipnstate.Status, <-chan struct{}, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.fresh || !v.watched {
		status, err := v.client.Status(ctx)
		if err != nil {
			return nil, nil, err
		}
		v.status, v.fresh = status, true
	}
	if !v.watched {
		return v.status, nil, nil
	}
	return v.status, v.changed, nil
}

func (v *peerView) resolve(ctx context.Context, host string) (netip.Addr, error) {
	for {
		status, changed, err := v.get(ctx)
		if err != nil {
			return netip.Addr{}, err
		}
		ip, err := resolveTarget(status, host)
		if err == nil || len(status.Peer) > 0 || changed == nil {
			return ip, err
		}
		select {
		case <-ctx.Done():
			return netip.Addr{}, err
		case <-changed:
		}
	}
}

// WatchPeers calls changed whenever the tailnet's self node or peer set changes, until ctx ends or the
// watch fails.
func (n *realNode) WatchPeers(ctx context.Context, changed func()) error {
	client, err := n.server.LocalClient()
	if err != nil {
		return err
	}
	watcher, err := client.WatchIPNBus(ctx, ipn.NotifyInitialStatus|ipn.NotifyPeerChanges)
	if err != nil {
		return err
	}
	defer watcher.Close()
	for {
		notify, err := watcher.Next()
		if err != nil {
			return err
		}
		if notify.InitialStatus != nil || notify.SelfChange != nil || len(notify.PeersChanged) > 0 || len(notify.PeersRemoved) > 0 {
			changed()
		}
	}
}
