package socksconn

import (
	"testing"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/loopback"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

func TestWrapTCPEnablesKeepAliveOnGvisorConn(t *testing.T) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})
	if err := s.CreateNIC(1, loopback.New()); err != nil {
		t.Fatal(err)
	}
	addr := tcpip.AddrFrom4([4]byte{10, 0, 0, 1})
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          header.IPv4ProtocolNumber,
		AddressWithPrefix: addr.WithPrefix(),
	}, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	full := tcpip.FullAddress{NIC: 1, Addr: addr, Port: 8080}
	ln, err := gonet.ListenTCP(s, full, header.IPv4ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	raw, err := gonet.DialTCP(s, full, header.IPv4ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	conn := WrapTCP(raw, IdleTimeout, DrainTimeout)
	defer conn.Close()

	ep, _, ok := GvisorEndpoint(raw)
	if !ok {
		t.Fatal("gonet endpoint not extractable")
	}
	if !ep.SocketOptions().GetKeepAlive() {
		t.Fatal("keepalive not enabled on gonet conn")
	}
	var idle tcpip.KeepaliveIdleOption
	if err := ep.GetSockOpt(&idle); err != nil {
		t.Fatal(err)
	}
	if got := idle; got != tcpip.KeepaliveIdleOption(keepAlivePeriod) {
		t.Fatalf("idle = %v, want %v", got, keepAlivePeriod)
	}
}
