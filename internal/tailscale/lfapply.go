package tailscale

import (
	"context"
	"sort"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/localforward"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

func (p *process) startLocalForwards(ctx context.Context, node tsNode, timeouts socksconn.Timeouts, rules []profile.LocalForward, changed func()) {
	dialer := localforward.Dialer{TCP: noNetstack, UDP: noNetstack}
	client, err := node.LocalClient()
	if err == nil {
		peers := newPeerView(ctx, node, client, changed)
		dialer = localForwardDialer(node, peers)
		p.mu.Lock()
		p.peers = peers
		p.mu.Unlock()
	}
	set := localforward.New(ctx, dialer, timeouts, changed)
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

// ApplyLocalForwards reconciles the running profile's local forwards with next, which must be normalized and validated.
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
	states := set.States()
	r.mu.Lock()
	proc := r.procs[strings.TrimSpace(profileID)]
	r.mu.Unlock()
	if proc == nil {
		return states
	}
	proc.mu.Lock()
	peers := proc.peers
	proc.mu.Unlock()
	if peers == nil {
		return states
	}
	peers.kick()
	status := peers.status.Load()
	if status == nil {
		return states
	}
	for i := range states {
		states[i].Path = targetPath(status, states[i].Target)
	}
	return states
}

// LocalForwardTargets lists the DNS names of the tailnet peers of a running profile.
func (r *Runner) LocalForwardTargets(profileID string) []string {
	r.mu.Lock()
	proc := r.procs[strings.TrimSpace(profileID)]
	r.mu.Unlock()
	if proc == nil {
		return nil
	}
	proc.mu.Lock()
	peers := proc.peers
	proc.mu.Unlock()
	if peers == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(peers.ctx, r.timeouts.Dial)
	defer cancel()
	status, err := peers.get(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(status.Peer))
	for _, peer := range status.Peer {
		if name := trimDNS(peer.DNSName); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
