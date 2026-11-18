package application

import (
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func startedService(t *testing.T) (*Service, profile.Profile, *fakeRuntime) {
	t.Helper()
	item := profile.NewTailscale("tailnet", 1080)
	service, _, _, runtime := newLoadedService(t, item)
	runtime.setRunning(item.ID, true)
	service.handleRuntimeEvent(connection.Event{Type: connection.EventStarted, ProfileID: item.ID, At: time.Now()})
	return service, item, runtime
}

func lastApplied(r *fakeRuntime) ([]profile.LocalForward, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.appliedForwards) == 0 {
		return nil, 0
	}
	return r.appliedForwards[len(r.appliedForwards)-1], len(r.appliedForwards)
}

func TestLiveEditsApplyLatestSavedRulesAndStaleSaveKeepsThem(t *testing.T) {
	service, item, runtime := startedService(t)
	stale, _ := service.Profile(item.ID)
	_, before := lastApplied(runtime)

	rule := profile.LocalForward{ListenAddr: "2222", TargetAddr: "pc:22"}
	err := service.AddLocalForward(t.Context(), item.ID, rule)
	if err != nil {
		t.Fatal(err)
	}
	got, after := lastApplied(runtime)
	if after != before+1 || len(got) != 1 || got[0].ListenAddr != "127.0.0.1:2222" {
		t.Fatalf("add not applied live: %#v (%d -> %d)", got, before, after)
	}

	stale.Name = "renamed"
	_, err = service.Save(stale)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := service.Profile(item.ID)
	if saved.Name != "renamed" || len(saved.LocalForwards) != 1 {
		t.Fatalf("stale save lost rules: %#v", saved)
	}

	err = service.RemoveLocalForward(t.Context(), item.ID, saved.LocalForwards[0].Key())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := lastApplied(runtime); len(got) != 0 {
		t.Fatalf("removal not applied: %#v", got)
	}
}

func TestFailedSaveDoesNotApplyAndRuleErrorKeepsProfileRunning(t *testing.T) {
	service, item, runtime := startedService(t)
	_, before := lastApplied(runtime)
	err := service.AddLocalForward(t.Context(), item.ID, profile.LocalForward{ListenAddr: "bad", TargetAddr: "pc:22"})
	if err == nil {
		t.Fatal("invalid rule accepted")
	}
	if _, after := lastApplied(runtime); after != before {
		t.Fatal("rejected edit reached the runtime")
	}
	service.handleRuntimeEvent(connection.Event{Type: connection.EventLocalForwardChanged, ProfileID: item.ID})
	if status := service.Status(item.ID); status != StatusRunning {
		t.Fatalf("status = %q after rule event", status)
	}
}

func TestProfileSnapshotDoesNotAliasStoredRules(t *testing.T) {
	service, item, _ := startedService(t)
	err := service.AddLocalForward(t.Context(), item.ID, profile.LocalForward{ListenAddr: "2222", TargetAddr: "pc:22"})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := service.Profile(item.ID)
	snap.LocalForwards[0].TargetAddr = "evil:1"
	again, _ := service.Profile(item.ID)
	if again.LocalForwards[0].TargetAddr != "pc:22" {
		t.Fatal("snapshot aliases stored rules")
	}
}

func TestConnectAllStartsWithClonedRules(t *testing.T) {
	item := profile.New("wg", sampleWG, 1080)
	item.LocalForwards = []profile.LocalForward{{ListenAddr: "127.0.0.1:2222", TargetAddr: "pc:22"}}
	service, _, _, runtime := newLoadedService(t, item)
	runtime.startSetsRunning = true
	err := service.ConnectAll()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		n := len(runtime.startProfiles)
		runtime.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	runtime.mu.Lock()
	started := runtime.startProfiles[0]
	runtime.mu.Unlock()
	started.LocalForwards[0].TargetAddr = "evil:1"
	got, _ := service.Profile(item.ID)
	if got.LocalForwards[0].TargetAddr != "pc:22" {
		t.Fatal("started profile aliases stored rules")
	}
}
