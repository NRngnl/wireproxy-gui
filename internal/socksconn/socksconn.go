// Package socksconn bounds how long proxied SOCKS5 connections and dials may
// stay idle or linger after half-close.
package socksconn

import (
	"context"
	"net"
	"sync"
	"time"
)

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

	keepAlivePeriod = 30 * time.Second
)

type idleDeadline struct {
	mu         sync.Mutex
	timeout    time.Duration
	halfClosed bool
	deadline   time.Time
}

func (d *idleDeadline) refresh(conn net.Conn, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.halfClosed || d.timeout <= 0 {
		return nil
	}
	next := now.Add(d.timeout)
	if !d.deadline.IsZero() && next.Sub(d.deadline) < d.timeout/4 {
		return nil
	}
	err := conn.SetReadDeadline(next)
	if err != nil {
		return err
	}
	d.deadline = next
	return nil
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
	if !d.deadline.IsZero() && d.deadline.Before(drainDeadline) {
		drainDeadline = d.deadline
	}
	err := conn.SetReadDeadline(drainDeadline)
	if err != nil {
		return err
	}
	d.deadline = drainDeadline
	return nil
}

func (d *idleDeadline) clear(conn net.Conn) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.timeout = 0
	if d.halfClosed {
		return nil
	}
	d.deadline = time.Time{}
	return conn.SetReadDeadline(time.Time{})
}

func readWithIdle(conn net.Conn, idle *idleDeadline, b []byte) (int, error) {
	err := idle.refresh(conn, time.Now())
	if err != nil {
		return 0, err
	}
	return conn.Read(b)
}

func writeWithIdle(conn net.Conn, idle *idleDeadline, b []byte) (int, error) {
	err := idle.refresh(conn, time.Now())
	if err != nil {
		return 0, err
	}
	n, err := conn.Write(b)
	if err == nil {
		refreshErr := idle.refresh(conn, time.Now())
		if refreshErr != nil {
			return n, refreshErr
		}
	}
	return n, err
}

// Conn bounds reads on a TCP connection by an activity-based idle deadline and
// a fixed drain deadline after CloseWrite.
type Conn struct {
	net.Conn

	idle         idleDeadline
	drainTimeout time.Duration
}

// Wrap returns conn wrapped with the default TCP idle and half-close drain timeouts.
func Wrap(conn net.Conn) net.Conn {
	return WrapTCP(conn, IdleTimeout, DrainTimeout)
}

// WrapTCP returns conn with TCP keepalive enabled where supported, an
// activity-based idle read deadline, and a bounded drain after CloseWrite.
func WrapTCP(conn net.Conn, idleTimeout, drainTimeout time.Duration) net.Conn {
	enableTCPKeepAlive(conn)
	return &Conn{
		Conn:         conn,
		idle:         idleDeadline{timeout: idleTimeout},
		drainTimeout: drainTimeout,
	}
}

// Read refreshes the idle read deadline while open and reads from the underlying connection.
func (c *Conn) Read(b []byte) (int, error) {
	return readWithIdle(c.Conn, &c.idle, b)
}

// Write writes to the underlying connection and refreshes the idle read deadline while open.
func (c *Conn) Write(b []byte) (int, error) {
	return writeWithIdle(c.Conn, &c.idle, b)
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

// UDPConn bounds reads on a UDP relay connection by an activity-based idle deadline.
type UDPConn struct {
	net.Conn

	idle idleDeadline
}

// WrapUDP returns conn wrapped with an activity-based idle read deadline.
func WrapUDP(conn net.Conn, idleTimeout time.Duration) net.Conn {
	return &UDPConn{
		Conn: conn,
		idle: idleDeadline{timeout: idleTimeout},
	}
}

// Read refreshes the idle read deadline and reads from the underlying connection.
func (c *UDPConn) Read(b []byte) (int, error) {
	return readWithIdle(c.Conn, &c.idle, b)
}

// Write writes to the underlying connection and refreshes the idle read deadline.
func (c *UDPConn) Write(b []byte) (int, error) {
	return writeWithIdle(c.Conn, &c.idle, b)
}

// WrapListener returns listener with accepted TCP connections wrapped for
// activity-based idle deadlines and bounded half-close drains.
func WrapListener(listener net.Listener, idleTimeout, drainTimeout time.Duration) net.Listener {
	return &drainListener{
		Listener:     listener,
		idleTimeout:  idleTimeout,
		drainTimeout: drainTimeout,
	}
}

type drainListener struct {
	net.Listener

	idleTimeout  time.Duration
	drainTimeout time.Duration
}

func (l *drainListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return WrapTCP(conn, l.idleTimeout, l.drainTimeout), nil
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
