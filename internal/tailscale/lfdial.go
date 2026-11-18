package tailscale

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/localforward"
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
	local := netip.IPv4Unspecified()
	if remote.Addr().Is6() {
		local = netip.IPv6Unspecified()
	}
	if network == "udp" {
		return ns.DialContextUDPWithBind(ctx, local, remote)
	}
	return ns.DialContextTCPWithBind(ctx, local, remote)
}

func localForwardDialer(node tsNode) localforward.Dialer {
	dial := func(network string) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, addr string) (net.Conn, error) {
			return dialTailnet(ctx, node, network, addr)
		}
	}
	return localforward.Dialer{TCP: dial("tcp"), UDP: dial("udp")}
}

func dialTailnet(ctx context.Context, node tsNode, network, addr string) (net.Conn, error) {
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
	ip, err := resolveWithPeerWait(ctx, node, host)
	if err != nil {
		return nil, err
	}
	return dialer.DialUserspace(ctx, network, netip.AddrPortFrom(ip, uint16(port)))
}

func resolveWithPeerWait(ctx context.Context, node tsNode, host string) (netip.Addr, error) {
	client, err := node.LocalClient()
	if err != nil {
		return netip.Addr{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, peerWaitTimeout)
	defer cancel()
	for {
		status, err := client.Status(waitCtx)
		if err != nil {
			status, err = client.Status(ctx)
			if err != nil {
				return netip.Addr{}, err
			}
			return resolveTarget(status, host)
		}
		ip, resolveErr := resolveTarget(status, host)
		if resolveErr == nil || len(status.Peer) > 0 {
			return ip, resolveErr
		}
		err = waitPoll(waitCtx)
		if err != nil {
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
