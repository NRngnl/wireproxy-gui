package tailscale

import (
	"context"
	"maps"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

// NOTE: Tailscale's magicsock.sessionActiveTimeout is 45s; add a 1s margin so Status observes Active=false once traffic stops.
const peerActiveExpiry = 46 * time.Second

type peerWatcher interface {
	WatchPeers(ctx context.Context, initial func(*ipnstate.Status), changed func()) error
}

type peerView struct {
	ctx        context.Context
	client     localClient
	watcher    peerWatcher
	status     atomic.Pointer[ipnstate.Status]
	ready      chan struct{}
	readyOne   sync.Once
	refresh    chan struct{}
	watching   atomic.Bool
	wg         sync.WaitGroup
	onPaths    func()
	now        func() time.Time
	expiryWait time.Duration
	mu         sync.Mutex
	paths      map[key.NodePublic]connection.PeerPath
	expiry     *time.Timer
}

func newPeerView(ctx context.Context, node tsNode, client localClient, onPaths func()) *peerView {
	v := &peerView{
		ctx:        ctx,
		client:     client,
		onPaths:    onPaths,
		ready:      make(chan struct{}),
		refresh:    make(chan struct{}, 1),
		now:        time.Now,
		expiryWait: peerActiveExpiry,
	}
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
	paths := peerPaths(status)
	v.mu.Lock()
	same := maps.Equal(v.paths, paths)
	v.paths = paths
	v.scheduleExpiryLocked(status)
	v.mu.Unlock()
	if !same && v.onPaths != nil {
		v.onPaths()
	}
}

func (v *peerView) scheduleExpiryLocked(status *ipnstate.Status) {
	if v.expiry != nil {
		v.expiry.Stop()
		v.expiry = nil
	}
	if status == nil || v.ctx.Err() != nil {
		return
	}
	var earliest time.Time
	for _, peer := range status.Peer {
		if peerPath(peer).Kind == connection.PeerPathUnknown || peer.LastWrite.IsZero() {
			continue
		}
		deadline := peer.LastWrite.Add(v.expiryWait)
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	if earliest.IsZero() {
		return
	}
	delay := max(earliest.Sub(v.now()), min(time.Second, v.expiryWait))
	v.expiry = time.AfterFunc(delay, v.kick)
}

func (v *peerView) stopExpiry() {
	v.mu.Lock()
	if v.expiry != nil {
		v.expiry.Stop()
		v.expiry = nil
	}
	v.mu.Unlock()
}

func (v *peerView) kick() {
	select {
	case v.refresh <- struct{}{}:
	default:
	}
}

func (v *peerView) refresher() {
	defer v.stopExpiry()
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

// WatchPeers reports the first status to initial and calls changed whenever the self node, the peer set, a
// peer's online state or a peer's dataplane path changes, until ctx ends or the watch fails.
func (n *realNode) WatchPeers(ctx context.Context, initial func(*ipnstate.Status), changed func()) error {
	if changed != nil {
		n.onPath.Store(&changed)
		defer n.onPath.Store(nil)
	}
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
		if notify.SelfChange != nil || len(notify.PeersChanged) > 0 || len(notify.PeersRemoved) > 0 || len(notify.PeerChangedPatch) > 0 {
			changed()
		}
	}
}
