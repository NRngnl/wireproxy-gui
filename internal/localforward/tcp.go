package localforward

import (
	"context"
	"net"
	"sync"

	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

func (s *Set) startTCP(ctx context.Context, e *entry) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", e.rule.ListenAddr)
	if err != nil {
		return err
	}
	e.closer = func() { _ = listener.Close() }
	go func() {
		defer close(e.done)
		var conns sync.WaitGroup
		defer conns.Wait()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conns.Go(func() { s.relayTCP(ctx, conn, e.rule.TargetAddr) })
		}
	}()
	return nil
}

func (s *Set) relayTCP(ctx context.Context, conn net.Conn, target string) {
	dialCtx, cancel := socksconn.DialContext(ctx, ctx, s.timeouts.Dial)
	out, err := s.dial.TCP(dialCtx, target)
	cancel()
	if err != nil {
		_ = conn.Close()
		return
	}
	socksconn.RelayTCP(ctx, conn, out, s.timeouts)
}
