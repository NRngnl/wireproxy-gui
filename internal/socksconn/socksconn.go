// Package socksconn bounds how long proxied SOCKS5 connections and dials may
// stay idle or linger after half-close.
package socksconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	socks5 "github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

var errLocalAddrNotTCP = errors.New("local address is not TCP")

const (
	// DrainTimeout is how long a connection may keep reading after CloseWrite.
	DrainTimeout = 60 * time.Second

	// IdleTimeout is how long a TCP relay may stay idle in both directions before timing out.
	IdleTimeout = 5 * time.Minute

	// UDPIdleTimeout is how long a UDP relay may stay idle before timing out.
	UDPIdleTimeout = 60 * time.Second

	// DialTimeout bounds outbound SOCKS5 and port-forward dial attempts.
	DialTimeout = 30 * time.Second

	// BufferSize is the SOCKS5 copy buffer pool size.
	BufferSize = 32 * 1024

	// UDPBufferSize holds a maximum-size UDP datagram plus its SOCKS5 UDP header.
	UDPBufferSize = 64*1024 + 512

	keepAlivePeriod = 30 * time.Second
)

// Timeouts configures dial, idle, and half-close drain bounds for SOCKS5 and
// port-forward relays. Zero values fall back to package defaults.
type Timeouts struct {
	Dial    time.Duration
	TCPIdle time.Duration
	Drain   time.Duration
	UDPIdle time.Duration
}

// WithDefaults returns a copy of t with non-positive fields set to package defaults.
func (t Timeouts) WithDefaults() Timeouts {
	if t.Dial <= 0 {
		t.Dial = DialTimeout
	}
	if t.TCPIdle <= 0 {
		t.TCPIdle = IdleTimeout
	}
	if t.Drain <= 0 {
		t.Drain = DrainTimeout
	}
	if t.UDPIdle <= 0 {
		t.UDPIdle = UDPIdleTimeout
	}
	return t
}

type idleDeadline struct {
	mu            sync.Mutex
	timeout       time.Duration
	halfClosed    bool
	lastActivity  time.Time
	readDeadline  time.Time
	writeDeadline time.Time
	drainDeadline time.Time
}

func (d *idleDeadline) beforeRead(conn net.Conn, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.halfClosed || d.timeout <= 0 {
		return nil
	}
	if d.lastActivity.IsZero() {
		d.lastActivity = now
	}
	target := d.lastActivity.Add(d.timeout)
	if !d.readDeadline.IsZero() && target.Sub(d.readDeadline) < d.timeout/4 {
		return nil
	}

	err := conn.SetReadDeadline(target)
	if err != nil {
		return err
	}
	d.readDeadline = target
	return nil
}

func (d *idleDeadline) recordActivity(now time.Time) {
	d.mu.Lock()
	if !d.halfClosed && d.timeout > 0 && now.After(d.lastActivity) {
		d.lastActivity = now
	}
	d.mu.Unlock()
}

func (d *idleDeadline) readDeadlineSnapshot() time.Time {
	d.mu.Lock()
	deadline := d.readDeadline
	d.mu.Unlock()
	return deadline
}

func (d *idleDeadline) retryReadDeadline(conn net.Conn, now time.Time) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.halfClosed || d.timeout <= 0 || d.lastActivity.IsZero() {
		return false, nil
	}
	target := d.lastActivity.Add(d.timeout)
	if !now.Before(target) {
		return false, nil
	}

	err := conn.SetReadDeadline(target)
	if err != nil {
		return false, err
	}
	d.readDeadline = target
	return true, nil
}

func (d *idleDeadline) beforeWrite(conn net.Conn, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timeout <= 0 {
		return nil
	}
	if d.halfClosed {
		if d.drainDeadline.IsZero() || (!d.writeDeadline.IsZero() && !d.drainDeadline.Before(d.writeDeadline)) {
			return nil
		}

		err := conn.SetWriteDeadline(d.drainDeadline)
		if err != nil {
			return err
		}
		d.writeDeadline = d.drainDeadline
		return nil
	}
	if now.After(d.lastActivity) {
		d.lastActivity = now
	}
	next := now.Add(d.timeout)
	if !d.writeDeadline.IsZero() && next.Sub(d.writeDeadline) < d.timeout/4 {
		return nil
	}

	err := conn.SetWriteDeadline(next)
	if err != nil {
		return err
	}
	d.writeDeadline = next
	return nil
}

func (d *idleDeadline) afterWrite(conn net.Conn, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.halfClosed || d.timeout <= 0 {
		return nil
	}
	if now.After(d.lastActivity) {
		d.lastActivity = now
	}
	next := now.Add(d.timeout)
	if !d.readDeadline.IsZero() && next.Sub(d.readDeadline) < d.timeout/4 {
		return nil
	}

	err := conn.SetReadDeadline(next)
	if err != nil {
		return err
	}
	d.readDeadline = next
	return nil
}

func (d *idleDeadline) retryWriteDeadline(conn net.Conn, start, now time.Time) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.halfClosed || d.timeout <= 0 {
		return false, nil
	}
	target := start.Add(d.timeout)
	if !now.Before(target) {
		return false, nil
	}

	err := conn.SetWriteDeadline(target)
	if err != nil {
		return false, err
	}
	d.writeDeadline = target
	return true, nil
}

func (d *idleDeadline) enterDrain(conn net.Conn, drainTimeout time.Duration, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.halfClosed {
		return nil
	}
	d.halfClosed = true
	if drainTimeout <= 0 {
		return nil
	}
	drainDeadline := now.Add(drainTimeout)
	if d.timeout > 0 && !d.lastActivity.IsZero() {
		idleLimit := d.lastActivity.Add(d.timeout)
		if idleLimit.Before(drainDeadline) {
			drainDeadline = idleLimit
		}
	} else if !d.readDeadline.IsZero() && d.readDeadline.Before(drainDeadline) {
		drainDeadline = d.readDeadline
	}

	err := conn.SetDeadline(drainDeadline)
	if err != nil {
		return err
	}
	d.drainDeadline = drainDeadline
	d.readDeadline = drainDeadline
	d.writeDeadline = drainDeadline
	return nil
}

func (d *idleDeadline) clear(conn net.Conn) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.timeout = 0
	if d.halfClosed {
		return nil
	}
	d.lastActivity = time.Time{}
	d.readDeadline = time.Time{}
	d.writeDeadline = time.Time{}
	d.drainDeadline = time.Time{}
	return conn.SetDeadline(time.Time{})
}

func readWithIdle(conn net.Conn, idle *idleDeadline, b []byte) (int, error) {
	err := idle.beforeRead(conn, time.Now())
	if err != nil {
		return 0, err
	}
	for {
		n, readErr := conn.Read(b)
		if n > 0 || readErr == nil {
			idle.recordActivity(time.Now())
			return n, readErr
		}
		if errors.Is(readErr, os.ErrDeadlineExceeded) {
			retry, retryErr := idle.retryReadDeadline(conn, time.Now())
			if retryErr != nil {
				return 0, retryErr
			}
			if retry {
				continue
			}
		}
		return n, readErr
	}
}

func writeWithIdle(conn net.Conn, idle *idleDeadline, b []byte) (int, error) {
	start := time.Now()
	err := idle.beforeWrite(conn, start)
	if err != nil {
		return 0, err
	}
	total := 0
	for {
		n, writeErr := conn.Write(b[total:])
		total += n
		if writeErr == nil {
			refreshErr := idle.afterWrite(conn, time.Now())
			if refreshErr != nil {
				return total, refreshErr
			}
			return total, nil
		}
		if n > 0 {
			start = time.Now()
			idle.recordActivity(start)
		}
		if errors.Is(writeErr, os.ErrDeadlineExceeded) {
			retry, retryErr := idle.retryWriteDeadline(conn, start, time.Now())
			if retryErr != nil {
				return total, retryErr
			}
			if retry {
				continue
			}
		}
		return total, writeErr
	}
}

// Conn bounds reads and writes on a TCP connection by an activity-based idle
// deadline and a fixed drain deadline after CloseWrite.
type Conn struct {
	net.Conn

	idle         idleDeadline
	drainTimeout time.Duration
	closeOnce    sync.Once
	onClose      func()
}

// Wrap returns conn wrapped with the default TCP idle and half-close drain timeouts.
func Wrap(conn net.Conn) net.Conn {
	return WrapTCP(conn, IdleTimeout, DrainTimeout)
}

// WrapTCP returns conn with TCP keepalive enabled where supported, an
// activity-based idle deadline, and a bounded drain after CloseWrite.
func WrapTCP(conn net.Conn, idleTimeout, drainTimeout time.Duration) net.Conn {
	enableTCPKeepAlive(conn)
	return &Conn{
		Conn:         conn,
		idle:         idleDeadline{timeout: idleTimeout},
		drainTimeout: drainTimeout,
	}
}

// Read refreshes the idle deadline while open and reads from the underlying connection.
func (c *Conn) Read(b []byte) (int, error) {
	return readWithIdle(c.Conn, &c.idle, b)
}

// Write writes to the underlying connection and refreshes the idle deadline while open.
func (c *Conn) Write(b []byte) (int, error) {
	return writeWithIdle(c.Conn, &c.idle, b)
}

// Close closes the underlying connection and runs any listener tracking cleanup once.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.Conn.Close()
}

// CloseWrite half-closes the connection when supported and locks in the drain deadline.
func (c *Conn) CloseWrite() error {
	var err error
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		err = cw.CloseWrite()
	}
	deadlineErr := c.idle.enterDrain(c.Conn, c.drainTimeout, time.Now())
	if err == nil {
		err = deadlineErr
	}
	return err
}

// ClearIdleTimeout disables the activity-based idle deadline for connections
// whose control channel must remain open without TCP traffic, such as UDP ASSOCIATE.
func (c *Conn) ClearIdleTimeout() error {
	return c.idle.clear(c.Conn)
}

// UDPConn bounds reads and writes on a UDP relay connection by an activity-based idle deadline.
type UDPConn struct {
	net.Conn

	idle idleDeadline
}

// WrapUDP returns conn wrapped with an activity-based idle deadline.
func WrapUDP(conn net.Conn, idleTimeout time.Duration) net.Conn {
	return &UDPConn{
		Conn: conn,
		idle: idleDeadline{timeout: idleTimeout},
	}
}

// Read refreshes the idle deadline and reads from the underlying connection.
func (c *UDPConn) Read(b []byte) (int, error) {
	err := c.idle.beforeRead(c.Conn, time.Now())
	if err != nil {
		return 0, err
	}
	deadline := c.idle.readDeadlineSnapshot()
	for {
		n, readErr := c.Conn.Read(b)
		if n > 0 || readErr == nil {
			c.idle.recordActivity(time.Now())
			return n, readErr
		}
		now := time.Now()
		if !deadline.IsZero() && !now.Before(deadline) && isTimeoutError(readErr) {
			retry, retryErr := c.idle.retryReadDeadline(c.Conn, now)
			if retryErr != nil {
				return 0, retryErr
			}
			if retry {
				deadline = c.idle.readDeadlineSnapshot()
				continue
			}
			return 0, normalizeUDPIdleTimeout(readErr)
		}
		return n, readErr
	}
}

type idleTimeoutError struct {
	err error
}

func (e idleTimeoutError) Error() string {
	return e.err.Error()
}

func (e idleTimeoutError) Unwrap() error {
	return e.err
}

func (e idleTimeoutError) Is(target error) bool {
	return target == os.ErrDeadlineExceeded
}

func isTimeoutError(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	netErr, ok := errors.AsType[net.Error](err)
	return ok && netErr.Timeout()
}

func normalizeUDPIdleTimeout(err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return err
	}
	return idleTimeoutError{err: err}
}

// Write writes to the underlying connection and refreshes the idle deadline.
func (c *UDPConn) Write(b []byte) (int, error) {
	return writeWithIdle(c.Conn, &c.idle, b)
}

// WrapListener returns listener with accepted TCP connections wrapped for
// activity-based idle deadlines, bounded half-close drains, and teardown on Close.
func WrapListener(listener net.Listener, idleTimeout, drainTimeout time.Duration) net.Listener {
	return &drainListener{
		Listener:     listener,
		idleTimeout:  idleTimeout,
		drainTimeout: drainTimeout,
		conns:        map[*Conn]struct{}{},
	}
}

type drainListener struct {
	net.Listener

	idleTimeout  time.Duration
	drainTimeout time.Duration

	mu     sync.Mutex
	closed bool
	conns  map[*Conn]struct{}
}

func (l *drainListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := WrapTCP(conn, l.idleTimeout, l.drainTimeout).(*Conn)
	wrapped.onClose = func() {
		l.mu.Lock()
		if !l.closed {
			delete(l.conns, wrapped)
		}
		l.mu.Unlock()
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	l.conns[wrapped] = struct{}{}
	l.mu.Unlock()
	return wrapped, nil
}

func (l *drainListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return l.Listener.Close()
	}
	l.closed = true
	conns := make([]*Conn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	clear(l.conns)
	l.mu.Unlock()

	err := l.Listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	return err
}

type udpAssocTarget struct {
	conn net.Conn
}

var udpBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, UDPBufferSize)
		return &buf
	},
}

type udpBindConn interface {
	ReadFromUDP(b []byte) (int, *net.UDPAddr, error)
	WriteTo(b []byte, addr net.Addr) (int, error)
	LocalAddr() net.Addr
	Close() error
}

func defaultListenUDP(laddr *net.UDPAddr) (udpBindConn, error) {
	return net.ListenUDP("udp", laddr)
}

// AssociateHandler handles SOCKS5 UDP ASSOCIATE requests with full-size UDP
// buffers, isolated per-destination expiration and failure recovery, and
// lifecycle cancellation tied to parentCtx. When onError is non-nil, terminal
// per-packet relay failures and unexpected read errors are reported with
// rate-limited, allowlisted reasons.
func AssociateHandler(
	parentCtx context.Context,
	dial func(context.Context, string, string) (net.Conn, error),
	onError func(error),
) func(context.Context, io.Writer, *socks5.Request) error {
	return associateHandler(parentCtx, dial, onError, defaultListenUDP, defaultUDPErrorInterval)
}

func associateHandler(
	parentCtx context.Context,
	dial func(context.Context, string, string) (net.Conn, error),
	onError func(error),
	listenUDP func(*net.UDPAddr) (udpBindConn, error),
	errorInterval time.Duration,
) func(context.Context, io.Writer, *socks5.Request) error {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	if listenUDP == nil {
		listenUDP = defaultListenUDP
	}
	return func(_ context.Context, writer io.Writer, request *socks5.Request) error {
		err := parentCtx.Err()
		if err != nil {
			return err
		}
		tcpAddr, ok := request.LocalAddr.(*net.TCPAddr)
		if !ok {
			_ = socks5.SendReply(writer, statute.RepServerFailure, nil)
			return fmt.Errorf("%w: %T", errLocalAddrNotTCP, request.LocalAddr)
		}

		bindLn, err := listenUDP(&net.UDPAddr{IP: tcpAddr.IP, Port: 0})
		if err != nil {
			_ = socks5.SendReply(writer, statute.RepServerFailure, nil)
			return err
		}

		err = socks5.SendReply(writer, statute.RepSuccess, bindLn.LocalAddr())
		if err != nil {
			_ = bindLn.Close()
			return err
		}

		var mu sync.Mutex
		closed := false
		targets := map[string]*udpAssocTarget{}
		var closeOnce sync.Once
		closeAll := func() {
			closeOnce.Do(func() {
				mu.Lock()
				closed = true
				active := make([]*udpAssocTarget, 0, len(targets))
				for key, target := range targets {
					active = append(active, target)
					delete(targets, key)
				}
				mu.Unlock()

				_ = bindLn.Close()
				for _, target := range active {
					_ = target.conn.Close()
				}
			})
		}
		isClosed := func() bool {
			if parentCtx.Err() != nil {
				return true
			}
			mu.Lock()
			c := closed
			mu.Unlock()
			return c
		}

		reporter := newUDPErrorReporter(errorInterval, onError)
		var wg sync.WaitGroup
		stopAfter := context.AfterFunc(parentCtx, closeAll)
		defer stopAfter()
		defer reporter.close()
		defer wg.Wait()
		defer closeAll()
		err = parentCtx.Err()
		if err != nil {
			return err
		}

		removeTarget := func(key string, target *udpAssocTarget) {
			mu.Lock()
			if targets[key] == target {
				delete(targets, key)
			}
			mu.Unlock()
			_ = target.conn.Close()
		}

		getOrDialTarget := func(key string, srcAddr *net.UDPAddr, dstAddr string, hdr []byte) (*udpAssocTarget, error) {
			mu.Lock()
			if closed {
				mu.Unlock()
				return nil, net.ErrClosed
			}
			existing := targets[key]
			mu.Unlock()
			if existing != nil {
				return existing, nil
			}

			conn, dialErr := dial(parentCtx, "udp", dstAddr)
			if dialErr != nil {
				return nil, dialErr
			}

			headerCopy := append([]byte(nil), hdr...)
			target := &udpAssocTarget{conn: conn}

			mu.Lock()
			if closed {
				mu.Unlock()
				_ = conn.Close()
				return nil, net.ErrClosed
			}
			if current := targets[key]; current != nil {
				mu.Unlock()
				_ = conn.Close()
				return current, nil
			}
			targets[key] = target
			wg.Add(1)
			mu.Unlock()

			go func() {
				defer wg.Done()
				defer removeTarget(key, target)

				bufPtr := udpBufferPool.Get().(*[]byte)
				defer udpBufferPool.Put(bufPtr)
				replyBuf := *bufPtr
				hdrLen := len(headerCopy)
				if hdrLen >= len(replyBuf) {
					return
				}

				for {
					rn, readErr := target.conn.Read(replyBuf[hdrLen:])
					if rn > 0 || readErr == nil {
						copy(replyBuf[:hdrLen], headerCopy)
						_, writeErr := bindLn.WriteTo(replyBuf[:hdrLen+rn], srcAddr)
						if writeErr != nil {
							if isClosed() {
								return
							}
							reporter.report(udpOpReplyWrite, writeErr)
							if errors.Is(writeErr, net.ErrClosed) {
								return
							}
						}
					}
					if readErr != nil {
						if !isClosed() &&
							!errors.Is(readErr, os.ErrDeadlineExceeded) &&
							!errors.Is(readErr, io.EOF) &&
							!errors.Is(readErr, net.ErrClosed) {
							reporter.report(udpOpTargetRead, readErr)
						}
						return
					}
				}
			}()

			return target, nil
		}

		wg.Go(func() {
			defer closeAll()

			bufPtr := udpBufferPool.Get().(*[]byte)
			defer udpBufferPool.Put(bufPtr)
			buf := *bufPtr

			for {
				n, srcAddr, readErr := bindLn.ReadFromUDP(buf)
				if readErr != nil {
					if isClosed() {
						return
					}
					reporter.report(udpOpClientRead, readErr)
					if errors.Is(readErr, io.EOF) || errors.Is(readErr, net.ErrClosed) {
						return
					}
					continue
				}

				pk, parseErr := statute.ParseDatagram(buf[:n])
				if parseErr != nil {
					continue
				}

				srcEqual := (request.DestAddr.IP.IsUnspecified() || request.DestAddr.IP.Equal(srcAddr.IP)) &&
					(request.DestAddr.Port == 0 || request.DestAddr.Port == srcAddr.Port)
				if !srcEqual {
					continue
				}

				connKey := srcAddr.String() + "--" + pk.DstAddr.String()
				target, dialErr := getOrDialTarget(connKey, srcAddr, pk.DstAddr.String(), pk.Header())
				if dialErr != nil {
					if !isClosed() {
						reporter.report(udpOpDial, dialErr)
					}
					continue
				}

				_, writeErr := target.conn.Write(pk.Data)
				if writeErr != nil {
					removeTarget(connKey, target)
					if !errors.Is(writeErr, net.ErrClosed) {
						if !isClosed() {
							reporter.report(udpOpWrite, writeErr)
						}
						continue
					}
					if isClosed() {
						continue
					}

					redialed, retryDialErr := getOrDialTarget(connKey, srcAddr, pk.DstAddr.String(), pk.Header())
					if retryDialErr != nil {
						if !isClosed() {
							reporter.report(udpOpRedial, retryDialErr)
						}
						continue
					}

					_, retryWriteErr := redialed.conn.Write(pk.Data)
					if retryWriteErr != nil {
						removeTarget(connKey, redialed)
						if !isClosed() {
							reporter.report(udpOpRetryWrite, retryWriteErr)
						}
					}
				}
			}
		})

		ctrlBuf := make([]byte, 256)
		for {
			_, ctrlErr := request.Reader.Read(ctrlBuf)
			if ctrlErr != nil {
				closeAll()
				if errors.Is(ctrlErr, io.EOF) || errors.Is(ctrlErr, net.ErrClosed) {
					return nil
				}
				return ctrlErr
			}
		}
	}
}

// DialContext bounds reqCtx with timeout while honoring earlier cancellation
// or deadlines on either parentCtx or reqCtx.
func DialContext(parentCtx, reqCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(reqCtx, timeout)
	if parentCtx == nil {
		return ctx, cancel
	}
	if parentCtx.Err() != nil {
		cancel()
		return ctx, cancel
	}
	stop := context.AfterFunc(parentCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func enableTCPKeepAlive(conn net.Conn) {
	if ka, ok := conn.(interface{ SetKeepAlive(bool) error }); ok {
		_ = ka.SetKeepAlive(true)
	}
	if kp, ok := conn.(interface{ SetKeepAlivePeriod(time.Duration) error }); ok {
		_ = kp.SetKeepAlivePeriod(keepAlivePeriod)
	}
}
