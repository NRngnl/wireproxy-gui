package wireproxy

import (
	"errors"
	"testing"

	upstream "github.com/windtf/wireproxy"
)

func TestNamedTargetWithoutDNSFailsBeforeDialing(t *testing.T) {
	dialer := tunDialer(&upstream.VirtualTun{SystemDNS: true})
	_, err := dialer.TCP(t.Context(), "remote-pc:22")
	if !errors.Is(err, errNamedTargetNeedsDNS) {
		t.Fatalf("err = %v", err)
	}
}
