package tailscale

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"

	"github.com/NRngnl/wireproxy-gui/internal/localforward"
	"tailscale.com/wgengine/netstack"
)

var errNoNetstack = errors.New("tailscale userspace netstack is unavailable; refusing to dial through the host network")

func noNetstack(context.Context, string) (net.Conn, error) {
	return nil, errNoNetstack
}

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

func localForwardDialer(node tsNode, peers *peerView) localforward.Dialer {
	dial := func(network string) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, addr string) (net.Conn, error) {
			return dialTailnet(ctx, node, peers, network, addr)
		}
	}
	return localforward.Dialer{TCP: dial("tcp"), UDP: dial("udp")}
}

func dialTailnet(ctx context.Context, node tsNode, peers *peerView, network, addr string) (net.Conn, error) {
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
	ip, err := peers.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	return dialer.DialUserspace(ctx, network, netip.AddrPortFrom(ip, uint16(port)))
}
