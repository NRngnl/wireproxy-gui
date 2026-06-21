package wireproxy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
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
