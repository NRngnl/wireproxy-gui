package localforward

import (
	"context"
	"net"
	"sync"

	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

func (s *Set) startUDP(ctx context.Context, e *entry) error {
	var lc net.ListenConfig
	packetConn, err := lc.ListenPacket(ctx, "udp", e.rule.ListenAddr)
	if err != nil {
		return err
	}
	e.closer = func() { _ = packetConn.Close() }
	go func() {
		defer close(e.done)
		s.serveUDP(ctx, packetConn, e.rule.TargetAddr)
	}()
	return nil
}

func (s *Set) serveUDP(ctx context.Context, packetConn net.PacketConn, target string) {
	var (
		mu       sync.Mutex
		sessions = map[string]net.Conn{}
		wg       sync.WaitGroup
	)
	defer func() {
		mu.Lock()
		for _, conn := range sessions {
			_ = conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	}()

	session := func(key string, source net.Addr) net.Conn {
		mu.Lock()
		conn := sessions[key]
		mu.Unlock()
		if conn != nil {
			return conn
		}
		dialCtx, cancel := socksconn.DialContext(ctx, ctx, s.timeouts.Dial)
		raw, err := s.dial.UDP(dialCtx, target)
		cancel()
		if err != nil {
			return nil
		}
		conn = socksconn.WrapUDP(raw, s.timeouts.UDPIdle)
		mu.Lock()
		sessions[key] = conn
		mu.Unlock()
		wg.Go(func() {
			defer func() {
				_ = conn.Close()
				mu.Lock()
				if sessions[key] == conn {
					delete(sessions, key)
				}
				mu.Unlock()
			}()
			buf := make([]byte, socksconn.UDPBufferSize)
			for {
				n, err := conn.Read(buf)
				if n > 0 {
					_, _ = packetConn.WriteTo(buf[:n], source)
				}
				if err != nil {
					return
				}
			}
		})
		return conn
	}

	buf := make([]byte, socksconn.UDPBufferSize)
	for {
		n, source, err := packetConn.ReadFrom(buf)
		if err != nil {
			return
		}
		conn := session(source.String(), source)
		if conn == nil {
			continue
		}
		_, _ = conn.Write(buf[:n])
	}
}
