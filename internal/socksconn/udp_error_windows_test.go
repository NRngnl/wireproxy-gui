//go:build windows

package socksconn

import (
	"fmt"
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

func TestClassifyUDPErrorWindows(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "raw WSAEMSGSIZE",
			err:  windows.WSAEMSGSIZE,
			want: "message too long",
		},
		{
			name: "net.OpError wrapping WSAEMSGSIZE",
			err:  &net.OpError{Op: "write", Net: "udp", Err: windows.WSAEMSGSIZE},
			want: "message too long",
		},
		{
			name: "wrapped WSAECONNREFUSED",
			err:  fmt.Errorf("dial: %w", windows.WSAECONNREFUSED),
			want: "connection refused",
		},
		{
			name: "WSAETIMEDOUT",
			err:  windows.WSAETIMEDOUT,
			want: "timeout",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyUDPError(tc.err); got != tc.want {
				t.Fatalf("classifyUDPError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
