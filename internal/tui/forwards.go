package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/tui/screens"
)

func (m *model) openForwards() (tea.Model, tea.Cmd) {
	m.clearTransientMessages()
	if m.list.selectedID == "" {
		return m, nil
	}
	m.forwards = screens.NewLocalForwardsPanel(m.list.selectedID)
	m.mode = modeForwards
	m.refreshForwards()
	return m, nil
}

func (m *model) refreshForwards() {
	if m.mode != modeForwards || m.forwards == nil {
		return
	}
	item, ok := m.core.Profile(m.forwards.ProfileID)
	if !ok {
		m.closeModal()
		return
	}
	m.forwards.SetRules(item.LocalForwards)
	m.forwards.Running = m.core.Status(item.ID) == application.StatusRunning
	m.forwards.States = m.core.LocalForwardStates(item.ID)
}

func (m *model) updateForwards(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	panel := m.forwards
	action, cmd := panel.HandleKey(msg)
	if action == screens.LocalForwardsNone {
		return m, cmd
	}
	if action == screens.LocalForwardsClose {
		m.closeModal()
		return m, nil
	}
	panel.Busy = true
	id, rule, editing := panel.ProfileID, panel.Rule(), panel.Editing()
	selected, hasSelected := panel.Selected()
	core := m.core
	return m, func() tea.Msg {
		ctx := context.Background()
		var err error
		switch action {
		case screens.LocalForwardsAdd:
			err = core.AddLocalForward(ctx, id, rule)
		case screens.LocalForwardsUpdate:
			err = core.UpdateLocalForward(ctx, id, *editing, rule)
		case screens.LocalForwardsRemove:
			if hasSelected {
				err = core.RemoveLocalForward(ctx, id, selected.Key())
			}
		case screens.LocalForwardsRetry:
			if hasSelected {
				err = core.RetryLocalForward(ctx, id, selected.Key())
			}
		}
		return forwardResultMsg{profileID: id, action: action, err: err}
	}
}

type forwardResultMsg struct {
	profileID string
	action    screens.LocalForwardsAction
	err       error
}

func (m *model) applyForwardResult(msg forwardResultMsg) tea.Cmd {
	if m.mode != modeForwards || m.forwards == nil || m.forwards.ProfileID != msg.profileID {
		return nil
	}
	m.forwards.Busy = false
	if msg.err != nil {
		m.forwards.Err = msg.err.Error()
		if msg.action == screens.LocalForwardsAdd || msg.action == screens.LocalForwardsUpdate {
			return m.forwards.ReopenForm()
		}
		return nil
	}
	m.forwards.Err = ""
	if msg.action == screens.LocalForwardsAdd || msg.action == screens.LocalForwardsUpdate {
		m.forwards.CloseForm()
	}
	m.refreshForwards()
	return nil
}
