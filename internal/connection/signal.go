package connection

import "sync"

// Signal collects the IDs of profiles whose local forward state changed. Marks for the same profile coalesce
// until Take, so it never drops a change and holds at most one entry per profile.
type Signal struct {
	mu      sync.Mutex
	pending map[string]struct{}
	ready   chan struct{}
}

// NewSignal returns an empty Signal.
func NewSignal() *Signal {
	return &Signal{pending: map[string]struct{}{}, ready: make(chan struct{}, 1)}
}

// Mark records that profileID changed.
func (s *Signal) Mark(profileID string) {
	s.mu.Lock()
	s.pending[profileID] = struct{}{}
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Ready receives after Mark when changes may be pending.
func (s *Signal) Ready() <-chan struct{} {
	return s.ready
}

// Take returns and clears the pending profile IDs.
func (s *Signal) Take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	clear(s.pending)
	return ids
}
