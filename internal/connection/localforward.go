package connection

import "github.com/NRngnl/wireproxy-gui/internal/profile"

// LocalForwardStatus is the live status of one local forward rule.
type LocalForwardStatus string

const (
	LocalForwardActive LocalForwardStatus = "active"
	LocalForwardError  LocalForwardStatus = "error"
)

// LocalForwardState reports one rule's status; Err is the bind failure for LocalForwardError, or the last dial failure for LocalForwardActive.
type LocalForwardState struct {
	Key    profile.LocalForwardKey
	Target string
	Status LocalForwardStatus
	Err    string
	Path   PeerPath
}

// PeerPathKind tells how a tailnet peer is currently reached.
type PeerPathKind string

const (
	PeerPathUnknown   PeerPathKind = ""
	PeerPathDirect    PeerPathKind = "direct"
	PeerPathRelay     PeerPathKind = "relay"
	PeerPathPeerRelay PeerPathKind = "peer-relay"
)

// PeerPath is the current route to the tailnet peer a rule targets. Relay names the DERP region for PeerPathRelay, or the relay address for PeerPathPeerRelay.
type PeerPath struct {
	Kind  PeerPathKind
	Relay string
}
