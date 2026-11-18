//go:build windows

package socksconn

import (
	"errors"

	"golang.org/x/sys/windows"
)

func classifyPlatformUDPError(err error) (string, bool) {
	switch {
	case errors.Is(err, windows.WSAETIMEDOUT):
		return "timeout", true
	case errors.Is(err, windows.WSAECONNREFUSED):
		return "connection refused", true
	case errors.Is(err, windows.WSAECONNRESET):
		return "connection reset", true
	case errors.Is(err, windows.WSAECONNABORTED):
		return "connection aborted", true
	case errors.Is(err, windows.WSAENETUNREACH):
		return "network unreachable", true
	case errors.Is(err, windows.WSAEHOSTUNREACH):
		return "host unreachable", true
	case errors.Is(err, windows.WSAENETDOWN):
		return "network down", true
	case errors.Is(err, windows.WSAEMSGSIZE):
		return "message too long", true
	case errors.Is(err, windows.WSAENOBUFS):
		return "buffer exhausted", true
	case errors.Is(err, windows.WSAEACCES):
		return "permission denied", true
	default:
		return "", false
	}
}
