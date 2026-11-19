package socksconn

import (
	"net"
	"reflect"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/waiter"
)

var (
	tcpipEndpointType = reflect.TypeFor[tcpip.Endpoint]()
	waiterQueueType   = reflect.TypeFor[*waiter.Queue]()
)

// GvisorEndpoint returns the gVisor endpoint and waiter queue behind a
// *gonet.TCPConn or *gonet.UDPConn, which do not export them.
func GvisorEndpoint(conn net.Conn) (tcpip.Endpoint, *waiter.Queue, bool) {
	switch conn.(type) {
	case *gonet.TCPConn, *gonet.UDPConn:
	default:
		return nil, nil, false
	}
	v := reflect.ValueOf(conn)
	if v.IsNil() {
		return nil, nil, false
	}
	v = v.Elem()
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

func enableGvisorKeepAlive(conn net.Conn, period time.Duration) bool {
	ep, _, ok := GvisorEndpoint(conn)
	if !ok {
		return false
	}
	idle := tcpip.KeepaliveIdleOption(period)
	interval := tcpip.KeepaliveIntervalOption(period)
	_ = ep.SetSockOpt(&idle)
	_ = ep.SetSockOpt(&interval)
	ep.SocketOptions().SetKeepAlive(true)
	return true
}
