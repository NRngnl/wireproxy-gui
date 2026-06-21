package connection

import "time"

type EventType string

const (
	EventLog     EventType = "log"
	EventStarted EventType = "started"
	EventStopped EventType = "stopped"
	EventError   EventType = "error"
)

type Event struct {
	Type        EventType
	ProfileID   string
	ProfileName string
	Message     string
	At          time.Time
}
