package connection

import "time"

type EventType string

const (
	EventLog     EventType = "log"
	EventStarted EventType = "started"
	EventStopped EventType = "stopped"
	EventError   EventType = "error"

	// EventLocalForwardChanged reports that a local forward rule changed state.
	EventLocalForwardChanged EventType = "local_forward_changed"
)

type Event struct {
	Type        EventType
	ProfileID   string
	ProfileName string
	Message     string
	At          time.Time
}
