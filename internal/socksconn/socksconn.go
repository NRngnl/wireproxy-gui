// Package socksconn bounds how long a proxied SOCKS5 connection may linger
// after the client has finished sending.
package socksconn

import (
	"net"
	"time"
)

// DrainTimeout is how long the target may keep sending after the client half-closed.
const DrainTimeout = 60 * time.Second

// Conn half-closes the wrapped target connection and then bounds its reads by
// DrainTimeout, so a target that never closes cannot keep the relay alive.
type Conn struct {
	net.Conn
}

// Wrap returns conn with a bounded drain after CloseWrite.
func Wrap(conn net.Conn) net.Conn {
	return Conn{Conn: conn}
}

// CloseWrite half-closes the target when supported and starts the drain deadline.
func (c Conn) CloseWrite() error {
	var err error
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		err = cw.CloseWrite()
	}
	if deadlineErr := c.SetReadDeadline(time.Now().Add(DrainTimeout)); err == nil {
		err = deadlineErr
	}
	return err
}
