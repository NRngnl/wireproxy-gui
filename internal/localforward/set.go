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

// ErrSessionLimitReached is the rule error when a profile's local forwards reach their derived share of the userspace port space.
var ErrSessionLimitReached = errors.New("local forward session limit reached")

// Status is the live status of one rule.
type Status = connection.LocalForwardStatus

// State reports one rule's status; Err is the bind failure for StatusError, or the last dial failure for StatusActive.
type State = connection.LocalForwardState

const (
	StatusActive = connection.LocalForwardActive
	StatusError  = connection.LocalForwardError
)

// Dialer opens connections to a target through the backend, never through the host network.
// EphemeralPorts optionally overrides the backend stack's ephemeral port count; zero reads gVisor's default range.
type Dialer struct {
	TCP            func(ctx context.Context, addr string) (net.Conn, error)
	UDP            func(ctx context.Context, addr string) (net.Conn, error)
	EphemeralPorts int
}

type entry struct {
	rule    profile.LocalForward
	status  Status
	err     string
	dialErr string
	cancel  context.CancelFunc
	closer  func()
	unwatch func() bool
}

// Set reconciles listeners against a rule list and tracks per-rule state.
type Set struct {
	ctx      context.Context
	dial     Dialer
	timeouts socksconn.Timeouts
	changed  func()

	tcpBudget slotBudget
	udpBudget slotBudget

	op      sync.Mutex
	running sync.WaitGroup
	mu      sync.Mutex
	closed  bool
	entries map[profile.LocalForwardKey]*entry
	order   []profile.LocalForwardKey
}

// New creates a Set. Canceling ctx closes every listener and session without waiting; Close also waits for
// them and is terminal.
// changed is called after state changes.
func New(ctx context.Context, dial Dialer, timeouts socksconn.Timeouts, changed func()) *Set {
	if changed == nil {
		changed = func() {}
	}
	tcpLimit, udpLimit := derivePortLimits(dial.EphemeralPorts, processFDLimit())
	return &Set{
		ctx:       ctx,
		dial:      dial,
		timeouts:  timeouts.WithDefaults(),
		changed:   changed,
		tcpBudget: slotBudget{limit: int64(tcpLimit)},
		udpBudget: slotBudget{limit: int64(udpLimit)},
		entries:   map[profile.LocalForwardKey]*entry{},
	}
}

// Apply releases removed or retargeted rules, then binds new ones, leaving unchanged rules and their
// sessions alone. Rule keys must be unique, as profile validation guarantees. A failed bind is recorded as that rule's state, never returned. It does nothing after Close.
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
			s.start(wanted[key])
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
	s.start(e.rule)
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
		states = append(states, State{Key: key, Target: e.rule.TargetAddr, Status: e.status, Err: msg})
	}
	return states
}

// Close stops every rule, waits for the goroutines of current and released rules and makes later Apply and
// Retry calls no-ops.
func (s *Set) Close() {
	s.op.Lock()
	s.mu.Lock()
	s.closed = true
	entries := s.entries
	s.entries = map[profile.LocalForwardKey]*entry{}
	s.order = nil
	s.mu.Unlock()
	for _, e := range entries {
		e.stop()
	}
	s.op.Unlock()
	s.running.Wait()
}

func (s *Set) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed || s.ctx.Err() != nil
}

func (s *Set) start(rule profile.LocalForward) {
	ctx, cancel := context.WithCancel(s.ctx)
	e := &entry{rule: rule, cancel: cancel}
	var run func()
	var err error
	if rule.Protocol == profile.PortForwardUDP {
		run, err = s.bindUDP(ctx, e)
	} else {
		run, err = s.bindTCP(ctx, e)
	}
	if err != nil {
		cancel()
		e.status = StatusError
		e.err = bindMessage(err)
	} else {
		e.status = StatusActive
		e.unwatch = context.AfterFunc(ctx, e.closer)
	}
	s.mu.Lock()
	s.entries[rule.Key()] = e
	s.mu.Unlock()
	if run != nil {
		s.running.Go(run)
	}
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
		e.cancel()
		e.closer()
		s.changed()
	}
}

func (s *Set) dialSucceeded(e *entry) {
	s.mu.Lock()
	changed := e.dialErr != ""
	e.dialErr = ""
	s.mu.Unlock()
	if changed {
		s.changed()
	}
}

func (s *Set) dialFailed(e *entry, err error) {
	s.mu.Lock()
	changed := e.dialErr != err.Error()
	e.dialErr = err.Error()
	s.mu.Unlock()
	if changed {
		s.changed()
	}
}

func (e *entry) stop() {
	if e.unwatch != nil {
		e.unwatch()
	}
	e.cancel()
	if e.closer != nil {
		e.closer()
	}
}

func bindMessage(err error) string {
	if errors.Is(err, syscall.EADDRINUSE) {
		return ErrPortInUse.Error()
	}
	return err.Error()
}
