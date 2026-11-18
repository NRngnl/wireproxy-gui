package localforward

import (
	"context"
	"errors"
)

var errUDPUnsupported = errors.New("udp local forwards are not supported yet")

func (s *Set) startUDP(context.Context, *entry) error {
	return errUDPUnsupported
}
