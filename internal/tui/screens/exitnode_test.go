package screens

import (
	"context"
	"errors"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
)

func TestExitNodePickerSetExitNodesClearsLoading(t *testing.T) {
	p := NewExitNodePicker(40, 10)
	p.SetLoading(true)
	if !p.Loading {
		t.Fatalf("expected Loading true after SetLoading(true)")
	}

	nodes := []connection.ExitNode{
		{ID: "n1", Name: "node-one", Online: true, TailscaleIPs: []string{"100.64.0.1"}},
		{ID: "n2", Name: "node-two", Online: false},
	}
	p.SetExitNodes(nodes)

	if p.Loading {
		t.Fatalf("expected Loading false after SetExitNodes")
	}
	if got := len(p.List.Items()); got != 2 {
		t.Fatalf("expected 2 items, got %d", got)
	}

	selected, ok := p.Selected()
	if !ok {
		t.Fatalf("expected a selected item")
	}
	if selected.ID != "n1" {
		t.Fatalf("expected first item selected, got %q", selected.ID)
	}
}

func TestExitNodeItemRendering(t *testing.T) {
	online := ExitNodeItem{Node: connection.ExitNode{ID: "n1", Name: "us-east", Online: true, TailscaleIPs: []string{"100.1.2.3", "100.1.2.4"}}}
	if got := online.FilterValue(); got != "us-east" {
		t.Fatalf("FilterValue() = %q, want %q", got, "us-east")
	}
	if got := online.ID(); got != "n1" {
		t.Fatalf("ID() = %q, want %q", got, "n1")
	}
	if got := online.Description(); got != "100.1.2.3, 100.1.2.4" {
		t.Fatalf("Description() = %q, want %q", got, "100.1.2.3, 100.1.2.4")
	}
}

type fakeExitNodeSource struct {
	nodes []connection.ExitNode
	err   error
}

func (f fakeExitNodeSource) ExitNodes(ctx context.Context, profileID string) ([]connection.ExitNode, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.nodes, nil
}

func TestLoadExitNodesCmdSuccess(t *testing.T) {
	nodes := []connection.ExitNode{{ID: "n1", Name: "node-one"}}
	cmd := LoadExitNodesCmd(fakeExitNodeSource{nodes: nodes}, context.Background(), "profile-1")

	msg := cmd()
	loaded, ok := msg.(ExitNodesLoadedMsg)
	if !ok {
		t.Fatalf("expected ExitNodesLoadedMsg, got %T", msg)
	}
	if loaded.ProfileID != "profile-1" {
		t.Fatalf("ProfileID = %q, want %q", loaded.ProfileID, "profile-1")
	}
	if len(loaded.Nodes) != 1 || loaded.Nodes[0].ID != "n1" {
		t.Fatalf("unexpected nodes: %#v", loaded.Nodes)
	}
}

func TestLoadExitNodesCmdError(t *testing.T) {
	wantErr := errors.New("boom")
	cmd := LoadExitNodesCmd(fakeExitNodeSource{err: wantErr}, context.Background(), "profile-1")

	msg := cmd()
	errMsg, ok := msg.(ExitNodesErrMsg)
	if !ok {
		t.Fatalf("expected ExitNodesErrMsg, got %T", msg)
	}
	if errMsg.ProfileID != "profile-1" || !errors.Is(errMsg.Err, wantErr) {
		t.Fatalf("unexpected ExitNodesErrMsg: %#v", errMsg)
	}
}
