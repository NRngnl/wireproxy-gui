package localforward

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
	"gvisor.dev/gvisor/pkg/tcpip/ports"
)

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	pc, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}

func waitRuleErr(t *testing.T, set *Set, key profile.LocalForwardKey, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, st := range set.States() {
			if st.Key == key && st.Status == StatusActive && st.Err == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("rule %v states = %#v, want StatusActive with err %q", key, set.States(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestForwardPortShareRefusesExcessAndReportsError(t *testing.T) {
	first, last := ports.NewPortManager().PortRange()
	ephemeralCount := int(last) - int(first) + 1
	defaultSet := New(t.Context(), Dialer{}, socksconn.Timeouts{}, nil)
	defer defaultSet.Close()
	if got, want := int(defaultSet.udpBudget.limit), ephemeralCount/2; got != want {
		t.Fatalf("default udp limit = %d, want %d (half of %d)", got, want, ephemeralCount)
	}
	if got, maxWant := int(defaultSet.tcpBudget.limit), ephemeralCount/4; got < 1 || got > maxWant {
		t.Fatalf("default tcp limit = %d, want in [1, %d]", got, maxWant)
	}

	var lc net.ListenConfig
	udpEcho, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	go func() {
		buf := make([]byte, 256)
		for {
			n, from, rerr := udpEcho.ReadFrom(buf)
			if rerr != nil {
				return
			}
			_, _ = udpEcho.WriteTo(buf[:n], from)
		}
	}()
	tcpTarget := echoServer(t)

	var d net.Dialer
	var udpDials, tcpDials atomic.Int32
	set := New(t.Context(), Dialer{
		TCP: func(ctx context.Context, addr string) (net.Conn, error) {
			tcpDials.Add(1)
			return d.DialContext(ctx, "tcp", addr)
		},
		UDP: func(ctx context.Context, addr string) (net.Conn, error) {
			udpDials.Add(1)
			return d.DialContext(ctx, "udp", addr)
		},
		EphemeralPorts: 8,
	}, socksconn.Timeouts{UDPIdle: 150 * time.Millisecond}, nil)
	defer set.Close()

	udpRule1 := profile.LocalForward{Protocol: profile.PortForwardUDP, ListenAddr: freeUDPAddr(t), TargetAddr: udpEcho.LocalAddr().String()}
	udpRule2 := profile.LocalForward{Protocol: profile.PortForwardUDP, ListenAddr: freeUDPAddr(t), TargetAddr: udpEcho.LocalAddr().String()}
	tcpRule1 := tcpRule(freeAddr(t), tcpTarget)
	tcpRule2 := tcpRule(freeAddr(t), tcpTarget)
	set.Apply([]profile.LocalForward{udpRule1, udpRule2, tcpRule1, tcpRule2})

	udpClients := make([]net.Conn, 4)
	reply := make([]byte, 16)
	for i := range udpClients {
		listen := udpRule1.ListenAddr
		if i >= 2 {
			listen = udpRule2.ListenAddr
		}
		c, derr := d.DialContext(t.Context(), "udp", listen)
		if derr != nil {
			t.Fatal(derr)
		}
		defer c.Close()
		udpClients[i] = c
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, werr := c.Write([]byte("ok"))
		if werr != nil {
			t.Fatal(werr)
		}
		n, rerr := c.Read(reply)
		if rerr != nil || string(reply[:n]) != "ok" {
			t.Fatalf("udp client %d = %q, %v", i, reply[:n], rerr)
		}
	}

	excessUDP, err := d.DialContext(t.Context(), "udp", udpRule1.ListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer excessUDP.Close()
	_, err = excessUDP.Write([]byte("drop"))
	if err != nil {
		t.Fatal(err)
	}
	waitRuleErr(t, set, udpRule1.Key(), ErrSessionLimitReached.Error())
	if got := udpDials.Load(); got != 4 {
		t.Fatalf("udp dials after refusal = %d, want 4", got)
	}
	_ = udpClients[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = udpClients[0].Write([]byte("keep"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := udpClients[0].Read(reply)
	if err != nil || string(reply[:n]) != "keep" {
		t.Fatalf("existing udp session broken: %q, %v", reply[:n], err)
	}

	tcpClients := make([]net.Conn, 2)
	for i, listen := range []string{tcpRule1.ListenAddr, tcpRule2.ListenAddr} {
		c, derr := dialTCP(t, listen)
		if derr != nil {
			t.Fatal(derr)
		}
		defer c.Close()
		tcpClients[i] = c
		_, werr := c.Write([]byte("a"))
		if werr != nil {
			t.Fatal(werr)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, rerr := io.ReadFull(c, reply[:1])
		if rerr != nil || reply[0] != 'a' {
			t.Fatalf("tcp client %d = %q, %v", i, reply[:1], rerr)
		}
	}

	excessTCP, err := dialTCP(t, tcpRule1.ListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer excessTCP.Close()
	_ = excessTCP.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = excessTCP.Read(reply[:1])
	if !isEOFOrReset(err) {
		t.Fatalf("excess tcp read err = %v, want immediate close", err)
	}
	waitRuleErr(t, set, tcpRule1.Key(), ErrSessionLimitReached.Error())
	if got := tcpDials.Load(); got != 2 {
		t.Fatalf("tcp dials after refusal = %d, want 2", got)
	}

	_, err = tcpClients[0].Write([]byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	_ = tcpClients[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadFull(tcpClients[0], reply[:1])
	if err != nil || reply[0] != 'b' {
		t.Fatalf("existing tcp session broken: %q, %v", reply[:1], err)
	}

	_ = tcpClients[0].Close()
	deadline := time.Now().Add(2 * time.Second)
	for set.tcpBudget.used.Load() >= 2 {
		if time.Now().After(deadline) {
			t.Fatal("tcp slot was not released after close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	recoveredTCP, err := dialTCP(t, tcpRule1.ListenAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredTCP.Close()
	_, err = recoveredTCP.Write([]byte("c"))
	if err != nil {
		t.Fatal(err)
	}
	_ = recoveredTCP.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = io.ReadFull(recoveredTCP, reply[:1])
	if err != nil || reply[0] != 'c' {
		t.Fatalf("recovered tcp = %q, %v", reply[:1], err)
	}
	waitRuleErr(t, set, tcpRule1.Key(), "")

	deadline = time.Now().Add(2 * time.Second)
	for set.udpBudget.used.Load() >= 4 {
		if time.Now().After(deadline) {
			t.Fatal("udp slot was not released after idle expiry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = excessUDP.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = excessUDP.Write([]byte("up"))
	if err != nil {
		t.Fatal(err)
	}
	n, err = excessUDP.Read(reply)
	if err != nil || string(reply[:n]) != "up" {
		t.Fatalf("recovered udp = %q, %v", reply[:n], err)
	}
	waitRuleErr(t, set, udpRule1.Key(), "")
}

func isEOFOrReset(err error) bool {
	return err != nil && !errors.Is(err, os.ErrDeadlineExceeded)
}
