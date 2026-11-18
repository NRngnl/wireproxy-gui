package connection

import "github.com/NRngnl/wireproxy-gui/internal/profile"

// LocalForwardStatus is the live status of one local forward rule.
type LocalForwardStatus string

const (
	LocalForwardActive LocalForwardStatus = "active"
	LocalForwardError  LocalForwardStatus = "error"
)

// LocalForwardState reports one rule's status; Err is set when Status is LocalForwardError.
type LocalForwardState struct {
	Key    profile.LocalForwardKey
	Status LocalForwardStatus
	Err    string
}
