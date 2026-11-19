package tailscale

import (
	"net"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func peerPath(peer *ipnstate.PeerStatus) connection.PeerPath {
	switch {
	case peer.CurAddr != "":
		return connection.PeerPath{Kind: connection.PeerPathDirect}
	case peer.Relay != "" || peer.PeerRelay != "":
		return connection.PeerPath{Kind: connection.PeerPathRelay, Relay: peer.Relay}
	}
	return connection.PeerPath{}
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
