package daemon

import (
	"io"
	"log/slog"
)

// NewLogger builds a structured logger for the daemon. When jsonFormat is
// true, logs are emitted as JSON lines; otherwise a human-readable text
// format is used.
func NewLogger(w io.Writer, jsonFormat bool) *slog.Logger {
	var handler slog.Handler
	if jsonFormat {
		handler = slog.NewJSONHandler(w, nil)
	} else {
		handler = slog.NewTextHandler(w, nil)
	}
	return slog.New(handler)
}
