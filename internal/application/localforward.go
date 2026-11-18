package application

import (
	"context"
	"errors"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

// ErrLocalForwardsNotImplemented is returned until the local forward backend exists.
var ErrLocalForwardsNotImplemented = errors.New("local forwards are not implemented yet")

// ErrLocalForwardPortInUse is the state message for a listen address already bound by another process.
var ErrLocalForwardPortInUse = errors.New("port in use")

// LocalForwardStatus is the live state of one local forward rule.
type LocalForwardStatus string

const (
	LocalForwardSaved  LocalForwardStatus = "saved"
	LocalForwardActive LocalForwardStatus = "active"
	LocalForwardError  LocalForwardStatus = "error"
)

// LocalForwardState reports the live status of the rule identified by Key.
type LocalForwardState struct {
	Key    profile.LocalForwardKey
	Status LocalForwardStatus
	Err    string
}

func (*Service) AddLocalForward(context.Context, string, profile.LocalForward) error {
	return ErrLocalForwardsNotImplemented
}

func (*Service) UpdateLocalForward(context.Context, string, profile.LocalForwardKey, profile.LocalForward) error {
	return ErrLocalForwardsNotImplemented
}

func (*Service) RemoveLocalForward(context.Context, string, profile.LocalForwardKey) error {
	return ErrLocalForwardsNotImplemented
}

func (*Service) RetryLocalForward(context.Context, string, profile.LocalForwardKey) error {
	return ErrLocalForwardsNotImplemented
}

func (*Service) LocalForwardStates(string) []LocalForwardState { return nil }

func (*Service) LocalForwardTargets(string) []string { return nil }
