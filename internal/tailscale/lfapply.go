package tailscale

import (
	"context"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/localforward"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

func (p *process) startLocalForwards(ctx context.Context, node tsNode, timeouts socksconn.Timeouts, rules []profile.LocalForward, changed func()) {
	set := localforward.New(ctx, localForwardDialer(node), timeouts, changed)
	p.mu.Lock()
	closed := p.closed
	if !closed {
		p.localForwards = set
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
	return proc.localForwards
}

// ApplyLocalForwards reconciles the running profile's local forwards with next.
func (r *Runner) ApplyLocalForwards(_ context.Context, profileID string, next []profile.LocalForward) error {
	set := r.localForwardSet(profileID)
	if set == nil {
		return ErrNotRunning
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
