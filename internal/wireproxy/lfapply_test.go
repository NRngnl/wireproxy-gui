package wireproxy

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	upstream "github.com/windtf/wireproxy"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func TestNamedTargetWithoutDNSFailsBeforeDialing(t *testing.T) {
	dialer := tunDialer(&upstream.VirtualTun{SystemDNS: true})
	_, err := dialer.TCP(t.Context(), "remote-pc:22")
	if !errors.Is(err, errNamedTargetNeedsDNS) {
		t.Fatalf("err = %v", err)
	}
}

func TestNamedTargetLookupStopsOnCancel(t *testing.T) {
	addr := netip.MustParseAddr("10.0.0.2")
	dns := netip.MustParseAddr("10.0.0.53")
	dev, tnet, err := netstack.CreateNetTUN([]netip.Addr{addr}, []netip.Addr{dns}, 1420)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	go func() {
		bufs, sizes := [][]byte{make([]byte, 2048)}, []int{0}
		for {
			_, err := dev.Read(bufs, sizes, 0)
			if err != nil {
				return
			}
		}
	}()
	tun := &upstream.VirtualTun{Tnet: tnet, Conf: &upstream.DeviceConfig{DNS: []netip.Addr{dns}}}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err = tunDialer(tun).TCP(ctx, "remote-pc:22")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("lookup returned %v after %v", err, time.Since(start))
	}
}
