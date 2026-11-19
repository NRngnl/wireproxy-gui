package tailscale

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"

	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
)

type peerWatcher interface {
	WatchPeers(ctx context.Context, initial func(*ipnstate.Status), changed func()) error
}

type peerView struct {
	ctx      context.Context
	client   localClient
	watcher  peerWatcher
	status   atomic.Pointer[ipnstate.Status]
	ready    chan struct{}
	readyOne sync.Once
	refresh  chan struct{}
	watching atomic.Bool
	wg       sync.WaitGroup
}

func newPeerView(ctx context.Context, node tsNode, client localClient) *peerView {
	v := &peerView{ctx: ctx, client: client, ready: make(chan struct{}), refresh: make(chan struct{}, 1)}
	v.watcher, _ = node.(peerWatcher)
	v.wg.Go(v.refresher)
	v.startWatch()
	return v
}

func (v *peerView) startWatch() {
	if v.watcher == nil || v.ctx.Err() != nil || !v.watching.CompareAndSwap(false, true) {
		return
	}
	v.wg.Go(func() {
		defer v.watching.Store(false)
		_ = v.watcher.WatchPeers(v.ctx, v.publish, v.kick)
	})
}

func (v *peerView) publish(status *ipnstate.Status) {
	v.status.Store(status)
	v.readyOne.Do(func() { close(v.ready) })
}

func (v *peerView) kick() {
	select {
	case v.refresh <- struct{}{}:
	default:
	}
}

func (v *peerView) refresher() {
	for {
		select {
		case <-v.ctx.Done():
			return
		case <-v.refresh:
			status, err := v.client.Status(v.ctx)
			if err == nil {
				v.publish(status)
			}
		}
	}
}

func (v *peerView) wait() {
	v.wg.Wait()
}

func (v *peerView) get(ctx context.Context) (*ipnstate.Status, error) {
	if !v.watching.Load() {
		v.startWatch()
		if !v.watching.Load() {
			return v.client.Status(ctx)
		}
	}
	select {
	case <-v.ready:
		return v.status.Load(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (v *peerView) resolve(ctx context.Context, host string) (netip.Addr, error) {
	status, err := v.get(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	return resolveTarget(status, host)
}

// WatchPeers reports the first status to initial and calls changed whenever the self node, the peer set or a
// peer's online state changes, until ctx ends or the watch fails.
func (n *realNode) WatchPeers(ctx context.Context, initial func(*ipnstate.Status), changed func()) error {
	client, err := n.server.LocalClient()
	if err != nil {
		return err
	}
	watcher, err := client.WatchIPNBus(ctx, ipn.NotifyInitialStatus|ipn.NotifyPeerChanges|ipn.NotifyPeerPatches)
	if err != nil {
		return err
	}
	defer watcher.Close()
	for {
		notify, err := watcher.Next()
		if err != nil {
			return err
		}
		if notify.InitialStatus != nil {
			initial(notify.InitialStatus)
		}
		if notify.SelfChange != nil || len(notify.PeersChanged) > 0 || len(notify.PeersRemoved) > 0 || onlineChanged(notify) {
			changed()
		}
	}
}

func onlineChanged(notify ipn.Notify) bool {
	for _, patch := range notify.PeerChangedPatch {
		if patch.Online != nil {
			return true
		}
	}
	return false
}
