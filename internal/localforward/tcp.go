package localforward

import (
	"context"
	"net"
	"sync"

	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

func (s *Set) bindTCP(ctx context.Context, e *entry) (func(), error) {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", e.rule.ListenAddr)
	if err != nil {
		return nil, err
	}
	e.closer = func() { _ = listener.Close() }
	return func() {
		defer close(e.done)
		var conns sync.WaitGroup
		defer conns.Wait()
		for {
			conn, err := listener.Accept()
			if err != nil {
				s.fail(ctx, e, err)
				return
			}
			conns.Go(func() { s.relayTCP(ctx, e, conn) })
		}
	}, nil
}

func (s *Set) relayTCP(ctx context.Context, e *entry, conn net.Conn) {
	dialCtx, cancel := socksconn.DialContext(ctx, ctx, s.timeouts.Dial)
	out, err := s.dial.TCP(dialCtx, e.rule.TargetAddr)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			s.dialFailed(e, err)
		}
		_ = conn.Close()
		return
	}
	s.dialSucceeded(e)
	socksconn.RelayTCP(ctx, conn, out, s.timeouts)
}
