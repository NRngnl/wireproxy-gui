package socksconn

import (
	"net"
	"sync"
	"testing"
	"time"
)

type deadlineConn struct {
	net.Conn
	mu    sync.Mutex
	armed bool
}

func (c *deadlineConn) set(t time.Time) error {
	c.mu.Lock()
	c.armed = !t.IsZero()
	c.mu.Unlock()
	return nil
}

func (c *deadlineConn) SetDeadline(t time.Time) error      { return c.set(t) }
func (c *deadlineConn) SetReadDeadline(t time.Time) error  { return c.set(t) }
func (c *deadlineConn) SetWriteDeadline(t time.Time) error { return c.set(t) }
func (c *deadlineConn) CloseWrite() error                  { return nil }

func TestCloseLeavesNoDeadlineArmed(t *testing.T) {
	for _, order := range []string{"drain-then-close", "close-then-drain"} {
		a, b := net.Pipe()
		defer b.Close()
		raw := &deadlineConn{Conn: a}
		conn := WrapTCP(raw, time.Minute, time.Minute).(*Conn)
		_ = raw.SetDeadline(time.Now().Add(time.Minute))
		if order == "drain-then-close" {
			_ = conn.CloseWrite()
			_ = conn.Close()
		} else {
			_ = conn.Close()
			_ = conn.CloseWrite()
		}
		raw.mu.Lock()
		armed := raw.armed
		raw.mu.Unlock()
		if armed {
			t.Fatalf("%s: deadline timer still armed after Close", order)
		}
	}
	a, b := net.Pipe()
	defer b.Close()
	raw := &deadlineConn{Conn: a}
	udp := WrapUDP(raw, time.Minute)
	_ = raw.SetDeadline(time.Now().Add(time.Minute))
	_ = udp.Close()
	if raw.armed {
		t.Fatal("udp: deadline timer still armed after Close")
	}
}
