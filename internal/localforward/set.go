// Package localforward owns host listeners that relay to targets dialed through a backend.
package localforward

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/socksconn"
)

// ErrPortInUse is the rule error when the listen address is already bound.
var ErrPortInUse = errors.New("port in use")

// Status is the live status of one rule.
type Status = connection.LocalForwardStatus

// State reports one rule's status; Err is set when Status is StatusError.
type State = connection.LocalForwardState

const (
	StatusActive = connection.LocalForwardActive
	StatusError  = connection.LocalForwardError
)

// Dialer opens connections to a target through the backend, never through the host network.
type Dialer struct {
	TCP func(ctx context.Context, addr string) (net.Conn, error)
	UDP func(ctx context.Context, addr string) (net.Conn, error)
}

type entry struct {
	rule    profile.LocalForward
	status  Status
	err     string
	dialErr string
	cancel  context.CancelFunc
	closer  func()
	done    chan struct{}
}

// Set reconciles listeners against a rule list and tracks per-rule state.
type Set struct {
	ctx      context.Context
	dial     Dialer
	timeouts socksconn.Timeouts
	changed  func()

	op      sync.Mutex
	mu      sync.Mutex
	closed  bool
	entries map[profile.LocalForwardKey]*entry
	order   []profile.LocalForwardKey
}

// New creates a Set. Close ends every rule and is terminal; canceling ctx only stops in-flight dials.
// changed is called after state changes.
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

// Apply releases removed or retargeted rules, then binds new ones, leaving unchanged rules and their
// sessions alone. A failed bind is recorded as that rule's state, never returned. It does nothing after Close.
func (s *Set) Apply(next []profile.LocalForward) {
	s.op.Lock()
	defer s.op.Unlock()
	if s.isClosed() {
		return
	}
	wanted := make(map[profile.LocalForwardKey]profile.LocalForward, len(next))
	order := make([]profile.LocalForwardKey, 0, len(next))
	for _, rule := range next {
		wanted[rule.Key()] = rule
		order = append(order, rule.Key())
	}
	var stale []*entry
	s.mu.Lock()
	for key, e := range s.entries {
		rule, ok := wanted[key]
		if !ok || rule.TargetAddr != e.rule.TargetAddr {
			stale = append(stale, e)
			delete(s.entries, key)
		}
	}
	s.order = order
	s.mu.Unlock()
	for _, e := range stale {
		e.stop()
	}
	for _, key := range order {
		s.mu.Lock()
		_, exists := s.entries[key]
		s.mu.Unlock()
		if !exists {
			s.install(key, s.start(wanted[key]))
		}
	}
	s.changed()
}

// Retry rebinds a rule that is in the error state.
func (s *Set) Retry(key profile.LocalForwardKey) {
	s.op.Lock()
	defer s.op.Unlock()
	if s.isClosed() {
		return
	}
	s.mu.Lock()
	e, ok := s.entries[key]
	retry := ok && e.status == StatusError
	s.mu.Unlock()
	if !retry {
		return
	}
	e.stop()
	s.install(key, s.start(e.rule))
	s.changed()
}

// States returns the state of every rule in rule order. For an active rule Err holds the last dial failure, if any.
func (s *Set) States() []State {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]State, 0, len(s.order))
	for _, key := range s.order {
		e, ok := s.entries[key]
		if !ok {
			continue
		}
		msg := e.err
		if e.status == StatusActive {
			msg = e.dialErr
		}
		states = append(states, State{Key: key, Status: e.status, Err: msg})
	}
	return states
}

// Close stops every rule, waits for its goroutines and makes later Apply and Retry calls no-ops.
func (s *Set) Close() {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	s.closed = true
	entries := s.entries
	s.entries = map[profile.LocalForwardKey]*entry{}
	s.order = nil
	s.mu.Unlock()
	for _, e := range entries {
		e.stop()
	}
}

func (s *Set) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Set) install(key profile.LocalForwardKey, e *entry) {
	s.mu.Lock()
	s.entries[key] = e
	s.mu.Unlock()
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

func (s *Set) fail(ctx context.Context, e *entry, err error) {
	s.mu.Lock()
	live := ctx.Err() == nil && s.entries[e.rule.Key()] == e
	if live {
		e.status = StatusError
		e.err = "listener stopped: " + err.Error()
	}
	s.mu.Unlock()
	if live {
		e.closer()
		s.changed()
	}
}

func (s *Set) dialFailed(e *entry, err error) {
	s.mu.Lock()
	e.dialErr = err.Error()
	s.mu.Unlock()
	s.changed()
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
