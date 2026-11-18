package localforward

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

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
