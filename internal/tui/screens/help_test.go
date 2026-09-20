package screens

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
)

type fakeBindings struct {
	short []key.Binding
	full  [][]key.Binding
}

func (f fakeBindings) ShortHelp() []key.Binding  { return f.short }
func (f fakeBindings) FullHelp() [][]key.Binding { return f.full }

func TestFullHelpViewRendersBindingHelp(t *testing.T) {
	up := key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	add := key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add"))

	bindings := fakeBindings{
		short: []key.Binding{up},
		full:  [][]key.Binding{{up}, {add}},
	}

	view := FullHelpView(bindings, 80)
	if !strings.Contains(view, "up") {
		t.Fatalf("expected full help view to contain %q, got %q", "up", view)
	}
	if !strings.Contains(view, "add") {
		t.Fatalf("expected full help view to contain %q, got %q", "add", view)
	}
}

func TestShortHelpViewRendersBindingHelp(t *testing.T) {
	quit := key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit"))
	bindings := fakeBindings{short: []key.Binding{quit}}

	view := ShortHelpView(bindings, 80)
	if !strings.Contains(view, "quit") {
		t.Fatalf("expected short help view to contain %q, got %q", "quit", view)
	}
}
