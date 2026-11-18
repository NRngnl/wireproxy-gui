package wireproxy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
	upstream "github.com/windtf/wireproxy"
)

func sampleConfig(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`[Interface]
Address = 10.2.0.2/32
PrivateKey = %s

[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0
`, ephemeralWireGuardKey(t), ephemeralWireGuardKey(t))
}

func TestWireproxyConfigReplacesSocks5Section(t *testing.T) {
	item := profile.New("demo", sampleConfig(t)+"\n[Socks5]\nBindAddress = 127.0.0.1:9999\n", 1085)

	got := wireproxyConfig(item)
	if strings.Count(got, "[Socks5]") != 1 {
		t.Fatalf("expected one Socks5 section, got:\n%s", got)
	}
	if !strings.Contains(got, "BindAddress = 127.0.0.1:1085") || strings.Contains(got, "9999") {
		t.Fatalf("unexpected generated config:\n%s", got)
	}
}

func TestWireproxyPersistentKeepaliveBehavior(t *testing.T) {
	t.Run("defaults omitted peer keepalive to 25 and preserves explicit values", func(t *testing.T) {
		cfg := fmt.Sprintf(`[Interface]
Address = 10.2.0.2/32
PrivateKey = %s

[Peer]
PublicKey = %s
AllowedIPs = 0.0.0.0/0

[Peer]
PublicKey = %s
PersistentKeepalive = 0
AllowedIPs = 10.3.0.0/24

[Peer]
PublicKey = %s
persistentkeepalive = 15
AllowedIPs = 10.4.0.0/24
`, ephemeralWireGuardKey(t), ephemeralWireGuardKey(t), ephemeralWireGuardKey(t), ephemeralWireGuardKey(t))

		parsed, err := parseProfileConfig(profile.New("demo", cfg, 18090))
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.Device.Peers) != 3 {
			t.Fatalf("peers = %d, want 3", len(parsed.Device.Peers))
		}
		if parsed.Device.Peers[0].KeepAlive != 25 {
			t.Fatalf("peer 0 KeepAlive = %d, want 25", parsed.Device.Peers[0].KeepAlive)
		}
		if parsed.Device.Peers[1].KeepAlive != 0 {
			t.Fatalf("peer 1 KeepAlive = %d, want 0", parsed.Device.Peers[1].KeepAlive)
		}
		if parsed.Device.Peers[2].KeepAlive != 15 {
			t.Fatalf("peer 2 KeepAlive = %d, want 15", parsed.Device.Peers[2].KeepAlive)
		}
	})

	t.Run("rejects malformed explicit values loudly", func(t *testing.T) {
		for _, raw := range []string{"abc", "", "-1", "65536"} {
			t.Run("value_"+raw, func(t *testing.T) {
				cfg := sampleConfig(t) + "PersistentKeepalive = " + raw + "\n"
				runner := newTestRunner()
				err := runner.Start(t.Context(), profile.New("bad-keepalive", cfg, 18091))
				if !errors.Is(err, ErrConfigInvalid) {
					t.Fatalf("Start(%q) error = %v, want ErrConfigInvalid", raw, err)
				}
			})
		}
	})
}

func ephemeralWireGuardKey(t *testing.T) string {
	t.Helper()
	var key [32]byte
	_, err := rand.Read(key[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key[:])
}

var errServeFailed = errors.New("serve failed")

func TestStartRejectsInvalidGeneratedConfig(t *testing.T) {
	runner := newTestRunner()
	p := profile.New("bad", strings.Replace(sampleConfig(t), "PrivateKey =", "PrivateKey = not-base64 #", 1), 18080)

	err := runner.Start(context.Background(), p)
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("expected ErrConfigInvalid, got %v", err)
	}
	if runner.Running(p.ID) {
		t.Fatal("profile should not be running after failed config parse")
	}
}

func TestStartRejectsDuplicateRunningProfile(t *testing.T) {
	runner := newTestRunner()
	p := profile.New("demo", sampleConfig(t), 18081)
	ctx := t.Context()

	err := runner.Start(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.StopAll()

	err = runner.Start(ctx, p)
	if !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("expected ErrAlreadyConnected, got %v", err)
	}
}

func TestStopCancelsRunningProfile(t *testing.T) {
	runner := newTestRunner()
	p := profile.New("demo", sampleConfig(t), 18082)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.Running(p.ID) {
		t.Fatal("profile should be running")
	}

	if !runner.Stop(p.ID) {
		t.Fatal("expected Stop to cancel profile")
	}

	deadline := time.After(2 * time.Second)
	for runner.Running(p.ID) {
		select {
		case <-deadline:
			t.Fatal("profile should not remain running after Stop")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestStopCancelsProfileDuringStartup(t *testing.T) {
	blockStart := make(chan struct{})
	runner := newTestRunner()
	runner.startWireguard = func(_ *upstream.Configuration, _ int) (*upstream.VirtualTun, error) {
		<-blockStart
		return &upstream.VirtualTun{}, nil
	}
	p := profile.New("slow", sampleConfig(t), 18083)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.Start(context.Background(), p)
	}()

	deadline := time.After(2 * time.Second)
	for !runner.Running(p.ID) {
		select {
		case <-deadline:
			t.Fatal("profile was not reserved while startup was running")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if !runner.Stop(p.ID) {
		t.Fatal("expected Stop to cancel reserved profile")
	}
	close(blockStart)

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop canceled startup")
	}
	if runner.Running(p.ID) {
		t.Fatal("profile should not remain running after canceled startup")
	}
}

func TestStopAfterWireGuardStartBeforeStartedDoesNotEmitStarted(t *testing.T) {
	runner := newTestRunner()
	p := profile.New("race", sampleConfig(t), 18087)
	runner.startWireguard = func(_ *upstream.Configuration, _ int) (*upstream.VirtualTun, error) {
		if !runner.Stop(p.ID) {
			t.Fatal("expected Stop to cancel reserved profile")
		}
		return &upstream.VirtualTun{}, nil
	}

	err := runner.Start(context.Background(), p)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if runner.Running(p.ID) {
		t.Fatal("profile should not remain running after canceled startup")
	}
	assertNoStartedEvent(t, runner.Events())
}

func TestStopAllAndWaitWaitsForServeShutdown(t *testing.T) {
	serveDone := make(chan struct{})
	runner := newTestRunner()
	runner.serveSocks5 = func(ctx context.Context, _ *upstream.Socks5Config, _ *upstream.VirtualTun, listener net.Listener) error {
		<-ctx.Done()
		_ = listener.Close()
		close(serveDone)
		return ctx.Err()
	}
	p := profile.New("demo", sampleConfig(t), 18085)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}

	err = runner.StopAllAndWait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-serveDone:
	default:
		t.Fatal("StopAllAndWait returned before serve shutdown completed")
	}
	if runner.Running(p.ID) {
		t.Fatal("profile should not remain running after StopAllAndWait")
	}
}

func TestStopAllAndWaitReturnsContextError(t *testing.T) {
	releaseServe := make(chan struct{})
	runner := newTestRunner()
	runner.serveSocks5 = func(ctx context.Context, _ *upstream.Socks5Config, _ *upstream.VirtualTun, listener net.Listener) error {
		<-ctx.Done()
		_ = listener.Close()
		<-releaseServe
		return ctx.Err()
	}
	p := profile.New("demo", sampleConfig(t), 18086)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	err = runner.StopAllAndWait(waitCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline error, got %v", err)
	}

	close(releaseServe)
	err = runner.StopAllAndWait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestServeErrorEmitsErrorEvent(t *testing.T) {
	runner := newTestRunner()
	runner.serveSocks5 = func(context.Context, *upstream.Socks5Config, *upstream.VirtualTun, net.Listener) error {
		return errServeFailed
	}
	p := profile.New("demo", sampleConfig(t), 18084)

	err := runner.Start(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-runner.Events():
			if event.Type != EventError {
				continue
			}
			if event.Message != errServeFailed.Error() {
				t.Fatalf("expected serve error event, got %q", event.Message)
			}
			if runner.Running(p.ID) {
				t.Fatal("profile should not remain running after serve error")
			}
			return
		case <-deadline:
			t.Fatal("expected serve error event")
		}
	}
}

func newTestRunner() *Runner {
	runner := NewRunner()
	runner.listen = func(_, _ string) (net.Listener, error) {
		return newFakeListener(), nil
	}
	runner.startWireguard = func(_ *upstream.Configuration, _ int) (*upstream.VirtualTun, error) {
		return &upstream.VirtualTun{}, nil
	}
	runner.serveSocks5 = func(ctx context.Context, _ *upstream.Socks5Config, _ *upstream.VirtualTun, listener net.Listener) error {
		<-ctx.Done()
		_ = listener.Close()
		return ctx.Err()
	}
	return runner
}

type fakeListener struct {
	closed chan struct{}
	once   sync.Once
}

func newFakeListener() *fakeListener {
	return &fakeListener{closed: make(chan struct{})}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *fakeListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *fakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
}

func TestRunSocks5HalfCloseAndIdleBehavior(t *testing.T) {
	t.Run("target half-close drains client side", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var lc net.ListenConfig
		targetLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer targetLn.Close()

		go func() {
			conn, acceptErr := targetLn.Accept()
			if acceptErr != nil {
				return
			}
			defer conn.Close()
			_, _ = conn.Write([]byte("ok"))
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			_, _ = io.Copy(io.Discard, conn)
		}()

		runner := NewRunner()
		runner.timeouts = socksconn.Timeouts{
			Dial:    time.Second,
			TCPIdle: 600 * time.Millisecond,
			Drain:   60 * time.Millisecond,
			UDPIdle: time.Second,
		}
		var dialer net.Dialer
		runner.dialTun = func(dialCtx context.Context, _ *upstream.VirtualTun, network, addr string) (net.Conn, error) {
			return dialer.DialContext(dialCtx, network, addr)
		}

		proxyLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer proxyLn.Close()

		go func() {
			_ = runner.runSocks5(ctx, &upstream.Socks5Config{}, nil, proxyLn)
		}()

		clientConn := dialSocks5Connect(t, proxyLn.Addr().String(), targetLn.Addr().(*net.TCPAddr))
		defer clientConn.Close()
		err = clientConn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}

		reply := make([]byte, 2)
		_, err = io.ReadFull(clientConn, reply)
		if err != nil {
			t.Fatalf("read reply = %v", err)
		}
		if string(reply) != "ok" {
			t.Fatalf("reply = %q, want ok", reply)
		}

		_, err = clientConn.Read(make([]byte, 1))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("client read after target half-close = %v, want io.EOF before TCPIdle", err)
		}
	})

	t.Run("one-way writes survive idle timeout then close when idle", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var lc net.ListenConfig
		targetLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer targetLn.Close()

		targetEOF := make(chan error, 1)
		go func() {
			conn, acceptErr := targetLn.Accept()
			if acceptErr != nil {
				return
			}
			defer conn.Close()
			_, copyErr := io.Copy(io.Discard, conn)
			targetEOF <- copyErr
		}()

		runner := NewRunner()
		runner.timeouts = socksconn.Timeouts{
			Dial:    time.Second,
			TCPIdle: 80 * time.Millisecond,
			Drain:   300 * time.Millisecond,
			UDPIdle: time.Second,
		}
		var dialer net.Dialer
		runner.dialTun = func(dialCtx context.Context, _ *upstream.VirtualTun, network, addr string) (net.Conn, error) {
			return dialer.DialContext(dialCtx, network, addr)
		}

		proxyLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer proxyLn.Close()

		go func() {
			_ = runner.runSocks5(ctx, &upstream.Socks5Config{}, nil, proxyLn)
		}()

		clientConn := dialSocks5Connect(t, proxyLn.Addr().String(), targetLn.Addr().(*net.TCPAddr))
		defer clientConn.Close()

		for range 8 {
			time.Sleep(20 * time.Millisecond)
			_, err = clientConn.Write([]byte("data"))
			if err != nil {
				t.Fatalf("one-way write failed before idle: %v", err)
			}
			select {
			case err := <-targetEOF:
				t.Fatalf("target saw premature close during active one-way stream: %v", err)
			default:
			}
		}

		select {
		case err := <-targetEOF:
			if err != nil {
				t.Fatalf("target copy after idle = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("connection did not close after stream went idle")
		}
	})
}

func TestRunSocks5UDPAssociateIdleAndRedial(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var lc net.ListenConfig
	proxyLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()

	targetUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer targetUDP.Close()

	var dials atomic.Int32
	closedConns := make(chan struct{}, 4)
	var dialer net.Dialer
	runner := NewRunner()
	runner.timeouts = socksconn.Timeouts{
		Dial:    time.Second,
		TCPIdle: 30 * time.Millisecond,
		Drain:   time.Second,
		UDPIdle: 60 * time.Millisecond,
	}
	runner.dialTun = func(dialCtx context.Context, _ *upstream.VirtualTun, network, addr string) (net.Conn, error) {
		dials.Add(1)
		c, dialErr := dialer.DialContext(dialCtx, network, addr)
		if dialErr != nil {
			return nil, dialErr
		}
		return &closeNotifyConn{Conn: c, closed: closedConns}, nil
	}

	go func() {
		_ = runner.runSocks5(ctx, &upstream.Socks5Config{}, nil, proxyLn)
	}()

	controlConn, relayAddr := dialSocks5UDPAssociate(t, proxyLn.Addr().String())
	defer controlConn.Close()

	udpClient, err := net.DialUDP("udp", nil, relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer udpClient.Close()

	targetAddr := targetUDP.LocalAddr().(*net.UDPAddr)
	sendDatagram := func(payload string) {
		t.Helper()
		pkt := []byte{0, 0, 0, 0x01, 127, 0, 0, 1, byte(targetAddr.Port >> 8), byte(targetAddr.Port)}
		pkt = append(pkt, payload...)
		_, writeErr := udpClient.Write(pkt)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		err = targetUDP.SetReadDeadline(time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		rn, _, readErr := targetUDP.ReadFromUDP(buf)
		if readErr != nil {
			t.Fatalf("target read %q = %v", payload, readErr)
		}
		if string(buf[:rn]) != payload {
			t.Fatalf("got %q, want %q", buf[:rn], payload)
		}
	}

	for range 5 {
		time.Sleep(20 * time.Millisecond)
		sendDatagram("keepalive")
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials during active stream = %d, want 1", got)
	}

	select {
	case <-closedConns:
	case <-time.After(time.Second):
		t.Fatal("idle UDP relay connection did not close")
	}

	time.Sleep(20 * time.Millisecond)
	sendDatagram("after-expiry")
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials after expiry = %d, want 2 (redial)", got)
	}
}

func TestRunSocks5DialTimeoutAndCancellation(t *testing.T) {
	t.Run("bounds hung dial by timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var lc net.ListenConfig
		proxyLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer proxyLn.Close()

		dialResult := make(chan error, 1)
		runner := NewRunner()
		runner.timeouts = socksconn.Timeouts{
			Dial:    40 * time.Millisecond,
			TCPIdle: time.Second,
			Drain:   time.Second,
			UDPIdle: time.Second,
		}
		runner.dialTun = func(dialCtx context.Context, _ *upstream.VirtualTun, _, _ string) (net.Conn, error) {
			<-dialCtx.Done()
			dialResult <- dialCtx.Err()
			return nil, dialCtx.Err()
		}

		go func() {
			_ = runner.runSocks5(ctx, &upstream.Socks5Config{}, nil, proxyLn)
		}()

		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", proxyLn.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))

		_, err = conn.Write([]byte{0x05, 0x01, 0x00})
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadFull(conn, make([]byte, 2))
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x00, 0x50})
		if err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 10)
		_, err = io.ReadFull(conn, reply)
		if err != nil {
			t.Fatal(err)
		}
		if reply[1] == 0x00 {
			t.Fatalf("expected non-success SOCKS5 reply after dial timeout, got %#v", reply)
		}

		select {
		case err := <-dialResult:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("dial ctx err = %v, want context.DeadlineExceeded", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for dial error")
		}
	})

	t.Run("honors earlier runner cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var lc net.ListenConfig
		proxyLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer proxyLn.Close()

		dialStarted := make(chan struct{})
		dialResult := make(chan error, 1)
		runner := NewRunner()
		runner.timeouts = socksconn.Timeouts{
			Dial:    30 * time.Second,
			TCPIdle: time.Second,
			Drain:   time.Second,
			UDPIdle: time.Second,
		}
		runner.dialTun = func(dialCtx context.Context, _ *upstream.VirtualTun, _, _ string) (net.Conn, error) {
			close(dialStarted)
			<-dialCtx.Done()
			dialResult <- dialCtx.Err()
			return nil, dialCtx.Err()
		}

		go func() {
			_ = runner.runSocks5(ctx, &upstream.Socks5Config{}, nil, proxyLn)
		}()

		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", proxyLn.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))

		_, err = conn.Write([]byte{0x05, 0x01, 0x00})
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadFull(conn, make([]byte, 2))
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Write([]byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x00, 0x50})
		if err != nil {
			t.Fatal(err)
		}

		<-dialStarted
		cancel()

		select {
		case err := <-dialResult:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("dial ctx err = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("dial was not canceled when runner context canceled")
		}
	})
}

func dialSocks5Connect(t *testing.T, proxyAddr string, target *net.TCPAddr) net.Conn {
	t.Helper()
	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	_, err = io.ReadFull(conn, greeting)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	ip4 := target.IP.To4()
	req := []byte{0x05, 0x01, 0x00, 0x01, ip4[0], ip4[1], ip4[2], ip4[3], byte(target.Port >> 8), byte(target.Port)}
	_, err = conn.Write(req)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	_, err = io.ReadFull(conn, reply)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		_ = conn.Close()
		t.Fatalf("SOCKS5 CONNECT failed: %#v", reply)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

func dialSocks5UDPAssociate(t *testing.T, proxyAddr string) (net.Conn, *net.UDPAddr) {
	t.Helper()
	var dialer net.Dialer
	conn, err := dialer.DialContext(t.Context(), "tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = conn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_, err = io.ReadFull(conn, make([]byte, 2))
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_, err = conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	_, err = io.ReadFull(conn, reply)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		_ = conn.Close()
		t.Fatalf("SOCKS5 UDP ASSOCIATE failed: %#v", reply)
	}
	_ = conn.SetDeadline(time.Time{})
	relayPort := int(reply[8])<<8 | int(reply[9])
	return conn, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: relayPort}
}

type closeNotifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeNotifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.closed <- struct{}{}
	})
	return err
}

func assertNoStartedEvent(t *testing.T, events <-chan Event) {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Type == EventStarted {
				t.Fatalf("unexpected started event: %#v", event)
			}
		default:
			return
		}
	}
}
