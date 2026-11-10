//go:build js

package ui

import (
	"context"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

type appearance int

const (
	appearanceAuto appearance = iota
	appearanceLight
	appearanceDark
)

type profileRow struct {
	ID, Name, Summary string
	Status            application.Status
}

type selectedProfile struct {
	Profile profile.Profile
	Status  application.Status
	Locked  bool
	Logs    []application.LogEntry
}

type presentationFrame struct {
	Summary  string
	Rows     []profileRow
	Selected *selectedProfile
}

type workbench struct {
	core       Application
	window     *mygo.Window
	selectedID atomic.Value
	wake       chan struct{}
	frame      presentationFrame
	notice     string
	noticeShut bool
	railOpen   bool
	width      float32
	look       int
	density    int
	logScroll  ui.ScrollState
	draft      draft
	exitNodes  []connection.ExitNode
	exitBusy   bool
	exitSeq    int
	fwdProto   int
	fwdPort    string
	fwdTarget  string
	info       string
	infoShut   bool
	confirmDel bool
	logID      string
	logTail    map[string]bool
	logY       map[string]float32
}

func newWorkbench(core Application, look appearance, compact, railOpen bool) *workbench {
	w := &workbench{core: core, wake: make(chan struct{}, 1), look: int(look), railOpen: railOpen, logTail: map[string]bool{}, logY: map[string]float32{}}
	if compact {
		w.density = 1
	}
	w.selectedID.Store("")
	w.frame = readFrame(core, "")
	if w.frame.Selected != nil {
		w.selectedID.Store(w.frame.Selected.Profile.ID)
	}
	return w
}

func (w *workbench) size() theme.Size {
	if w.density == 1 {
		return theme.Small
	}
	return theme.Middle
}

func (w *workbench) selectProfile(id string) {
	w.selectedID.Store(id)
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func readFrame(core Application, selectedID string) presentationFrame {
	profiles := core.Profiles()
	frame := presentationFrame{Rows: make([]profileRow, 0, len(profiles)), Summary: profilesSummaryText(profiles, func(id string) string { return string(core.Status(id)) })}
	var chosen *profile.Profile
	for i, p := range profiles {
		status := core.Status(p.ID)
		frame.Rows = append(frame.Rows, profileRow{ID: p.ID, Name: p.Name, Status: status, Summary: statusTitleText(string(status)) + " · " + p.BindAddress()})
		if p.ID == selectedID || (chosen == nil && i == 0) {
			chosen = &profiles[i]
		}
	}
	if chosen != nil {
		frame.Selected = &selectedProfile{
			Profile: *chosen,
			Status:  core.Status(chosen.ID),
			Locked:  core.RuntimeLocked(chosen.ID),
			Logs:    core.Logs(chosen.ID),
		}
	}
	return frame
}

func (w *workbench) observe(ctx context.Context) {
	w.publish(application.Change{})
	for {
		var change application.Change
		select {
		case <-ctx.Done():
			return
		case change = <-w.core.Changes():
		case <-w.wake:
		}
		w.publish(change)
	}
}

func (w *workbench) publish(change application.Change) {
	id, _ := w.selectedID.Load().(string)
	frame := readFrame(w.core, id)
	w.window.Update(func() { w.apply(frame, change) })
}

func (w *workbench) apply(frame presentationFrame, change application.Change) {
	w.frame = frame
	if sel := frame.Selected; sel != nil && sel.Profile.TailscaleConfig.Authenticated && w.draft.ID == sel.Profile.ID {
		w.draft.AuthKey = ""
	}
	if change.Err != nil {
		w.notice = displayErrorText(operationErrorTitle(change.Operation), change.Err)
		w.noticeShut = false
	}
}

func (w *workbench) report(title string, err error) {
	if err == nil {
		return
	}
	w.window.Update(func() {
		w.notice = displayErrorText(title, err)
		w.noticeShut = false
	})
}

func (w *workbench) applyLook(c *ui.Context) {
	switch appearance(w.look) {
	case appearanceLight:
		c.SetTheme(theme.Light().Theme())
	case appearanceDark:
		c.SetTheme(theme.Dark().Theme())
	default:
		theme.Apply(c)
	}
}
