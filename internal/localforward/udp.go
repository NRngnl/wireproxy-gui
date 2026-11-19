package localforward

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

var replyBuffers = sync.Pool{New: func() any {
	buf := make([]byte, socksconn.UDPBufferSize)
	return &buf
}}

type udpSession struct {
	conn udpSessionConn
}

func (s *Set) bindUDP(ctx context.Context, e *entry) (func(), error) {
	var lc net.ListenConfig
	packetConn, err := lc.ListenPacket(ctx, "udp", e.rule.ListenAddr)
	if err != nil {
		return nil, err
	}
	e.closer = func() { _ = packetConn.Close() }
	return func() {
		s.serveUDP(ctx, e, packetConn)
	}, nil
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

	dialAndSend := func(key string, sess *udpSession, first []byte) udpSessionConn {
		dialCtx, cancel := socksconn.DialContext(ctx, ctx, s.timeouts.Dial)
		raw, err := s.dial.UDP(dialCtx, e.rule.TargetAddr)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.dialFailed(e, err)
			}
			retire(key, sess)
			return nil
		}
		conn := wrapUDPSession(raw, s.timeouts.UDPIdle)
		_, werr := conn.Write(first)
		if werr != nil {
			_ = conn.Close()
			if ctx.Err() == nil {
				s.dialFailed(e, werr)
			}
			retire(key, sess)
			return nil
		}
		s.dialSucceeded(e)
		mu.Lock()
		sess.conn = conn
		stale := sessions[key] != sess || ctx.Err() != nil || closing
		mu.Unlock()
		if stale {
			_ = conn.Close()
			return nil
		}
		return conn
	}

	open := func(key string, source net.Addr, sess *udpSession, first []byte) {
		conn := dialAndSend(key, sess, first)
		if conn == nil {
			return
		}
		_ = conn.readLoop(func(reply []byte) {
			_, _ = packetConn.WriteTo(reply, source)
		})
		retire(key, sess)
	}

	start := func(key string, source net.Addr, first []byte) bool {
		if !s.udpBudget.tryAcquire() {
			return false
		}
		sess := &udpSession{}
		sessions[key] = sess
		payload := append([]byte(nil), first...)
		wg.Go(func() {
			defer s.udpBudget.release()
			initial := payload
			payload = nil
			open(key, source, sess, initial)
		})
		return true
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
			admitted := start(key, source, buf[:n])
			mu.Unlock()
			if !admitted {
				s.dialFailed(e, ErrSessionLimitReached)
			}
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
			if !errors.Is(werr, net.ErrClosed) {
				continue
			}
			admitted := true
			mu.Lock()
			if _, ok := sessions[key]; !ok && !closing {
				admitted = start(key, source, buf[:n])
			}
			mu.Unlock()
			if !admitted {
				s.dialFailed(e, ErrSessionLimitReached)
			}
		}
	}
}
