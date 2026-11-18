package socksconn

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

var errDeadlineBoom = errors.New("deadline failed")

func TestCloseWriteHalfClosesAndKeepsDrainReadable(t *testing.T) {
	client, remote := newLoopbackTCPPair(t)

	conn := Wrap(client)
	err := conn.(*Conn).CloseWrite()
	if err != nil {
		t.Fatal(err)
	}
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	_, err = remote.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("remote read = %v, want EOF from half-close", err)
	}
	_, err = remote.Write([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadFull(conn, make([]byte, 1))
	if err != nil {
		t.Fatalf("drain read = %v, want data before deadline", err)
	}
}

func TestConnIdleAndHalfCloseBehavior(t *testing.T) {
	t.Run("idle closes silent connection", func(t *testing.T) {
		client, remote := newLoopbackTCPPair(t)
		defer remote.Close()

		conn := WrapTCP(client, 40*time.Millisecond, 200*time.Millisecond)
		_, err := conn.Read(make([]byte, 1))
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read = %v, want os.ErrDeadlineExceeded", err)
		}
	})

	t.Run("one-way writes keep read alive", func(t *testing.T) {
		client, remote := newLoopbackTCPPair(t)
		defer remote.Close()

		go func() {
			buf := make([]byte, 64)
			for {
				_, err := remote.Read(buf)
				if err != nil {
					return
				}
			}
		}()

		conn := WrapTCP(client, 80*time.Millisecond, 200*time.Millisecond)
		readErr := make(chan error, 1)
		go func() {
			_, err := conn.Read(make([]byte, 1))
			readErr <- err
		}()

		for range 8 {
			time.Sleep(20 * time.Millisecond)
			_, err := conn.Write([]byte("ping"))
			if err != nil {
				t.Fatalf("write failed during active one-way stream: %v", err)
			}
			select {
			case err := <-readErr:
				t.Fatalf("read exited prematurely during active writes: %v", err)
			default:
			}
		}

		select {
		case err := <-readErr:
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("read after idle = %v, want os.ErrDeadlineExceeded", err)
			}
		case <-time.After(time.Second):
			t.Fatal("read did not time out after writes stopped")
		}
	})

	t.Run("drain is not extended by reads", func(t *testing.T) {
		client, remote := newLoopbackTCPPair(t)

		conn := WrapTCP(client, 400*time.Millisecond, 60*time.Millisecond)
		err := conn.(*Conn).CloseWrite()
		if err != nil {
			t.Fatal(err)
		}

		stopWrites := make(chan struct{})
		defer close(stopWrites)
		go func() {
			ticker := time.NewTicker(15 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopWrites:
					return
				case <-ticker.C:
					_, err := remote.Write([]byte("x"))
					if err != nil {
						return
					}
				}
			}
		}()

		deadline := time.Now().Add(500 * time.Millisecond)
		buf := make([]byte, 16)
		for time.Now().Before(deadline) {
			_, err := conn.Read(buf)
			if err != nil {
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Fatalf("drain read = %v, want os.ErrDeadlineExceeded", err)
				}
				return
			}
		}
		t.Fatal("half-close drain deadline was extended past expected bound")
	})

	t.Run("clear idle timeout keeps control connection open", func(t *testing.T) {
		client, remote := newLoopbackTCPPair(t)
		defer remote.Close()

		conn := WrapTCP(client, 30*time.Millisecond, 200*time.Millisecond)
		err := conn.(*Conn).ClearIdleTimeout()
		if err != nil {
			t.Fatal(err)
		}

		readErr := make(chan error, 1)
		go func() {
			buf := make([]byte, 1)
			_, err := conn.Read(buf)
			readErr <- err
		}()

		time.Sleep(60 * time.Millisecond)
		select {
		case err := <-readErr:
			t.Fatalf("read exited unexpectedly after ClearIdleTimeout: %v", err)
		default:
		}

		_, err = remote.Write([]byte("z"))
		if err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-readErr:
			if err != nil {
				t.Fatalf("read = %v, want nil", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for cleared read")
		}
	})

	t.Run("preserves deadline errors", func(t *testing.T) {
		fc := &deadlineErrConn{deadlineErr: errDeadlineBoom}

		conn := WrapTCP(fc, 50*time.Millisecond, 50*time.Millisecond)
		_, err := conn.Read(make([]byte, 1))
		if !errors.Is(err, errDeadlineBoom) {
			t.Fatalf("read error = %v, want %v", err, errDeadlineBoom)
		}
		_, err = conn.Write([]byte("a"))
		if !errors.Is(err, errDeadlineBoom) {
			t.Fatalf("write error = %v, want %v", err, errDeadlineBoom)
		}
		err = conn.(*Conn).CloseWrite()
		if !errors.Is(err, errDeadlineBoom) {
			t.Fatalf("closeWrite error = %v, want %v", err, errDeadlineBoom)
		}
	})
}

func TestWrapListenerWrapsAcceptedConnsAndEnablesKeepAlive(t *testing.T) {
	ctx := t.Context()
	rawLn, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := WrapListener(rawLn, 200*time.Millisecond, 50*time.Millisecond)
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- c
		}
	}()

	client, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	serverConn := <-accepted
	defer serverConn.Close()

	err = serverConn.(*Conn).CloseWrite()
	if err != nil {
		t.Fatal(err)
	}
	_, err = serverConn.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("accepted conn drain read = %v, want os.ErrDeadlineExceeded", err)
	}

	kaConn := &keepAliveRecorderConn{}
	_ = WrapTCP(kaConn, time.Second, time.Second)
	if !kaConn.keepAlive || kaConn.period != keepAlivePeriod {
		t.Fatalf("keepAlive=%v period=%v, want true %v", kaConn.keepAlive, kaConn.period, keepAlivePeriod)
	}
}

func TestDialContextBoundsTimeoutAndHonorsCancellation(t *testing.T) {
	t.Run("bounds by timeout", func(t *testing.T) {
		ctx, cancel := DialContext(t.Context(), context.Background(), 30*time.Millisecond)
		defer cancel()

		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("err = %v, want context.DeadlineExceeded", ctx.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("DialContext did not expire")
		}
	})

	t.Run("honors earlier request deadline", func(t *testing.T) {
		reqCtx, reqCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer reqCancel()

		ctx, cancel := DialContext(t.Context(), reqCtx, 30*time.Second)
		defer cancel()

		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("err = %v, want context.DeadlineExceeded", ctx.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("DialContext did not honor earlier request deadline")
		}
	})

	t.Run("honors parent cancellation", func(t *testing.T) {
		parentCtx, parentCancel := context.WithCancel(t.Context())
		ctx, cancel := DialContext(parentCtx, context.Background(), 30*time.Second)
		defer cancel()

		parentCancel()
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", ctx.Err())
			}
		case <-time.After(time.Second):
			t.Fatal("DialContext did not cancel when parent canceled")
		}
	})
}

func newLoopbackTCPPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ctx := t.Context()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- c
		}
	}()

	client, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	remote := <-accepted
	t.Cleanup(func() { _ = remote.Close() })
	return client, remote
}

type deadlineErrConn struct {
	net.Conn
	deadlineErr error
}

func (c *deadlineErrConn) Read(_ []byte) (int, error)         { return 0, nil }
func (c *deadlineErrConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *deadlineErrConn) Close() error                       { return nil }
func (c *deadlineErrConn) LocalAddr() net.Addr                { return nil }
func (c *deadlineErrConn) RemoteAddr() net.Addr               { return nil }
func (c *deadlineErrConn) SetDeadline(_ time.Time) error      { return c.deadlineErr }
func (c *deadlineErrConn) SetReadDeadline(_ time.Time) error  { return c.deadlineErr }
func (c *deadlineErrConn) SetWriteDeadline(_ time.Time) error { return nil }

type keepAliveRecorderConn struct {
	net.Conn
	mu        sync.Mutex
	keepAlive bool
	period    time.Duration
}

func (c *keepAliveRecorderConn) SetKeepAlive(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keepAlive = on
	return nil
}

func (c *keepAliveRecorderConn) SetKeepAlivePeriod(d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.period = d
	return nil
}
