package tailscale

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/loopback"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

func TestUnspecifiedBindFailsButUnboundDialWorks(t *testing.T) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})
	if s.CreateNIC(1, loopback.New()) != nil {
		t.Fatal("nic")
	}
	addr := tcpip.AddrFromSlice(netip.MustParseAddr("127.0.0.1").AsSlice())
	if s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: addr.WithPrefix()}, stack.AddressProperties{}) != nil {
		t.Fatal("addr")
	}
	s.SetRouteTable([]tcpip.Route{{Destination: addr.WithPrefix().Subnet(), NIC: 1}})
	remote := tcpip.FullAddress{NIC: 1, Addr: addr, Port: 9000}
	l, err := gonet.ListenTCP(s, remote, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	unspecified := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFromSlice(netip.IPv4Unspecified().AsSlice())}
	c, err := gonet.DialTCPWithBind(ctx, s, unspecified, remote, ipv4.ProtocolNumber)
	if err == nil {
		_ = c.Close()
		t.Fatal("unspecified source bind unexpectedly succeeded; the unbound dial workaround may be unnecessary")
	}
	c, err = gonet.DialContextTCP(ctx, s, remote, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("unbound dial: %v", err)
	}
	_ = c.Close()
}
