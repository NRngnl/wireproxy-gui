package socksconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	socks5 "github.com/things-go/go-socks5"
)

var (
	errInjectedLeak = errors.New("secret-token 192.0.2.55:9999 payload=super-secret")
	errInjectedEOF  = io.ErrUnexpectedEOF
)

func TestAssociateHandlerTerminalFailureReportingAndIsolation(t *testing.T) {
	type failureCase struct {
		name string
		run  func(t *testing.T, h *udpAssocHarness)
	}

	cases := []failureCase{
		{
			name: "initial dial failure",
			run: func(t *testing.T, h *udpAssocHarness) {
				h.setDialA(func(context.Context, string, string) (net.Conn, error) {
					return nil, fmt.Errorf("%w: %w", syscall.ECONNREFUSED, errInjectedLeak)
				})
				h.sendTo(t, h.addrA, "drop-dial")
				msg := h.waitError(t)
				assertContains(t, msg, "UDP dial failed")
				assertContains(t, msg, "connection refused")
				assertCleanMessage(t, msg, h.addrA.String(), "drop-dial", "secret-token", "192.0.2.55", "super-secret")
			},
		},
		{
			name: "initial write failure evicts destination",
			run: func(t *testing.T, h *udpAssocHarness) {
				var dials atomic.Int32
				var failWrite atomic.Bool
				failWrite.Store(true)
				h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
					n := dials.Add(1)
					c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
					if err != nil {
						return nil, err
					}
					if n == 1 {
						return &injectedConn{
							Conn: c,
							writeFn: func([]byte) (int, error) {
								if failWrite.Load() {
									return 0, fmt.Errorf("%w: %w", context.DeadlineExceeded, errInjectedLeak)
								}
								return 0, nil
							},
						}, nil
					}
					return c, nil
				})
				h.sendTo(t, h.addrA, "drop-write")
				msg := h.waitError(t)
				assertContains(t, msg, "UDP write failed")
				assertContains(t, msg, "timeout")
				assertCleanMessage(t, msg, h.addrA.String(), "drop-write", "secret-token")

				failWrite.Store(false)
				h.sendTo(t, h.addrA, "after-evict")
				h.expectTargetRecv(t, h.targetA, "after-evict")
				if got := dials.Load(); got != 2 {
					t.Fatalf("dials after eviction = %d, want 2", got)
				}
			},
		},
		{
			name: "recovery redial failure including live closed error",
			run: func(t *testing.T, h *udpAssocHarness) {
				var dials atomic.Int32
				h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
					n := dials.Add(1)
					if n == 1 {
						c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
						if err != nil {
							return nil, err
						}
						return &injectedConn{
							Conn: c,
							writeFn: func([]byte) (int, error) {
								return 0, net.ErrClosed
							},
						}, nil
					}
					return nil, net.ErrClosed
				})
				h.sendTo(t, h.addrA, "drop-redial")
				msg := h.waitError(t)
				assertContains(t, msg, "UDP redial failed")
				assertContains(t, msg, "connection closed")
				assertCleanMessage(t, msg, h.addrA.String(), "drop-redial")
			},
		},
		{
			name: "retry write failure evicts replacement",
			run: func(t *testing.T, h *udpAssocHarness) {
				var dials atomic.Int32
				h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
					n := dials.Add(1)
					c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
					if err != nil {
						return nil, err
					}
					switch n {
					case 1:
						return &injectedConn{
							Conn: c,
							writeFn: func([]byte) (int, error) {
								return 0, net.ErrClosed
							},
						}, nil
					case 2:
						return &injectedConn{
							Conn: c,
							writeFn: func([]byte) (int, error) {
								return 0, errInjectedLeak
							},
						}, nil
					default:
						return c, nil
					}
				})
				h.sendTo(t, h.addrA, "drop-retry-write")
				msg := h.waitError(t)
				assertContains(t, msg, "UDP retry write failed")
				assertContains(t, msg, "unexpected error")
				assertCleanMessage(t, msg, h.addrA.String(), "drop-retry-write", "secret-token", "192.0.2.55", "super-secret")

				h.sendTo(t, h.addrA, "after-replacement-evict")
				h.expectTargetRecv(t, h.targetA, "after-replacement-evict")
				if got := dials.Load(); got != 3 {
					t.Fatalf("dials after replacement eviction = %d, want 3", got)
				}
			},
		},
		{
			name: "retry write live closed error is not suppressed",
			run: func(t *testing.T, h *udpAssocHarness) {
				var dials atomic.Int32
				h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
					n := dials.Add(1)
					c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
					if err != nil {
						return nil, err
					}
					if n <= 2 {
						return &injectedConn{
							Conn: c,
							writeFn: func([]byte) (int, error) {
								return 0, net.ErrClosed
							},
						}, nil
					}
					return c, nil
				})
				h.sendTo(t, h.addrA, "drop-retry-closed")
				msg := h.waitError(t)
				assertContains(t, msg, "UDP retry write failed")
				assertContains(t, msg, "connection closed")
				assertCleanMessage(t, msg, h.addrA.String(), "drop-retry-closed")
			},
		},
		{
			name: "reply write failure keeps shared socket alive",
			run: func(t *testing.T, h *udpAssocHarness) {
				var replied atomic.Bool
				h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
					c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
					if err != nil {
						return nil, err
					}
					return &injectedConn{
						Conn: c,
						readFn: func(b []byte) (int, error) {
							if replied.CompareAndSwap(false, true) {
								const oversized = 65500
								for i := range oversized {
									b[i] = 'R'
								}
								return oversized, nil
							}
							return c.Read(b)
						},
					}, nil
				})
				h.sendTo(t, h.addrA, "trigger-reply")
				h.expectTargetRecv(t, h.targetA, "trigger-reply")
				msg := h.waitError(t)
				assertContains(t, msg, "UDP reply write failed")
				assertContains(t, msg, "message too long")
				assertCleanMessage(t, msg, h.addrA.String(), "trigger-reply")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
			h.sendTo(t, h.addrB, "warm-b")
			srcWarm := h.expectTargetRecv(t, h.targetB, "warm-b")
			_, err := h.targetB.WriteToUDP([]byte("warm-reply-b"), srcWarm)
			if err != nil {
				t.Fatal(err)
			}
			h.expectClientReply(t, "warm-reply-b")

			tc.run(t, h)

			h.sendTo(t, h.addrB, "ok-b")
			src := h.expectTargetRecv(t, h.targetB, "ok-b")
			_, err = h.targetB.WriteToUDP([]byte("reply-b"), src)
			if err != nil {
				t.Fatal(err)
			}
			h.expectClientReply(t, "reply-b")
			h.assertNoMoreErrors(t, 40*time.Millisecond)
		})
	}
}

func TestAssociateHandlerReadFailureReporting(t *testing.T) {
	t.Run("client socket read error", func(t *testing.T) {
		var injected atomic.Bool
		listenUDP := func(laddr *net.UDPAddr) (udpBindConn, error) {
			raw, err := net.ListenUDP("udp", laddr)
			if err != nil {
				return nil, err
			}
			return &injectedBindConn{
				UDPConn: raw,
				readFn: func(b []byte) (int, *net.UDPAddr, error) {
					if injected.CompareAndSwap(false, true) {
						return 0, nil, fmt.Errorf("%w: %w", syscall.ECONNRESET, errInjectedLeak)
					}
					return raw.ReadFromUDP(b)
				},
			}, nil
		}

		h := newUDPAssocHarness(t, listenUDP, defaultUDPErrorInterval)
		msg := h.waitError(t)
		assertContains(t, msg, "UDP client read failed")
		assertContains(t, msg, "connection reset")
		assertCleanMessage(t, msg, "secret-token", "192.0.2.55")

		h.sendTo(t, h.addrB, "still-works")
		h.expectTargetRecv(t, h.targetB, "still-works")
	})

	t.Run("target read error", func(t *testing.T) {
		h := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		delivered := make(chan struct{})
		h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			return &injectedConn{
				Conn: c,
				readFn: func([]byte) (int, error) {
					select {
					case <-delivered:
						return 0, fmt.Errorf("%w: %w", errInjectedEOF, errInjectedLeak)
					case <-h.ctx.Done():
						return 0, net.ErrClosed
					}
				},
			}, nil
		})

		h.sendTo(t, h.addrA, "ping-a")
		h.expectTargetRecv(t, h.targetA, "ping-a")
		close(delivered)
		msg := h.waitError(t)
		assertContains(t, msg, "UDP target read failed")
		assertContains(t, msg, "unexpected EOF")
		assertCleanMessage(t, msg, h.addrA.String(), "ping-a", "secret-token")

		h.sendTo(t, h.addrB, "ok-b")
		h.expectTargetRecv(t, h.targetB, "ok-b")
		h.assertNoMoreErrors(t, 40*time.Millisecond)
	})

	t.Run("premature backend timeout is reported", func(t *testing.T) {
		h := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		delivered := make(chan struct{})
		h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			return WrapUDP(&injectedConn{
				Conn: c,
				readFn: func([]byte) (int, error) {
					select {
					case <-delivered:
						return 0, &net.OpError{Op: "read", Net: "udp", Err: backendTimeoutError{}}
					case <-h.ctx.Done():
						return 0, net.ErrClosed
					}
				},
			}, 2*time.Second), nil
		})

		h.sendTo(t, h.addrA, "ping-a")
		h.expectTargetRecv(t, h.targetA, "ping-a")
		close(delivered)

		msg := h.waitError(t)
		assertContains(t, msg, "UDP target read failed")
		assertContains(t, msg, "timeout")
		assertCleanMessage(t, msg, h.addrA.String(), "ping-a")
		h.assertNoMoreErrors(t, 40*time.Millisecond)
	})

	t.Run("unowned backend timeout without idle deadline is reported", func(t *testing.T) {
		h := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		delivered := make(chan struct{})
		h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			return WrapUDP(&injectedConn{
				Conn: c,
				readFn: func([]byte) (int, error) {
					select {
					case <-delivered:
						return 0, &net.OpError{Op: "read", Net: "udp", Err: backendTimeoutError{}}
					case <-h.ctx.Done():
						return 0, net.ErrClosed
					}
				},
			}, 0), nil
		})

		h.sendTo(t, h.addrA, "ping-a")
		h.expectTargetRecv(t, h.targetA, "ping-a")
		close(delivered)

		msg := h.waitError(t)
		assertContains(t, msg, "UDP target read failed")
		assertContains(t, msg, "timeout")
		assertCleanMessage(t, msg, h.addrA.String(), "ping-a")
		h.assertNoMoreErrors(t, 40*time.Millisecond)
	})
}

func TestAssociateHandlerQuietPaths(t *testing.T) {
	t.Run("expected idle read expiry stays quiet", func(t *testing.T) {
		h := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		var dials atomic.Int32
		closed := make(chan struct{}, 2)
		h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			return &closeTrackingConn{
				Conn:   WrapUDP(c, 35*time.Millisecond),
				closed: closed,
			}, nil
		})

		h.sendTo(t, h.addrA, "first")
		h.expectTargetRecv(t, h.targetA, "first")

		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("idle UDP target did not expire")
		}

		h.sendTo(t, h.addrA, "second")
		h.expectTargetRecv(t, h.targetA, "second")
		if got := dials.Load(); got != 2 {
			t.Fatalf("dials = %d, want 2", got)
		}
		h.assertNoMoreErrors(t, 50*time.Millisecond)
	})

	t.Run("expected backend-style idle read expiry stays quiet", func(t *testing.T) {
		h := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		var dials atomic.Int32
		closed := make(chan struct{}, 2)
		h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			return &closeTrackingConn{
				Conn:   WrapUDP(&backendTimeoutConn{Conn: c}, 35*time.Millisecond),
				closed: closed,
			}, nil
		})

		h.sendTo(t, h.addrA, "first")
		h.expectTargetRecv(t, h.targetA, "first")

		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("idle backend UDP target did not expire")
		}

		h.sendTo(t, h.addrA, "second")
		h.expectTargetRecv(t, h.targetA, "second")
		if got := dials.Load(); got != 2 {
			t.Fatalf("dials = %d, want 2", got)
		}
		h.assertNoMoreErrors(t, 50*time.Millisecond)
	})

	t.Run("successful stale-socket recovery stays quiet", func(t *testing.T) {
		h := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		var dials atomic.Int32
		h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			n := dials.Add(1)
			c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			if n == 1 {
				return &injectedConn{
					Conn: c,
					writeFn: func([]byte) (int, error) {
						return 0, net.ErrClosed
					},
				}, nil
			}
			return c, nil
		})

		h.sendTo(t, h.addrA, "stale-recover")
		h.expectTargetRecv(t, h.targetA, "stale-recover")
		if got := dials.Load(); got != 2 {
			t.Fatalf("dials = %d, want 2", got)
		}
		h.assertNoMoreErrors(t, 50*time.Millisecond)
	})

	t.Run("TCP control closure and parent cancellation stay quiet", func(t *testing.T) {
		hCtrl := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		hCtrl.sendTo(t, hCtrl.addrA, "active-ctrl")
		hCtrl.expectTargetRecv(t, hCtrl.targetA, "active-ctrl")
		_ = hCtrl.controlConn.Close()
		hCtrl.waitHandlerExit(t)
		hCtrl.assertNoMoreErrors(t, 40*time.Millisecond)

		hCtx := newUDPAssocHarness(t, nil, defaultUDPErrorInterval)
		hCtx.sendTo(t, hCtx.addrA, "active-ctx")
		hCtx.expectTargetRecv(t, hCtx.targetA, "active-ctx")
		hCtx.cancel()
		hCtx.waitHandlerExit(t)
		hCtx.assertNoMoreErrors(t, 40*time.Millisecond)
	})
}

func TestAssociateHandlerRateLimitsAndFlushesRepeats(t *testing.T) {
	const interval = 100 * time.Millisecond
	h := newUDPAssocHarness(t, nil, interval)
	h.setDialA(func(context.Context, string, string) (net.Conn, error) {
		return nil, context.DeadlineExceeded
	})

	for range 4 {
		h.sendTo(t, h.addrA, "burst-1")
	}

	first := h.waitError(t)
	assertContains(t, first, "UDP dial failed")
	assertContains(t, first, "timeout")
	if strings.Contains(first, "repeat") {
		t.Fatalf("first emission should not carry repeat count, got %q", first)
	}

	flushed := h.waitError(t)
	assertContains(t, flushed, "UDP dial failed")
	assertContains(t, flushed, "timeout")
	assertContains(t, flushed, "3 repeats")

	h.sendTo(t, h.addrA, "burst-2a")
	secondImmediate := h.waitErrorWithin(t, interval/2)
	assertContains(t, secondImmediate, "UDP dial failed")
	assertContains(t, secondImmediate, "timeout")
	if strings.Contains(secondImmediate, "repeat") {
		t.Fatalf("first failure after flush should emit immediately without repeat count, got %q", secondImmediate)
	}

	h.sendTo(t, h.addrA, "burst-2b")
	time.Sleep(10 * time.Millisecond)
	_ = h.controlConn.Close()
	h.waitHandlerExit(t)

	teardownFlush := h.waitError(t)
	assertContains(t, teardownFlush, "UDP dial failed")
	assertContains(t, teardownFlush, "timeout")
	assertContains(t, teardownFlush, "1 repeat")
	h.assertNoMoreErrors(t, 40*time.Millisecond)
}

func TestAssociateHandlerRateLimitRegressions(t *testing.T) {
	t.Run("flush then first failure emits immediately", func(t *testing.T) {
		const interval = 120 * time.Millisecond
		h := newUDPAssocHarness(t, nil, interval)
		h.setDialA(func(context.Context, string, string) (net.Conn, error) {
			return nil, syscall.ECONNREFUSED
		})

		h.sendTo(t, h.addrA, "first")
		first := h.waitErrorWithin(t, interval/2)
		assertContains(t, first, "UDP dial failed")
		assertContains(t, first, "connection refused")
		if strings.Contains(first, "repeat") {
			t.Fatalf("initial failure carried repeat: %q", first)
		}

		h.sendTo(t, h.addrA, "second")
		flushed := h.waitError(t)
		assertContains(t, flushed, "UDP dial failed")
		assertContains(t, flushed, "connection refused")
		assertContains(t, flushed, "1 repeat")

		h.sendTo(t, h.addrA, "after-flush")
		postFlush := h.waitErrorWithin(t, interval/2)
		assertContains(t, postFlush, "UDP dial failed")
		assertContains(t, postFlush, "connection refused")
		if strings.Contains(postFlush, "repeat") {
			t.Fatalf("first failure after flush should not carry repeat: %q", postFlush)
		}
		h.assertNoMoreErrors(t, interval+40*time.Millisecond)
	})

	t.Run("mixed reasons retain distinct allowlisted categories and counts", func(t *testing.T) {
		const interval = 120 * time.Millisecond
		h := newUDPAssocHarness(t, nil, interval)
		var dials atomic.Int32
		h.setDialA(func(context.Context, string, string) (net.Conn, error) {
			if dials.Add(1) <= 2 {
				return nil, fmt.Errorf("%w: %w", syscall.ECONNREFUSED, errInjectedLeak)
			}
			return nil, fmt.Errorf("%w: %w", context.DeadlineExceeded, errInjectedLeak)
		})

		h.sendTo(t, h.addrA, "refused-1")
		gotRefused := h.waitErrorWithin(t, interval/2)
		assertContains(t, gotRefused, "UDP dial failed")
		assertContains(t, gotRefused, "connection refused")
		assertCleanMessage(t, gotRefused, h.addrA.String(), "secret-token")

		h.sendTo(t, h.addrA, "refused-2")

		h.sendTo(t, h.addrA, "timeout-1")
		gotTimeout := h.waitErrorWithin(t, interval/2)
		assertContains(t, gotTimeout, "UDP dial failed")
		assertContains(t, gotTimeout, "timeout")
		assertCleanMessage(t, gotTimeout, h.addrA.String(), "secret-token")

		h.sendTo(t, h.addrA, "timeout-2")

		summaries := []string{h.waitError(t), h.waitError(t)}
		var sawRefusedRepeat, sawTimeoutRepeat bool
		for _, msg := range summaries {
			assertCleanMessage(t, msg, h.addrA.String(), "secret-token")
			if strings.Contains(msg, "UDP dial failed: connection refused (1 repeat)") {
				sawRefusedRepeat = true
			}
			if strings.Contains(msg, "UDP dial failed: timeout (1 repeat)") {
				sawTimeoutRepeat = true
			}
		}
		if !sawRefusedRepeat || !sawTimeoutRepeat {
			t.Fatalf("summaries = %q, want both connection refused (1 repeat) and timeout (1 repeat)", summaries)
		}
		h.assertNoMoreErrors(t, 40*time.Millisecond)
	})

	t.Run("rearms timer when another operation summary is due earlier", func(t *testing.T) {
		const interval = 160 * time.Millisecond
		h := newUDPAssocHarness(t, nil, interval)
		var failWrite atomic.Bool
		h.setDialA(func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			if !failWrite.Load() {
				return nil, syscall.ECONNREFUSED
			}
			c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			return &injectedConn{
				Conn: c,
				writeFn: func([]byte) (int, error) {
					return 0, syscall.ECONNRESET
				},
			}, nil
		})

		h.sendTo(t, h.addrA, "dial-1")
		dialImmediate := h.waitErrorWithin(t, interval/2)
		assertContains(t, dialImmediate, "UDP dial failed")
		assertContains(t, dialImmediate, "connection refused")

		time.Sleep(90 * time.Millisecond)

		failWrite.Store(true)
		h.sendTo(t, h.addrA, "write-1")
		writeImmediate := h.waitErrorWithin(t, interval/2)
		assertContains(t, writeImmediate, "UDP write failed")
		assertContains(t, writeImmediate, "connection reset")

		h.sendTo(t, h.addrA, "write-2")
		time.Sleep(5 * time.Millisecond)

		failWrite.Store(false)
		h.sendTo(t, h.addrA, "dial-2")

		dialSummary := h.waitErrorWithin(t, 95*time.Millisecond)
		assertContains(t, dialSummary, "UDP dial failed")
		assertContains(t, dialSummary, "connection refused")
		assertContains(t, dialSummary, "1 repeat")

		writeSummary := h.waitErrorWithin(t, 150*time.Millisecond)
		assertContains(t, writeSummary, "UDP write failed")
		assertContains(t, writeSummary, "connection reset")
		assertContains(t, writeSummary, "1 repeat")

		_ = h.controlConn.Close()
		h.waitHandlerExit(t)
		h.assertNoMoreErrors(t, 40*time.Millisecond)
	})
}

type udpAssocHarness struct {
	ctx         context.Context
	cancel      context.CancelFunc
	controlConn net.Conn
	udpClient   *net.UDPConn
	targetA     *net.UDPConn
	targetB     *net.UDPConn
	addrA       *net.UDPAddr
	addrB       *net.UDPAddr
	errCh       chan string
	doneCh      chan struct{}

	mu    sync.Mutex
	dialA func(context.Context, string, string) (net.Conn, error)
}

func newUDPAssocHarness(t *testing.T, listenUDP func(*net.UDPAddr) (udpBindConn, error), interval time.Duration) *udpAssocHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	var lc net.ListenConfig
	rawLn, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyLn := WrapListener(rawLn, time.Second, time.Second)
	stopLn := context.AfterFunc(ctx, func() {
		_ = proxyLn.Close()
	})
	t.Cleanup(func() {
		stopLn()
		_ = proxyLn.Close()
	})

	targetA, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = targetA.Close() })

	targetB, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = targetB.Close() })

	h := &udpAssocHarness{
		ctx:     ctx,
		cancel:  cancel,
		targetA: targetA,
		targetB: targetB,
		addrA:   targetA.LocalAddr().(*net.UDPAddr),
		addrB:   targetB.LocalAddr().(*net.UDPAddr),
		errCh:   make(chan string, 32),
		doneCh:  make(chan struct{}, 1),
	}

	var dialer net.Dialer
	dialFn := func(dialCtx context.Context, network, addr string) (net.Conn, error) {
		if addr == h.addrA.String() {
			h.mu.Lock()
			fn := h.dialA
			h.mu.Unlock()
			if fn != nil {
				return fn(dialCtx, network, addr)
			}
		}
		return dialer.DialContext(dialCtx, network, addr)
	}

	assocHandle := associateHandler(ctx, dialFn, func(reportErr error) {
		if reportErr != nil {
			h.errCh <- reportErr.Error()
		}
	}, listenUDP, interval)

	server := socks5.NewServer(
		socks5.WithDial(dialFn),
		socks5.WithAssociateHandle(func(reqCtx context.Context, writer io.Writer, request *socks5.Request) error {
			err := assocHandle(reqCtx, writer, request)
			select {
			case h.doneCh <- struct{}{}:
			default:
			}
			return err
		}),
		socks5.WithAssociateMiddleware(func(_ context.Context, writer io.Writer, _ *socks5.Request) error {
			if c, ok := writer.(interface{ ClearIdleTimeout() error }); ok {
				return c.ClearIdleTimeout()
			}
			return nil
		}),
		socks5.WithRule(socks5.NewPermitConnAndAss()),
	)

	go func() {
		_ = server.Serve(proxyLn)
	}()

	controlConn, err := dialer.DialContext(ctx, "tcp", proxyLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlConn.Close() })
	_ = controlConn.SetDeadline(time.Now().Add(time.Second))

	_, err = controlConn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadFull(controlConn, make([]byte, 2))
	if err != nil {
		t.Fatal(err)
	}
	_, err = controlConn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	_, err = io.ReadFull(controlConn, reply)
	if err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("associate reply = %#v, want success", reply)
	}
	_ = controlConn.SetDeadline(time.Time{})

	relayPort := int(reply[8])<<8 | int(reply[9])
	udpClient, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: relayPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udpClient.Close() })

	h.controlConn = controlConn
	h.udpClient = udpClient
	return h
}

func (h *udpAssocHarness) setDialA(fn func(context.Context, string, string) (net.Conn, error)) {
	h.mu.Lock()
	h.dialA = fn
	h.mu.Unlock()
}

func (h *udpAssocHarness) sendTo(t *testing.T, dst *net.UDPAddr, payload string) {
	t.Helper()
	pkt := []byte{0, 0, 0, 0x01, 127, 0, 0, 1, byte(dst.Port >> 8), byte(dst.Port)}
	pkt = append(pkt, payload...)
	_, err := h.udpClient.Write(pkt)
	if err != nil {
		t.Fatal(err)
	}
}

func (h *udpAssocHarness) expectTargetRecv(t *testing.T, target *net.UDPConn, want string) *net.UDPAddr {
	t.Helper()
	_ = target.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	rn, src, err := target.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("target read %q = %v", want, err)
	}
	if string(buf[:rn]) != want {
		t.Fatalf("target payload = %q, want %q", buf[:rn], want)
	}
	return src
}

func (h *udpAssocHarness) expectClientReply(t *testing.T, want string) {
	t.Helper()
	_ = h.udpClient.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	rn, err := h.udpClient.Read(buf)
	if err != nil {
		t.Fatalf("client read %q = %v", want, err)
	}
	if rn < 10 || string(buf[10:rn]) != want {
		t.Fatalf("client reply = %q, want %q", buf[10:rn], want)
	}
}

func (h *udpAssocHarness) waitError(t *testing.T) string {
	t.Helper()
	return h.waitErrorWithin(t, time.Second)
}

func (h *udpAssocHarness) waitErrorWithin(t *testing.T, timeout time.Duration) string {
	t.Helper()
	select {
	case msg := <-h.errCh:
		return msg
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for reported UDP error", timeout)
		return ""
	}
}

func (h *udpAssocHarness) assertNoMoreErrors(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case msg := <-h.errCh:
		t.Fatalf("unexpected reported UDP error: %q", msg)
	case <-time.After(wait):
	}
}

func (h *udpAssocHarness) waitHandlerExit(t *testing.T) {
	t.Helper()
	select {
	case <-h.doneCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for AssociateHandler exit")
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("message %q does not contain %q", s, substr)
	}
}

func assertCleanMessage(t *testing.T, msg string, forbidden ...string) {
	t.Helper()
	for _, item := range forbidden {
		if item != "" && strings.Contains(msg, item) {
			t.Fatalf("message %q leaked forbidden content %q", msg, item)
		}
	}
}

type injectedConn struct {
	net.Conn
	readFn  func([]byte) (int, error)
	writeFn func([]byte) (int, error)
}

func (c *injectedConn) Read(b []byte) (int, error) {
	if c.readFn != nil {
		return c.readFn(b)
	}
	return c.Conn.Read(b)
}

func (c *injectedConn) Write(b []byte) (int, error) {
	if c.writeFn != nil {
		return c.writeFn(b)
	}
	return c.Conn.Write(b)
}

type injectedBindConn struct {
	*net.UDPConn
	readFn func([]byte) (int, *net.UDPAddr, error)
}

func (c *injectedBindConn) ReadFromUDP(b []byte) (int, *net.UDPAddr, error) {
	if c.readFn != nil {
		return c.readFn(b)
	}
	return c.UDPConn.ReadFromUDP(b)
}

type closeTrackingConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeTrackingConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.closed <- struct{}{}
	})
	return err
}

type backendTimeoutError struct{}

func (backendTimeoutError) Error() string   { return "i/o timeout" }
func (backendTimeoutError) Timeout() bool   { return true }
func (backendTimeoutError) Temporary() bool { return true }

type backendTimeoutConn struct {
	net.Conn
}

func (c *backendTimeoutConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, &net.OpError{Op: "read", Net: "udp", Err: backendTimeoutError{}}
	}
	return n, err
}
