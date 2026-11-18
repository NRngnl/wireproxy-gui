package wireproxy

import (
	"errors"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	upstream "github.com/windtf/wireproxy"
)

func TestNamedTargetWithoutDNSFailsBeforeDialing(t *testing.T) {
	dialer := tunDialer(&upstream.VirtualTun{SystemDNS: true})
	_, err := dialer.TCP(t.Context(), "remote-pc:22")
	if !errors.Is(err, errNamedTargetNeedsDNS) {
		t.Fatalf("err = %v", err)
	}
}

func TestForwardChangeFloodDoesNotCrowdOutLifecycleEvents(t *testing.T) {
	r := NewRunner()
	p := profile.New("wg", "", 1080)
	for range 5000 {
		r.emit(EventLocalForwardChanged, p, "")
	}
	r.emit(EventStopped, p, "disconnected")
	for {
		select {
		case e := <-r.events:
			if e.Type == EventStopped {
				return
			}
		default:
			t.Fatal("Stopped event was dropped")
		}
	}
}
