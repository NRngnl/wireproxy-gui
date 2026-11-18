package wireproxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/localforward"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
	upstream "github.com/windtf/wireproxy"
)

var errNamedTargetNeedsDNS = errors.New("local forward target is a name but the WireGuard config has no DNS; set DNS in the WireGuard config or use an IP")

func tunDialer(tun *upstream.VirtualTun) localforward.Dialer {
	dial := func(network string) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			_, parseErr := netip.ParseAddr(host)
			if parseErr != nil && tun.SystemDNS {
				return nil, errNamedTargetNeedsDNS
			}
			return tun.Tnet.DialContext(ctx, network, addr)
		}
	}
	return localforward.Dialer{TCP: dial("tcp"), UDP: dial("udp")}
}

func (p *process) startLocalForwards(ctx context.Context, tun *upstream.VirtualTun, timeouts socksconn.Timeouts, rules []profile.LocalForward, changed func()) {
	set := localforward.New(ctx, tunDialer(tun), timeouts, changed)
	p.mu.Lock()
	closed := p.closed
	if !closed {
		p.forwards = set
	}
	p.mu.Unlock()
	if closed {
		set.Close()
		return
	}
	set.Apply(rules)
}

func (r *Runner) localForwardSet(profileID string) *localforward.Set {
	r.mu.Lock()
	proc := r.procs[strings.TrimSpace(profileID)]
	r.mu.Unlock()
	if proc == nil {
		return nil
	}
	proc.mu.Lock()
	defer proc.mu.Unlock()
	return proc.forwards
}

// ApplyLocalForwards reconciles the running profile's local forwards with next.
func (r *Runner) ApplyLocalForwards(_ context.Context, profileID string, next []profile.LocalForward) error {
	set := r.localForwardSet(profileID)
	if set == nil {
		return connection.ErrNotRunning
	}
	set.Apply(next)
	return nil
}

// RetryLocalForward rebinds a local forward that is in the error state.
func (r *Runner) RetryLocalForward(profileID string, key profile.LocalForwardKey) {
	if set := r.localForwardSet(profileID); set != nil {
		set.Retry(key)
	}
}

// LocalForwardStates returns the live state of each local forward of a running profile.
func (r *Runner) LocalForwardStates(profileID string) []localforward.State {
	set := r.localForwardSet(profileID)
	if set == nil {
		return nil
	}
	return set.States()
}

// LocalForwardTargets returns no suggestions: WireGuard has no peer directory.
func (*Runner) LocalForwardTargets(string) []string { return nil }
