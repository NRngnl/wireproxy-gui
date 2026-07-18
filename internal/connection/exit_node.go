package connection

import "errors"

var (
	ErrAlreadyConnected     = errors.New("profile is already connected")
	ErrExitNodesUnavailable = errors.New("exit nodes are only available for running Tailscale profiles")
	ErrInvalidProfileID     = errors.New("invalid Tailscale profile ID")
	ErrNotRunning           = errors.New("tailscale profile is not connected")
	ErrNotTailscale         = errors.New("profile is not a Tailscale profile")
)

type ExitNode struct {
	ID           string
	Name         string
	Online       bool
	TailscaleIPs []string
}
