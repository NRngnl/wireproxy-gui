package application

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// ErrLocalForwardPortInUse is the state message for a listen address already bound by another process.
var ErrLocalForwardPortInUse = errors.New("port in use")

// ErrLocalForwardNotFound reports an edit of a rule the profile does not have.
var ErrLocalForwardNotFound = errors.New("local forward was not found")

// LocalForwardStatus is the live status of one local forward rule.
type LocalForwardStatus = connection.LocalForwardStatus

// LocalForwardSaved marks a rule that is stored but not listening because the profile is not connected.
const LocalForwardSaved LocalForwardStatus = "saved"

const (
	LocalForwardActive = connection.LocalForwardActive
	LocalForwardError  = connection.LocalForwardError
)

// LocalForwardState reports the live status of the rule identified by Key.
type LocalForwardState = connection.LocalForwardState

// AddLocalForward stores a new rule and, while the profile is connected, starts listening on it.
func (s *Service) AddLocalForward(ctx context.Context, profileID string, rule profile.LocalForward) error {
	return s.editLocalForwards(ctx, profileID, func(rules []profile.LocalForward) ([]profile.LocalForward, error) {
		return append(rules, rule), nil
	})
}

// UpdateLocalForward replaces the rule identified by oldKey in place.
func (s *Service) UpdateLocalForward(ctx context.Context, profileID string, oldKey profile.LocalForwardKey, rule profile.LocalForward) error {
	oldKey = profile.NormalizeLocalForwardKey(oldKey)
	return s.editLocalForwards(ctx, profileID, func(rules []profile.LocalForward) ([]profile.LocalForward, error) {
		index := slices.IndexFunc(rules, func(r profile.LocalForward) bool { return r.Key() == oldKey })
		if index < 0 {
			return nil, ErrLocalForwardNotFound
		}
		rules[index] = rule
		return rules, nil
	})
}

// RemoveLocalForward deletes the rule and closes its listener and sessions.
func (s *Service) RemoveLocalForward(ctx context.Context, profileID string, key profile.LocalForwardKey) error {
	key = profile.NormalizeLocalForwardKey(key)
	return s.editLocalForwards(ctx, profileID, func(rules []profile.LocalForward) ([]profile.LocalForward, error) {
		index := slices.IndexFunc(rules, func(r profile.LocalForward) bool { return r.Key() == key })
		if index < 0 {
			return nil, ErrLocalForwardNotFound
		}
		return slices.Delete(rules, index, index+1), nil
	})
}

// RetryLocalForward tries to bind a rule that is in the error state again.
func (s *Service) RetryLocalForward(_ context.Context, profileID string, key profile.LocalForwardKey) error {
	if s.runtime == nil {
		return ErrRuntimeUnavailable
	}
	s.runtime.RetryLocalForward(profileID, profile.NormalizeLocalForwardKey(key))
	s.notify(Change{ProfileID: profileID})
	return nil
}

// LocalForwardStates returns the live state of the rules of a running profile; it is empty otherwise.
func (s *Service) LocalForwardStates(profileID string) []LocalForwardState {
	s.mu.Lock()
	running := s.statuses[profileID] == StatusRunning
	s.mu.Unlock()
	if !running || s.runtime == nil {
		return nil
	}
	return s.runtime.LocalForwardStates(profileID)
}

// LocalForwardTargets suggests peer names for the target field of a connected profile.
func (s *Service) LocalForwardTargets(profileID string) []string {
	if s.runtime == nil {
		return nil
	}
	return s.runtime.LocalForwardTargets(profileID)
}

func (s *Service) editLocalForwards(ctx context.Context, profileID string, edit func([]profile.LocalForward) ([]profile.LocalForward, error)) error {
	s.mu.Lock()
	index := s.profileIndexLocked(profileID)
	if index < 0 {
		s.mu.Unlock()
		return ErrProfileNotFound
	}
	updated := s.profiles[index]
	rules, err := edit(slices.Clone(updated.LocalForwards))
	if err != nil {
		s.mu.Unlock()
		return err
	}
	updated.LocalForwards = rules
	updated.Normalize()
	err = updated.Validate()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	updated.Touch()
	next := cloneProfiles(s.profiles)
	next[index] = updated
	err = s.repository.Save(next)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.profiles = next
	s.appendLogLocked(profileID, time.Now(), "saved local forwards")
	s.mu.Unlock()
	err = s.syncLocalForwards(ctx, profileID)
	s.notify(Change{ProfileID: profileID})
	return err
}

func (s *Service) syncLocalForwards(ctx context.Context, profileID string) error {
	if s.runtime == nil {
		return nil
	}
	s.forwards.Lock()
	defer s.forwards.Unlock()
	s.mu.Lock()
	item, ok := s.profileLocked(profileID)
	running := ok && s.statuses[profileID] == StatusRunning
	s.mu.Unlock()
	if !running {
		return nil
	}
	return s.runtime.ApplyLocalForwards(ctx, profileID, item.LocalForwards)
}

func (s *Service) profileLocked(profileID string) (profile.Profile, bool) {
	index := s.profileIndexLocked(profileID)
	if index < 0 {
		return profile.Profile{}, false
	}
	item := s.profiles[index]
	item.LocalForwards = slices.Clone(item.LocalForwards)
	return item, true
}
