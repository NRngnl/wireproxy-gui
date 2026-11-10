//go:build js

package ui

import (
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
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

func TestStaleExitNodeResponseIsIgnored(t *testing.T) {
	w := &workbench{exitBusy: true, exitSeq: 2}
	w.draft.ID = "b"
	w.applyExitNodes("a", 1, []connection.ExitNode{{ID: "x"}}, nil)
	w.applyExitNodes("b", 1, []connection.ExitNode{{ID: "x"}}, nil)
	if len(w.exitNodes) != 0 || !w.exitBusy {
		t.Fatalf("stale response applied: %#v", w.exitNodes)
	}
	w.applyExitNodes("b", 2, []connection.ExitNode{{ID: "x"}}, nil)
	if len(w.exitNodes) != 1 {
		t.Fatal("current response dropped")
	}
}

func TestAuthenticationClearsConsumedAuthKey(t *testing.T) {
	p := profile.NewTailscale("travel", 1082)
	w := &workbench{}
	w.draft = newDraft(p)
	w.draft.AuthKey = "tskey-secret"
	p.TailscaleConfig.Authenticated = true
	w.apply(presentationFrame{Selected: &selectedProfile{Profile: p}}, application.Change{})
	if w.draft.AuthKey != "" {
		t.Fatal("auth key kept after authentication")
	}
	saved, err := w.draft.apply(p)
	if err != nil || saved.TailscaleConfig.AuthKey != "" || !saved.TailscaleConfig.Authenticated {
		t.Fatalf("saved = %#v, err = %v", saved.TailscaleConfig, err)
	}
}
