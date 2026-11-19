package localforward

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/waiter"
)

var (
	errGvisorRead     = errors.New("gvisor udp read")
	tcpipEndpointType = reflect.TypeFor[tcpip.Endpoint]()
	waiterQueueType   = reflect.TypeFor[*waiter.Queue]()
)

type udpSessionConn interface {
	Write([]byte) (int, error)
	Close() error
	readLoop(func([]byte)) error
}

type fallbackUDPConn struct {
	net.Conn
}

func (c fallbackUDPConn) readLoop(handle func([]byte)) error {
	bufPtr := replyBuffers.Get().(*[]byte)
	defer replyBuffers.Put(bufPtr)
	buf := *bufPtr
	for {
		n, err := c.Read(buf)
		if err != nil {
			return err
		}
		handle(buf[:n])
	}
}

func wrapUDPSession(raw net.Conn, idleTimeout time.Duration) udpSessionConn {
	if gc, ok := newGvisorUDPConn(raw, idleTimeout); ok {
		return gc
	}
	return fallbackUDPConn{Conn: socksconn.WrapUDP(raw, idleTimeout)}
}

type gvisorUDPConn struct {
	raw         *gonet.UDPConn
	ep          tcpip.Endpoint
	wq          *waiter.Queue
	idleTimeout time.Duration
	created     time.Time
	lastActive  atomic.Int64
	closed      atomic.Bool
}

func newGvisorUDPConn(raw net.Conn, idleTimeout time.Duration) (*gvisorUDPConn, bool) {
	gc, ok := raw.(*gonet.UDPConn)
	if !ok {
		return nil, false
	}
	ep, wq, ok := extractGvisorUDP(gc)
	if !ok {
		return nil, false
	}
	return &gvisorUDPConn{
		raw:         gc,
		ep:          ep,
		wq:          wq,
		idleTimeout: idleTimeout,
		created:     time.Now(),
	}, true
}

func extractGvisorUDP(conn *gonet.UDPConn) (tcpip.Endpoint, *waiter.Queue, bool) {
	if conn == nil {
		return nil, nil, false
	}
	v := reflect.ValueOf(conn).Elem()
	epField := v.FieldByName("ep")
	wqField := v.FieldByName("wq")
	if !epField.IsValid() || !wqField.IsValid() || !epField.CanAddr() || !wqField.CanAddr() {
		return nil, nil, false
	}
	if epField.Type() != tcpipEndpointType || wqField.Type() != waiterQueueType {
		return nil, nil, false
	}
	epVal := reflect.NewAt(tcpipEndpointType, epField.Addr().UnsafePointer()).Elem()
	wqVal := reflect.NewAt(waiterQueueType, wqField.Addr().UnsafePointer()).Elem()
	ep, ok := reflect.TypeAssert[tcpip.Endpoint](epVal)
	if !ok || ep == nil {
		return nil, nil, false
	}
	wq, ok := reflect.TypeAssert[*waiter.Queue](wqVal)
	if !ok || wq == nil {
		return nil, nil, false
	}
	return ep, wq, true
}

func (c *gvisorUDPConn) recordActivity() {
	c.lastActive.Store(int64(time.Since(c.created)))
}

func (c *gvisorUDPConn) idleRemaining(now time.Time) time.Duration {
	return time.Duration(c.lastActive.Load()) + c.idleTimeout - now.Sub(c.created)
}

func (c *gvisorUDPConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := c.raw.Write(p)
	if err != nil {
		if c.closed.Load() {
			return n, net.ErrClosed
		}
		return n, err
	}
	c.recordActivity()
	return n, nil
}

func (c *gvisorUDPConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.raw.Close()
}

func (c *gvisorUDPConn) readLoop(handle func([]byte)) error {
	const events = waiter.ReadableEvents | waiter.EventErr | waiter.EventHUp
	waitEntry, notifyCh := waiter.NewChannelEntry(events)
	c.wq.EventRegister(&waitEntry)
	defer c.wq.EventUnregister(&waitEntry)

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if c.idleTimeout > 0 {
		timer = time.NewTimer(c.idleTimeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	for {
		if c.ep.Readiness(events) == 0 {
			select {
			case <-notifyCh:
				continue
			case <-timeoutCh:
				if remaining := c.idleRemaining(time.Now()); remaining > 0 {
					timer.Reset(remaining)
					continue
				}
				return os.ErrDeadlineExceeded
			}
		}
		retry, err := c.readOnce(handle)
		if err != nil || !retry {
			return err
		}
	}
}

func (c *gvisorUDPConn) readOnce(handle func([]byte)) (bool, error) {
	bufPtr := replyBuffers.Get().(*[]byte)
	defer replyBuffers.Put(bufPtr)
	w := tcpip.SliceWriter(*bufPtr)
	res, terr := c.ep.Read(&w, tcpip.ReadOptions{})
	if terr != nil {
		if _, ok := terr.(*tcpip.ErrWouldBlock); ok {
			return true, nil
		}
		if _, ok := terr.(*tcpip.ErrClosedForReceive); ok {
			return false, io.EOF
		}
		return false, fmt.Errorf("%w: %s", errGvisorRead, terr.String())
	}
	c.recordActivity()
	handle((*bufPtr)[:res.Count])
	return true, nil
}
