package tailscale

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/localforward"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/wgengine/netstack"
)

var errNoNetstack = errors.New("tailscale userspace netstack is unavailable; refusing to dial through the host network")

const peerWaitTimeout = 5 * time.Second

type userspaceDialer interface {
	DialUserspace(ctx context.Context, network string, remote netip.AddrPort) (net.Conn, error)
}

// DialUserspace dials through the embedded netstack only. tsnet's own Dial falls
// back to host DNS and the host network on a miss, so it is never used here.
func (n *realNode) DialUserspace(ctx context.Context, network string, remote netip.AddrPort) (net.Conn, error) {
	impl, ok := n.server.Sys().Netstack.GetOK()
	if !ok {
		return nil, errNoNetstack
	}
	ns, ok := impl.(*netstack.Impl)
	if !ok {
		return nil, errNoNetstack
	}
	if network == "udp" {
		return ns.DialContextUDP(ctx, remote)
	}
	return ns.DialContextTCP(ctx, remote)
}

type statusCache struct {
	client localClient
	mu     sync.Mutex
	at     time.Time
	cached *ipnstate.Status
}

const statusCacheTTL = 2 * time.Second

func (c *statusCache) get(ctx context.Context, force bool) (*ipnstate.Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.cached != nil && time.Since(c.at) < statusCacheTTL {
		return c.cached, nil
	}
	status, err := c.client.Status(ctx)
	if err != nil {
		return nil, err
	}
	c.cached, c.at = status, time.Now()
	return status, nil
}

func localForwardDialer(node tsNode) localforward.Dialer {
	var cache *statusCache
	var once sync.Once
	dial := func(network string) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, addr string) (net.Conn, error) {
			once.Do(func() {
				client, err := node.LocalClient()
				if err == nil {
					cache = &statusCache{client: client}
				}
			})
			if cache == nil {
				return nil, errNoNetstack
			}
			return dialTailnet(ctx, node, cache, network, addr)
		}
	}
	return localforward.Dialer{TCP: dial("tcp"), UDP: dial("udp")}
}

func dialTailnet(ctx context.Context, node tsNode, cache *statusCache, network, addr string) (net.Conn, error) {
	dialer, ok := node.(userspaceDialer)
	if !ok {
		return nil, errNoNetstack
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, err
	}
	ip, err := resolveWithPeerWait(ctx, cache, host)
	if err != nil {
		return nil, err
	}
	return dialer.DialUserspace(ctx, network, netip.AddrPortFrom(ip, uint16(port)))
}

func resolveWithPeerWait(ctx context.Context, cache *statusCache, host string) (netip.Addr, error) {
	waitCtx, cancel := context.WithTimeout(ctx, peerWaitTimeout)
	defer cancel()
	force := false
	for {
		status, err := cache.get(waitCtx, force)
		if err != nil {
			if ctx.Err() != nil {
				return netip.Addr{}, ctx.Err()
			}
			if waitCtx.Err() != nil {
				return netip.Addr{}, errTargetNotFound
			}
			if waitPoll(waitCtx) != nil {
				return netip.Addr{}, err
			}
			continue
		}
		ip, resolveErr := resolveTarget(status, host)
		if resolveErr == nil {
			return ip, nil
		}
		if !force {
			force = true
			continue
		}
		if len(status.Peer) > 0 {
			return ip, resolveErr
		}
		err = waitPoll(waitCtx)
		if err != nil {
			if ctx.Err() != nil {
				return netip.Addr{}, ctx.Err()
			}
			return ip, resolveErr
		}
	}
}

func waitPoll(ctx context.Context) error {
	timer := time.NewTimer(exitNodeReadyPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
