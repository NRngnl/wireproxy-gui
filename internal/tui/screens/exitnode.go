package screens

import (
	"context"
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
)

// ExitNodeItem adapts a connection.ExitNode into a list.Item for the exit
// node picker.
type ExitNodeItem struct {
	Node connection.ExitNode
}

// FilterValue implements list.Item.
func (i ExitNodeItem) FilterValue() string {
	return i.Node.Name
}

// Title returns the display title: an online/offline glyph plus the node
// name.
func (i ExitNodeItem) Title() string {
	glyph := "\u25cb"
	style := styleDisconnected
	if i.Node.Online {
		glyph = "\u25cf"
		style = styleConnected
	}
	return style.Render(glyph) + " " + i.Node.Name
}

// Description returns the node's tailnet IPs joined for display.
func (i ExitNodeItem) Description() string {
	desc := ""
	for idx, ip := range i.Node.TailscaleIPs {
		if idx > 0 {
			desc += ", "
		}
		desc += ip
	}
	return desc
}

// ID returns the exit node ID.
func (i ExitNodeItem) ID() string {
	return i.Node.ID
}

type exitNodeDelegate struct{}

func (d exitNodeDelegate) Height() int                         { return 2 }
func (d exitNodeDelegate) Spacing() int                        { return 1 }
func (d exitNodeDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d exitNodeDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(ExitNodeItem)
	if !ok {
		return
	}

	title := item.Title()
	desc := styleNormalDesc.Render(item.Description())

	if index == m.Index() {
		title = styleSelected.Render("> " + title)
	} else {
		title = styleNormalTitle.Render(title)
	}

	fmt.Fprintf(w, "%s\n%s", title, desc)
}

// ExitNodePicker wraps a bubbles/list.Model of exit nodes, plus a Loading
// flag the caller can render as a spinner/placeholder while an async
// ExitNodes(ctx, profileID) call is in flight.
type ExitNodePicker struct {
	List    list.Model
	Loading bool
}

// NewExitNodePicker constructs an empty picker sized to width/height. Call
// SetExitNodes once results arrive.
func NewExitNodePicker(width, height int) *ExitNodePicker {
	l := list.New(nil, exitNodeDelegate{}, width, height)
	l.Title = "Exit nodes"
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	return &ExitNodePicker{List: l}
}

// SetExitNodes replaces the picker's items with the given exit nodes and
// clears the Loading flag.
func (p *ExitNodePicker) SetExitNodes(nodes []connection.ExitNode) {
	items := make([]list.Item, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, ExitNodeItem{Node: n})
	}
	p.List.SetItems(items)
	p.Loading = false
}

// SetLoading toggles the Loading flag, e.g. true just before dispatching
// LoadExitNodesCmd and false once ExitNodesLoadedMsg/ExitNodesErrMsg
// arrives.
func (p *ExitNodePicker) SetLoading(loading bool) {
	p.Loading = loading
}

// Selected returns the currently highlighted exit node, if any.
func (p *ExitNodePicker) Selected() (connection.ExitNode, bool) {
	item, ok := p.List.SelectedItem().(ExitNodeItem)
	if !ok {
		return connection.ExitNode{}, false
	}
	return item.Node, true
}

// View renders the picker, or a loading placeholder while Loading is true.
func (p *ExitNodePicker) View() string {
	if p.Loading {
		return "Loading exit nodes\u2026"
	}
	return p.List.View()
}

// exitNodeSource is the minimal Application surface LoadExitNodesCmd needs;
// internal/application.Service satisfies it via its ExitNodes method.
type exitNodeSource interface {
	ExitNodes(context.Context, string) ([]connection.ExitNode, error)
}

// ExitNodesLoadedMsg carries a successful exit-node fetch result back into
// the Bubble Tea Update loop.
type ExitNodesLoadedMsg struct {
	ProfileID string
	Nodes     []connection.ExitNode
}

// ExitNodesErrMsg carries a failed exit-node fetch result back into the
// Bubble Tea Update loop.
type ExitNodesErrMsg struct {
	ProfileID string
	Err       error
}

// LoadExitNodesCmd returns a tea.Cmd that calls app.ExitNodes(ctx,
// profileID) and reports the result as an ExitNodesLoadedMsg or
// ExitNodesErrMsg. The caller (model.go) owns dispatching this command and
// handling the resulting messages.
func LoadExitNodesCmd(app exitNodeSource, ctx context.Context, profileID string) tea.Cmd {
	return func() tea.Msg {
		nodes, err := app.ExitNodes(ctx, profileID)
		if err != nil {
			return ExitNodesErrMsg{ProfileID: profileID, Err: err}
		}
		return ExitNodesLoadedMsg{ProfileID: profileID, Nodes: nodes}
	}
}
