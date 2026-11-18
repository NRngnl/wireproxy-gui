package localforward

import (
	"context"
	"net"
	"sync"

	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

type udpSession struct {
	conn net.Conn
}

func (s *Set) startUDP(ctx context.Context, e *entry) error {
	var lc net.ListenConfig
	packetConn, err := lc.ListenPacket(ctx, "udp", e.rule.ListenAddr)
	if err != nil {
		return err
	}
	e.closer = func() { _ = packetConn.Close() }
	go func() {
		defer close(e.done)
		s.serveUDP(ctx, e, packetConn)
	}()
	return nil
}

func (s *Set) serveUDP(ctx context.Context, e *entry, packetConn net.PacketConn) {
	var (
		mu       sync.Mutex
		sessions = map[string]*udpSession{}
		wg       sync.WaitGroup
	)
	closing := false
	defer func() {
		mu.Lock()
		closing = true
		for _, sess := range sessions {
			if sess.conn != nil {
				_ = sess.conn.Close()
			}
		}
		mu.Unlock()
		wg.Wait()
	}()

	retire := func(key string, sess *udpSession) {
		mu.Lock()
		if sessions[key] == sess {
			delete(sessions, key)
		}
		mu.Unlock()
		if sess.conn != nil {
			_ = sess.conn.Close()
		}
	}

	open := func(key string, source net.Addr, sess *udpSession, first []byte) {
		dialCtx, cancel := socksconn.DialContext(ctx, ctx, s.timeouts.Dial)
		raw, err := s.dial.UDP(dialCtx, e.rule.TargetAddr)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.dialFailed(e, err)
			}
			retire(key, sess)
			return
		}
		s.dialSucceeded(e)
		conn := socksconn.WrapUDP(raw, s.timeouts.UDPIdle)
		mu.Lock()
		sess.conn = conn
		stale := sessions[key] != sess || ctx.Err() != nil || closing
		mu.Unlock()
		if stale {
			_ = conn.Close()
			return
		}
		_, werr := conn.Write(first)
		if werr != nil {
			retire(key, sess)
			return
		}
		buf := make([]byte, socksconn.UDPBufferSize)
		for {
			n, rerr := conn.Read(buf)
			if rerr != nil {
				retire(key, sess)
				return
			}
			_, _ = packetConn.WriteTo(buf[:n], source)
		}
	}

	buf := make([]byte, socksconn.UDPBufferSize)
	for {
		n, source, err := packetConn.ReadFrom(buf)
		if err != nil {
			s.fail(ctx, e, err)
			return
		}
		key := source.String()
		mu.Lock()
		sess, ok := sessions[key]
		if !ok {
			sess = &udpSession{}
			sessions[key] = sess
			payload := append([]byte(nil), buf[:n]...)
			wg.Go(func() { open(key, source, sess, payload) })
			mu.Unlock()
			continue
		}
		conn := sess.conn
		mu.Unlock()
		if conn == nil {
			continue
		}
		_, werr := conn.Write(buf[:n])
		if werr != nil {
			retire(key, sess)
		}
	}
}
