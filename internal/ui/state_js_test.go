//go:build js

package ui

import (
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func TestLogUpdateKeepsEditedDraft(t *testing.T) {
	p := profile.New("home", "", 1080)
	w := &workbench{}
	w.frame = presentationFrame{Selected: &selectedProfile{Profile: p}}
	w.syncDraft()
	w.draft.Name = "edited"
	next := presentationFrame{Selected: &selectedProfile{Profile: p, Logs: []application.LogEntry{{Message: "tick"}}}}
	w.apply(next, application.Change{})
	w.syncDraft()
	if w.draft.Name != "edited" {
		t.Fatalf("draft name = %q", w.draft.Name)
	}
}
