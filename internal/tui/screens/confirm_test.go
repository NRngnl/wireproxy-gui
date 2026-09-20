package screens

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestConfirmHandleKeyConfirms(t *testing.T) {
	c := NewConfirm("Delete profile \"foo\"?")
	if c.Resolved() {
		t.Fatalf("expected unresolved confirm before any key")
	}

	resolved, confirmed := c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !resolved || !confirmed {
		t.Fatalf("HandleKey('y') = (%v, %v), want (true, true)", resolved, confirmed)
	}
	if !c.Resolved() || !c.Confirmed() {
		t.Fatalf("expected Resolved()=true Confirmed()=true after 'y'")
	}
}

func TestConfirmHandleKeyCancelsOnAnyOtherKey(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune("n")},
		{Type: tea.KeyRunes, Runes: []rune("x")},
		{Type: tea.KeyEnter},
	} {
		c := NewConfirm("Logout?")
		resolved, confirmed := c.HandleKey(key)
		if !resolved || confirmed {
			t.Fatalf("HandleKey(%v) = (%v, %v), want (true, false)", key, resolved, confirmed)
		}
	}
}

func TestConfirmView(t *testing.T) {
	c := NewConfirm("Quit while connected?")
	want := "Quit while connected? (y/N)"
	if got := c.View(); got != want {
		t.Fatalf("View() = %q, want %q", got, want)
	}
}
