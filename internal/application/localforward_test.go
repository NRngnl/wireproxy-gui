package application

import (
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func TestStaleSaveKeepsLocalForwardsAndRuleErrorKeepsStatus(t *testing.T) {
	item := profile.NewTailscale("tailnet", 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.setRunning(item.ID, true)
	service.handleRuntimeEvent(connection.Event{Type: connection.EventStarted, ProfileID: item.ID, At: time.Now()})
	stale, _ := service.Profile(item.ID)

	rule := profile.LocalForward{ListenAddr: "2222", TargetAddr: "pc:22"}
	err := service.AddLocalForward(t.Context(), item.ID, rule)
	if err != nil {
		t.Fatal(err)
	}
	stale.Name = "renamed"
	_, err = service.Save(stale)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := service.Profile(item.ID)
	if got.Name != "renamed" || len(got.LocalForwards) != 1 || got.LocalForwards[0].ListenAddr != "127.0.0.1:2222" {
		t.Fatalf("profile after stale save = %#v", got)
	}
	runtime.mu.Lock()
	applied := len(runtime.appliedForwards)
	runtime.mu.Unlock()
	if applied == 0 {
		t.Fatal("running profile was not reconciled")
	}

	runtime.forwardStates = []connection.LocalForwardState{{Key: got.LocalForwards[0].Key(), Status: connection.LocalForwardError, Err: "port in use"}}
	if status := service.Status(item.ID); status != StatusRunning {
		t.Fatalf("status = %q, want running despite rule error", status)
	}
	if states := service.LocalForwardStates(item.ID); len(states) != 1 || states[0].Err != "port in use" {
		t.Fatalf("states = %#v", states)
	}
}
