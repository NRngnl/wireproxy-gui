package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/tailscale"
	"github.com/NRngnl/wireproxy-gui/internal/wireproxy"
)

type backend interface {
	Events() <-chan connection.Event
	Running(profileID string) bool
	ExitNodes(context.Context, string) ([]connection.ExitNode, error)
	UpdateExitNode(context.Context, string, profile.TailscaleConfig) error
	Logout(context.Context, string) error
	Start(context.Context, profile.Profile) error
	Stop(profileID string) bool
	StopAll()
	StopAllAndWait(context.Context) error
}

type Runner struct {
	events chan connection.Event

	wireguard backend
	tailscale backend

	mu      sync.Mutex
	backing map[string]profile.BackendKind
}

func New() *Runner {
	return NewWithBackends(wireproxy.NewRunner(), tailscale.NewRunner())
}

func NewWithBackends(wireguard, tailscale backend) *Runner {
	r := &Runner{
		events:    make(chan connection.Event, 512),
		wireguard: wireguard,
		tailscale: tailscale,
		backing:   map[string]profile.BackendKind{},
	}
	r.forward(profile.BackendWireGuard, wireguard)
	r.forward(profile.BackendTailscale, tailscale)
	return r
}

func (r *Runner) Events() <-chan connection.Event {
	return r.events
}

func (r *Runner) Running(profileID string) bool {
	return r.wireguard.Running(profileID) || r.tailscale.Running(profileID)
}

func (r *Runner) ExitNodes(ctx context.Context, profileID string) ([]connection.ExitNode, error) {
	r.mu.Lock()
	kind, ok := r.backing[profileID]
	r.mu.Unlock()
	if ok {
		return r.backendByKind(kind).ExitNodes(ctx, profileID)
	}
	if r.tailscale.Running(profileID) {
		return r.tailscale.ExitNodes(ctx, profileID)
	}
	return nil, connection.ErrExitNodesUnavailable
}

func (r *Runner) UpdateExitNode(ctx context.Context, profileID string, cfg profile.TailscaleConfig) error {
	r.mu.Lock()
	kind, ok := r.backing[profileID]
	r.mu.Unlock()
	if ok {
		return r.backendByKind(kind).UpdateExitNode(ctx, profileID, cfg)
	}
	if r.tailscale.Running(profileID) {
		return r.tailscale.UpdateExitNode(ctx, profileID, cfg)
	}
	return connection.ErrExitNodesUnavailable
}

func (r *Runner) Logout(ctx context.Context, profileID string) error {
	r.mu.Lock()
	kind, ok := r.backing[profileID]
	r.mu.Unlock()
	if ok {
		return r.backendByKind(kind).Logout(ctx, profileID)
	}
	return r.tailscale.Logout(ctx, profileID)
}

func (r *Runner) Start(ctx context.Context, p profile.Profile) error {
	p.Normalize()
	backend, kind, err := r.backendFor(p)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if _, ok := r.backing[p.ID]; ok {
		r.mu.Unlock()
		return fmt.Errorf("%s: %w", p.Name, wireproxy.ErrAlreadyConnected)
	}
	r.backing[p.ID] = kind
	r.mu.Unlock()

	err = backend.Start(ctx, p)
	if err != nil {
		r.removeBacking(p.ID, kind)
		return err
	}
	return nil
}

func (r *Runner) Stop(profileID string) bool {
	r.mu.Lock()
	kind, ok := r.backing[profileID]
	r.mu.Unlock()
	if ok {
		stopped := r.backendByKind(kind).Stop(profileID)
		if stopped {
			return true
		}
	}
	return r.wireguard.Stop(profileID) || r.tailscale.Stop(profileID)
}

func (r *Runner) StopAll() {
	r.wireguard.StopAll()
	r.tailscale.StopAll()
}

func (r *Runner) StopAllAndWait(ctx context.Context) error {
	return errors.Join(
		r.wireguard.StopAllAndWait(ctx),
		r.tailscale.StopAllAndWait(ctx),
	)
}

func (r *Runner) backendFor(p profile.Profile) (backend, profile.BackendKind, error) {
	switch {
	case p.IsWireGuard():
		return r.wireguard, profile.BackendWireGuard, nil
	case p.IsTailscale():
		return r.tailscale, profile.BackendTailscale, nil
	default:
		return nil, "", profile.ErrBackendKindInvalid
	}
}

func (r *Runner) backendByKind(kind profile.BackendKind) backend {
	if kind == profile.BackendTailscale {
		return r.tailscale
	}
	return r.wireguard
}

func (r *Runner) removeBacking(profileID string, kind profile.BackendKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backing[profileID] == kind {
		delete(r.backing, profileID)
	}
}

func (r *Runner) forward(kind profile.BackendKind, source backend) {
	go func() {
		for event := range source.Events() {
			if event.Type == connection.EventStopped || event.Type == connection.EventError {
				r.removeBacking(event.ProfileID, kind)
			}
			select {
			case r.events <- event:
			default:
			}
		}
	}()
}
