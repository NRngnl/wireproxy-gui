package connection

import "errors"

var ErrExitNodesUnavailable = errors.New("exit nodes are only available for running Tailscale profiles")

type ExitNode struct {
	ID           string
	Name         string
	Online       bool
	TailscaleIPs []string
}
