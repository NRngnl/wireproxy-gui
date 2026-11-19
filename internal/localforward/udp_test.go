package localforward

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/loopback"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

func newGvisorUDPEcho(t *testing.T) (*stack.Stack, tcpip.FullAddress) {
	t.Helper()
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{udp.NewProtocol},
	})
	t.Cleanup(func() {
		s.Close()
		s.Wait()
	})
	if err := s.CreateNIC(1, loopback.New()); err != nil {
		t.Fatalf("CreateNIC: %v", err)
	}
	addr := tcpip.AddrFromSlice(netip.MustParseAddr("127.0.0.1").AsSlice())
	protoAddr := tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: addr.WithPrefix(),
	}
	if err := s.AddProtocolAddress(1, protoAddr, stack.AddressProperties{}); err != nil {
		t.Fatalf("AddProtocolAddress: %v", err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: addr.WithPrefix().Subnet(), NIC: 1}})
	remote := tcpip.FullAddress{NIC: 1, Addr: addr, Port: 9000}
	echoConn, err := gonet.DialUDP(s, &remote, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = echoConn.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, rerr := echoConn.ReadFrom(buf)
			if rerr != nil {
				return
			}
			_, _ = echoConn.WriteTo(buf[:n], from)
		}
	}()
	return s, remote
}

func liveHeapBytes() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

func TestUDPIdleSessionsHoldNoReplyBuffer(t *testing.T) {
	s, remote := newGvisorUDPEcho(t)
	var lc net.ListenConfig
	probe, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.LocalAddr().String()
	_ = probe.Close()

	var dials atomic.Int32
	set := New(t.Context(), Dialer{UDP: func(_ context.Context, _ string) (net.Conn, error) {
		dials.Add(1)
		return gonet.DialUDP(s, nil, &remote, ipv4.ProtocolNumber)
	}}, socksconn.Timeouts{UDPIdle: 60 * time.Second}, nil)
	defer set.Close()
	set.Apply([]profile.LocalForward{{
		Protocol:   profile.PortForwardUDP,
		ListenAddr: listen,
		TargetAddr: "127.0.0.1:9000",
	}})

	const sessions = 256
	var d net.Dialer
	clients := make([]net.Conn, sessions)
	for i := range clients {
		c, derr := d.DialContext(t.Context(), "udp", listen)
		if derr != nil {
			t.Fatal(derr)
		}
		defer c.Close()
		clients[i] = c
	}

	reply := make([]byte, 16)
	before := liveHeapBytes()
	for i, c := range clients {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, werr := c.Write([]byte("ping"))
		if werr != nil {
			t.Fatalf("client %d write: %v", i, werr)
		}
		n, rerr := c.Read(reply)
		if rerr != nil || string(reply[:n]) != "ping" {
			t.Fatalf("client %d reply = %q, %v", i, reply[:n], rerr)
		}
	}
	after := liveHeapBytes()

	for _, idx := range []int{0, sessions - 1} {
		_ = clients[idx].SetReadDeadline(time.Now().Add(5 * time.Second))
		_, werr := clients[idx].Write([]byte("live"))
		if werr != nil {
			t.Fatalf("client %d live write: %v", idx, werr)
		}
		n, rerr := clients[idx].Read(reply)
		if rerr != nil || string(reply[:n]) != "live" {
			t.Fatalf("client %d live reply = %q, %v", idx, reply[:n], rerr)
		}
	}
	if got := dials.Load(); got != sessions {
		t.Fatalf("dials = %d, want %d", got, sessions)
	}
	if st := set.States()[0]; st.Status != StatusActive || st.Err != "" {
		t.Fatalf("rule state = %#v", st)
	}

	var delta int64
	if after > before {
		delta = int64(after - before)
	}
	perSession := delta / sessions
	t.Logf("idle_udp_sessions=%d before=%d after=%d delta=%d bytes_per_session=%d", sessions, before, after, delta, perSession)
	if perSession >= socksconn.UDPBufferSize/2 {
		t.Fatalf("live heap per idle UDP session = %d B (want < %d B)", perSession, socksconn.UDPBufferSize/2)
	}

	probe2, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen2 := probe2.LocalAddr().String()
	_ = probe2.Close()
	var idleDials atomic.Int32
	idleSet := New(t.Context(), Dialer{UDP: func(_ context.Context, _ string) (net.Conn, error) {
		idleDials.Add(1)
		return gonet.DialUDP(s, nil, &remote, ipv4.ProtocolNumber)
	}}, socksconn.Timeouts{UDPIdle: 100 * time.Millisecond}, nil)
	defer idleSet.Close()
	idleSet.Apply([]profile.LocalForward{{
		Protocol:   profile.PortForwardUDP,
		ListenAddr: listen2,
		TargetAddr: "127.0.0.1:9000",
	}})
	idleClient, err := d.DialContext(t.Context(), "udp", listen2)
	if err != nil {
		t.Fatal(err)
	}
	defer idleClient.Close()
	roundTrip := func(msg string) {
		_ = idleClient.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, werr := idleClient.Write([]byte(msg))
		if werr != nil {
			t.Fatal(werr)
		}
		n, rerr := idleClient.Read(reply)
		if rerr != nil || string(reply[:n]) != msg {
			t.Fatalf("idle echo = %q, %v", reply[:n], rerr)
		}
	}
	roundTrip("a")
	time.Sleep(250 * time.Millisecond)
	roundTrip("b")
	if got := idleDials.Load(); got != 2 {
		t.Fatalf("gvisor idle dials = %d, want 2", got)
	}
}
