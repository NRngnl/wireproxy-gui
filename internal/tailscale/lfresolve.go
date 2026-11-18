package tailscale

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"tailscale.com/ipn/ipnstate"
)

var (
	errTargetNotFound  = errors.New("target is not a peer, tailnet IP or advertised subnet route")
	errTargetAmbiguous = errors.New("target name matches several peers; use the full name or an IP")
)

func trimDNS(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func firstLabel(name string) string {
	label, _, _ := strings.Cut(trimDNS(name), ".")
	return label
}

// resolveTarget maps a tailnet name or IP to an address the netstack can dial.
// It never consults host DNS. Names resolve only to peers or self; IPs may also
// fall inside an online peer's primary (subnet) routes.
func resolveTarget(status *ipnstate.Status, host string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(host)
	if err == nil {
		return resolveIP(status, ip.Unmap())
	}
	name := trimDNS(host)
	var short []netip.Addr
	nodes := []*ipnstate.PeerStatus{}
	if status.Self != nil {
		nodes = append(nodes, status.Self)
	}
	for _, peer := range status.Peer {
		nodes = append(nodes, peer)
	}
	for _, node := range nodes {
		if len(node.TailscaleIPs) == 0 {
			continue
		}
		if trimDNS(node.DNSName) == name {
			return node.TailscaleIPs[0], nil
		}
		if firstLabel(node.DNSName) == name {
			short = append(short, node.TailscaleIPs[0])
		}
	}
	switch len(short) {
	case 0:
		return netip.Addr{}, fmt.Errorf("%w: %s", errTargetNotFound, host)
	case 1:
		return short[0], nil
	default:
		return netip.Addr{}, fmt.Errorf("%w: %s", errTargetAmbiguous, host)
	}
}

func resolveIP(status *ipnstate.Status, ip netip.Addr) (netip.Addr, error) {
	if status.Self != nil && containsAddr(status.Self.TailscaleIPs, ip) {
		return ip, nil
	}
	for _, peer := range status.Peer {
		if containsAddr(peer.TailscaleIPs, ip) {
			return ip, nil
		}
	}
	for _, peer := range status.Peer {
		if !peer.Online || peer.PrimaryRoutes == nil {
			continue
		}
		for _, route := range peer.PrimaryRoutes.All() {
			if route.Contains(ip) {
				return ip, nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("%w: %s", errTargetNotFound, ip)
}

func containsAddr(addrs []netip.Addr, ip netip.Addr) bool {
	return slices.Contains(addrs, ip)
}
