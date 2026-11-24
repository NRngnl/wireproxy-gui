package tailscale

import (
	"net"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func peerPath(peer *ipnstate.PeerStatus) connection.PeerPath {
	// NOTE: Relay is the peer's home DERP region and is set even when idle; match `tailscale status` by reporting a path only while the peer is active.
	if peer == nil || !peer.Active {
		return connection.PeerPath{}
	}
	switch {
	case peer.CurAddr != "":
		return connection.PeerPath{Kind: connection.PeerPathDirect}
	case peer.PeerRelay != "":
		return connection.PeerPath{Kind: connection.PeerPathPeerRelay, Relay: peerRelayAddr(peer.PeerRelay)}
	case peer.Relay != "":
		return connection.PeerPath{Kind: connection.PeerPathRelay, Relay: peer.Relay}
	}
	return connection.PeerPath{}
}

func peerRelayAddr(s string) string {
	addr, _, _ := strings.Cut(s, ":vni:")
	return addr
}

func isPeerPathLog(format string) bool {
	if !strings.Contains(format, "magicsock:") {
		return false
	}
	return strings.Contains(format, "now using") ||
		strings.Contains(format, "new contact:") ||
		strings.Contains(format, "derp route for") ||
		strings.Contains(format, "removing route") ||
		strings.Contains(format, "home is now derp-") ||
		strings.Contains(format, "performing rebind") ||
		strings.Contains(format, "disco: failed to send") ||
		strings.Contains(format, "doing cleanup for")
}

func peerPaths(status *ipnstate.Status) map[key.NodePublic]connection.PeerPath {
	if status == nil {
		return nil
	}
	paths := make(map[key.NodePublic]connection.PeerPath, len(status.Peer))
	for k, peer := range status.Peer {
		paths[k] = peerPath(peer)
	}
	return paths
}

func targetPath(status *ipnstate.Status, target string) connection.PeerPath {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return connection.PeerPath{}
	}
	ip, err := resolveTarget(status, host)
	if err != nil {
		return connection.PeerPath{}
	}
	for _, peer := range status.Peer {
		if containsAddr(peer.TailscaleIPs, ip) {
			return peerPath(peer)
		}
	}
	return connection.PeerPath{}
}
