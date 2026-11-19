package localforward

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

var errDial = errors.New("no route")

func listenTCP(t *testing.T, addr string) (net.Listener, error) {
	t.Helper()
	var lc net.ListenConfig
	return lc.Listen(t.Context(), "tcp", addr)
}

func dialTCP(t *testing.T, addr string) (net.Conn, error) {
	t.Helper()
	var d net.Dialer
	return d.DialContext(t.Context(), "tcp", addr)
}

func echoServer(t *testing.T) string {
	t.Helper()
	l, err := listenTCP(t, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.(*net.TCPConn).CloseWrite()
			}()
		}
	}()
	return l.Addr().String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := listenTCP(t, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func newTestSet(t *testing.T) *Set {
	t.Helper()
	var d net.Dialer
	set := New(t.Context(), Dialer{TCP: func(ctx context.Context, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", addr)
	}}, socksconn.Timeouts{}, nil)
	t.Cleanup(set.Close)
	return set
}

func tcpRule(listen, target string) profile.LocalForward {
	return profile.LocalForward{Protocol: profile.PortForwardTCP, ListenAddr: listen, TargetAddr: target}
}

func TestTCPRelayHalfClose(t *testing.T) {
	target := echoServer(t)
	listen := freeAddr(t)
	set := newTestSet(t)
	set.Apply([]profile.LocalForward{tcpRule(listen, target)})
	conn, err := dialTCP(t, listen)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("ping"))
	_ = conn.(*net.TCPConn).CloseWrite()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != "ping" {
		t.Fatalf("echo = %q, %v", got, err)
	}
}

func TestApplyPortConflictLiveSessionAndRemoval(t *testing.T) {
	target := echoServer(t)
	busy, err := listenTCP(t, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	good := freeAddr(t)
	set := newTestSet(t)
	rules := []profile.LocalForward{tcpRule(busy.Addr().String(), target), tcpRule(good, target)}
	set.Apply(rules)
	states := set.States()
	if states[0].Status != StatusError || states[0].Err != ErrPortInUse.Error() || states[1].Status != StatusActive {
		t.Fatalf("states = %#v", states)
	}
	live, err := dialTCP(t, good)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	set.Apply(rules[1:])
	_, _ = live.Write([]byte("x"))
	buf := make([]byte, 1)
	_ = live.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.ReadFull(live, buf)
	if err != nil {
		t.Fatalf("live session broken by unrelated apply: %v", err)
	}
	set.Apply(nil)
	l, err := listenTCP(t, good)
	if err != nil {
		t.Fatalf("port not freed after removal: %v", err)
	}
	_ = l.Close()
}

func TestUDPRelayAndIdleExpiry(t *testing.T) {
	var lc net.ListenConfig
	echo, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()
	var d net.Dialer
	var dials atomic.Int32
	set := New(t.Context(), Dialer{UDP: func(ctx context.Context, addr string) (net.Conn, error) {
		dials.Add(1)
		return d.DialContext(ctx, "udp", addr)
	}}, socksconn.Timeouts{UDPIdle: 200 * time.Millisecond}, nil)
	defer set.Close()
	probe, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.LocalAddr().String()
	_ = probe.Close()
	set.Apply([]profile.LocalForward{{Protocol: profile.PortForwardUDP, ListenAddr: listen, TargetAddr: echo.LocalAddr().String()}})
	client, err := d.DialContext(t.Context(), "udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	roundTrip := func() {
		_, _ = client.Write([]byte("hi"))
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 16)
		n, err := client.Read(buf)
		if err != nil || string(buf[:n]) != "hi" {
			t.Fatalf("echo = %q, %v", buf[:n], err)
		}
	}
	roundTrip()
	time.Sleep(600 * time.Millisecond)
	roundTrip()
	if dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2 (idle session should expire)", dials.Load())
	}
}

func TestRetargetStaysActiveAndCloseIsTerminal(t *testing.T) {
	first, second := echoServer(t), echoServer(t)
	listen := freeAddr(t)
	set := newTestSet(t)
	set.Apply([]profile.LocalForward{tcpRule(listen, first)})
	set.Apply([]profile.LocalForward{tcpRule(listen, second)})
	if states := set.States(); len(states) != 1 || states[0].Status != StatusActive {
		t.Fatalf("after retarget: %#v", states)
	}
	conn, err := dialTCP(t, listen)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	set.Close()
	set.Apply([]profile.LocalForward{tcpRule(listen, second)})
	l, err := listenTCP(t, listen)
	if err != nil {
		t.Fatalf("Apply after Close rebound the port: %v", err)
	}
	_ = l.Close()
}

func TestDialFailureIsRuleStateNotListenerDeath(t *testing.T) {
	listen := freeAddr(t)
	set := New(t.Context(), Dialer{TCP: func(context.Context, string) (net.Conn, error) {
		return nil, errDial
	}}, socksconn.Timeouts{}, nil)
	defer set.Close()
	set.Apply([]profile.LocalForward{tcpRule(listen, "x:1")})
	conn, err := dialTCP(t, listen)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.ReadAll(conn)
	_ = conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st := set.States()[0]; st.Status == StatusActive && st.Err == errDial.Error() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("states = %#v", set.States())
}

func TestDialErrorClearsAfterSuccess(t *testing.T) {
	target := echoServer(t)
	listen := freeAddr(t)
	var d net.Dialer
	fail := atomic.Bool{}
	fail.Store(true)
	set := New(t.Context(), Dialer{TCP: func(ctx context.Context, addr string) (net.Conn, error) {
		if fail.Load() {
			return nil, errDial
		}
		return d.DialContext(ctx, "tcp", addr)
	}}, socksconn.Timeouts{}, nil)
	defer set.Close()
	set.Apply([]profile.LocalForward{tcpRule(listen, target)})
	use := func() {
		c, err := dialTCP(t, listen)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = c.Write([]byte("x"))
		_, _ = io.ReadAll(c)
		_ = c.Close()
	}
	use()
	waitErr := func(want string) {
		for range 200 {
			if set.States()[0].Err == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("err = %q, want %q", set.States()[0].Err, want)
	}
	waitErr(errDial.Error())
	fail.Store(false)
	use()
	waitErr("")
}

func TestSetCloseDoesNotHangOnUDPSessions(t *testing.T) {
	var lc net.ListenConfig
	probe, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.LocalAddr().String()
	_ = probe.Close()
	var d net.Dialer
	set := New(t.Context(), Dialer{UDP: func(ctx context.Context, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "udp", addr)
	}}, socksconn.Timeouts{}, nil)
	set.Apply([]profile.LocalForward{{Protocol: profile.PortForwardUDP, ListenAddr: listen, TargetAddr: "127.0.0.1:9"}})
	client, err := d.DialContext(t.Context(), "udp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = client.Write([]byte("x"))
	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { set.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung with an active UDP session")
	}
}

func TestCancelReleasesListenersWithoutClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	dialing := make(chan struct{})
	set := New(ctx, Dialer{TCP: func(ctx context.Context, _ string) (net.Conn, error) {
		close(dialing)
		<-ctx.Done()
		return nil, ctx.Err()
	}}, socksconn.Timeouts{}, nil)
	listen := freeAddr(t)
	set.Apply([]profile.LocalForward{tcpRule(listen, "127.0.0.1:1")})
	client, err := dialTCP(t, listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	<-dialing
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		l, err := listenTCP(t, listen)
		if err == nil {
			_ = l.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listen port still bound after the set context was canceled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	set.Close()
}

func TestUDPSourcesKeepSessionsWhileLive(t *testing.T) {
	var lc net.ListenConfig
	echo, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()
	var d net.Dialer
	var dials atomic.Int32
	set := New(t.Context(), Dialer{UDP: func(ctx context.Context, addr string) (net.Conn, error) {
		dials.Add(1)
		return d.DialContext(ctx, "udp", addr)
	}}, socksconn.Timeouts{}, nil)
	defer set.Close()
	probe, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.LocalAddr().String()
	_ = probe.Close()
	set.Apply([]profile.LocalForward{{Protocol: profile.PortForwardUDP, ListenAddr: listen, TargetAddr: echo.LocalAddr().String()}})
	const sources = 600
	clients := make([]net.Conn, sources)
	reply := make([]byte, 8)
	for i := range clients {
		c, err := d.DialContext(t.Context(), "udp", listen)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		clients[i] = c
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = c.Write([]byte("x"))
		_, err = c.Read(reply)
		if err != nil {
			t.Fatalf("source %d: %v", i, err)
		}
	}
	_ = clients[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = clients[0].Write([]byte("y"))
	_, err = clients[0].Read(reply)
	if err != nil {
		t.Fatal(err)
	}
	if n := dials.Load(); n != sources {
		t.Fatalf("dials = %d, want %d: a live source lost its session", n, sources)
	}
}

func TestApplyDoesNotWaitForReleasedRuleSessions(t *testing.T) {
	dialing := make(chan struct{})
	release := make(chan struct{})
	set := New(t.Context(), Dialer{TCP: func(context.Context, string) (net.Conn, error) {
		close(dialing)
		<-release
		return nil, errDial
	}}, socksconn.Timeouts{}, nil)
	defer set.Close()
	defer close(release)
	listen := freeAddr(t)
	set.Apply([]profile.LocalForward{tcpRule(listen, "127.0.0.1:1")})
	client, err := dialTCP(t, listen)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	<-dialing
	done := make(chan struct{})
	go func() { set.Apply(nil); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Apply waited for a released rule's session")
	}
}
