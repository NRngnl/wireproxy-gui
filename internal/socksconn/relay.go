package socksconn

import (
	"context"
	"io"
	"net"
	"sync"
)

// RelayTCP copies bytes both ways between client and target with idle and drain bounds,
// half-closing where supported, and closes both connections when it returns.
func RelayTCP(ctx context.Context, client, target net.Conn, timeouts Timeouts) {
	timeouts = timeouts.WithDefaults()

	wrappedClient := WrapTCP(client, timeouts.TCPIdle, timeouts.Drain)
	wrappedTarget := WrapTCP(target, timeouts.TCPIdle, timeouts.Drain)
	defer wrappedClient.Close()
	defer wrappedTarget.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = wrappedClient.Close()
			_ = wrappedTarget.Close()
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = io.Copy(wrappedTarget, wrappedClient)
		closeWriteOrClose(wrappedTarget, target)
	})
	wg.Go(func() {
		_, _ = io.Copy(wrappedClient, wrappedTarget)
		closeWriteOrClose(wrappedClient, client)
	})
	wg.Wait()
}

func closeWriteOrClose(wrapped, raw net.Conn) {
	if _, ok := raw.(interface{ CloseWrite() error }); ok {
		if cw, ok := wrapped.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
			return
		}
	}
	_ = wrapped.Close()
}
