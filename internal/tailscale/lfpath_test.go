package tailscale

import (
	"net/netip"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestTargetPath(t *testing.T) {
	peer := func(name, ip, cur, relay string) *ipnstate.PeerStatus {
		return &ipnstate.PeerStatus{DNSName: name + ".tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr(ip)}, CurAddr: cur, Relay: relay}
	}
	status := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
		key.NewNode().Public(): peer("direct", "100.64.0.2", "192.0.2.1:41641", "fra"),
		key.NewNode().Public(): peer("relayed", "100.64.0.3", "", "tok"),
		key.NewNode().Public(): peer("idle", "100.64.0.4", "", ""),
	}}
	cases := map[string]connection.PeerPath{
		"direct:22":       {Kind: connection.PeerPathDirect},
		"relayed:22":      {Kind: connection.PeerPathRelay, Relay: "tok"},
		"idle:22":         {},
		"100.64.0.3:22":   {Kind: connection.PeerPathRelay, Relay: "tok"},
		"192.168.1.10:22": {},
		"missing:22":      {},
	}
	for target, want := range cases {
		if got := targetPath(status, target); got != want {
			t.Errorf("targetPath(%q) = %+v, want %+v", target, got, want)
		}
	}
}
