package socksconn

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestCloseWriteHalfClosesAndKeepsDrainReadable(t *testing.T) {
	ctx := t.Context()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	remote := <-accepted
	defer remote.Close()

	conn := Wrap(raw)
	err = conn.(Conn).CloseWrite()
	if err != nil {
		t.Fatal(err)
	}
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	_, err = remote.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("remote read = %v, want EOF from half-close", err)
	}
	_, err = remote.Write([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadFull(conn, make([]byte, 1))
	if err != nil {
		t.Fatalf("drain read = %v, want data before deadline", err)
	}
}
