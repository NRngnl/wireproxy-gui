package tailscale

import (
	"errors"
	"net/netip"
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestResolveTarget(t *testing.T) {
	routes := views.SliceOf([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")})
	status := &ipnstate.Status{
		Self: &ipnstate.PeerStatus{DNSName: "me.tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {DNSName: "pc.tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")}},
			key.NewNode().Public(): {DNSName: "nas.tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}, Online: true, PrimaryRoutes: &routes},
			key.NewNode().Public(): {DNSName: "nas.other.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.4")}},
		},
	}
	tests := []struct {
		host    string
		want    string
		wantErr error
	}{
		{"PC.tail.ts.net", "100.64.0.2", nil},
		{"pc", "100.64.0.2", nil},
		{"nas", "", errTargetAmbiguous},
		{"100.64.0.1", "100.64.0.1", nil},
		{"192.0.2.9", "192.0.2.9", nil},
		{"198.51.100.1", "", errTargetNotFound},
		{"nope", "", errTargetNotFound},
	}
	for _, tc := range tests {
		got, err := resolveTarget(status, tc.host)
		if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && got.String() != tc.want) {
			t.Errorf("resolveTarget(%q) = %v, %v; want %s, %v", tc.host, got, err, tc.want, tc.wantErr)
		}
	}
}
