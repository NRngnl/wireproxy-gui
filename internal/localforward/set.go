// Package localforward owns host listeners that relay to targets dialed through a backend.
package localforward

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

// ErrPortInUse is the rule error when the listen address is already bound.
var ErrPortInUse = errors.New("port in use")

// Status is the live state of one rule.
type Status string

const (
	StatusActive Status = "active"
	StatusError  Status = "error"
)

// State reports one rule's status; Err is set when Status is StatusError.
type State struct {
	Key    profile.LocalForwardKey
	Status Status
	Err    string
}

// Dialer opens connections to a target through the backend, never through the host network.
type Dialer struct {
	TCP func(ctx context.Context, addr string) (net.Conn, error)
	UDP func(ctx context.Context, addr string) (net.Conn, error)
}

type entry struct {
	rule   profile.LocalForward
	status Status
	err    string
	cancel context.CancelFunc
	closer func()
	done   chan struct{}
}

// Set reconciles listeners against a rule list and tracks per-rule state.
type Set struct {
	ctx      context.Context
	dial     Dialer
	timeouts socksconn.Timeouts
	changed  func()

	mu      sync.Mutex
	entries map[profile.LocalForwardKey]*entry
	order   []profile.LocalForwardKey
}

// New creates a Set whose rules live until ctx ends or Close is called. changed is called after state changes.
func New(ctx context.Context, dial Dialer, timeouts socksconn.Timeouts, changed func()) *Set {
	if changed == nil {
		changed = func() {}
	}
	return &Set{
		ctx:      ctx,
		dial:     dial,
		timeouts: timeouts.WithDefaults(),
		changed:  changed,
		entries:  map[profile.LocalForwardKey]*entry{},
	}
}

// Apply binds added rules, closes removed or changed ones and leaves unchanged rules and their sessions alone.
// A failed bind is recorded as that rule's state, never returned.
func (s *Set) Apply(next []profile.LocalForward) {
	s.mu.Lock()
	wanted := make(map[profile.LocalForwardKey]profile.LocalForward, len(next))
	order := make([]profile.LocalForwardKey, 0, len(next))
	for _, rule := range next {
		wanted[rule.Key()] = rule
		order = append(order, rule.Key())
	}
	var stale []*entry
	for key, e := range s.entries {
		rule, ok := wanted[key]
		if !ok || rule.TargetAddr != e.rule.TargetAddr {
			stale = append(stale, e)
			delete(s.entries, key)
		}
	}
	for _, key := range order {
		if _, ok := s.entries[key]; !ok {
			s.entries[key] = s.start(wanted[key])
		}
	}
	s.order = order
	s.mu.Unlock()
	for _, e := range stale {
		e.stop()
	}
	s.changed()
}

// Retry rebinds a rule that is in the error state.
func (s *Set) Retry(key profile.LocalForwardKey) {
	s.mu.Lock()
	e, ok := s.entries[key]
	if !ok || e.status != StatusError {
		s.mu.Unlock()
		return
	}
	s.entries[key] = s.start(e.rule)
	s.mu.Unlock()
	s.changed()
}

// States returns the state of every rule in rule order.
func (s *Set) States() []State {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]State, 0, len(s.order))
	for _, key := range s.order {
		if e, ok := s.entries[key]; ok {
			states = append(states, State{Key: key, Status: e.status, Err: e.err})
		}
	}
	return states
}

// Close stops every rule and waits for its goroutines.
func (s *Set) Close() {
	s.mu.Lock()
	entries := s.entries
	s.entries = map[profile.LocalForwardKey]*entry{}
	s.order = nil
	s.mu.Unlock()
	for _, e := range entries {
		e.stop()
	}
}

func (s *Set) start(rule profile.LocalForward) *entry {
	ctx, cancel := context.WithCancel(s.ctx)
	e := &entry{rule: rule, cancel: cancel, done: make(chan struct{})}
	var err error
	if rule.Protocol == profile.PortForwardUDP {
		err = s.startUDP(ctx, e)
	} else {
		err = s.startTCP(ctx, e)
	}
	if err != nil {
		cancel()
		close(e.done)
		e.status = StatusError
		e.err = bindMessage(err)
		return e
	}
	e.status = StatusActive
	return e
}

func (e *entry) stop() {
	e.cancel()
	if e.closer != nil {
		e.closer()
	}
	<-e.done
}

func bindMessage(err error) string {
	if errors.Is(err, syscall.EADDRINUSE) {
		return ErrPortInUse.Error()
	}
	return err.Error()
}
