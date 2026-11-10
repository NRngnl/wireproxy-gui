package ui

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/NRngnl/wireproxy-gui/internal/application"
)

const narrowBelow = 720

func (w *workbench) view(c *ui.Context) {
	w.applyLook(c)
	tk := theme.TokensOf(c)
	root := ui.Row(c).Fill().Background(tk.BgLayout).AlignItems(ui.Stretch)
	if w.window != nil {
		cw, ch := w.window.ContentSize()
		w.width, w.height = float32(cw), float32(ch)
	}
	narrow := w.width > 0 && w.width < narrowBelow
	root.Children(func() {
		if !narrow || w.railOpen {
			w.rail(c, tk, narrow)
		}
		if !narrow || !w.railOpen {
			w.detail(c, tk, narrow)
		}
		w.usageGuide(c)
	})
}

func statusColor(s application.Status) display.TagColor {
	switch s {
	case application.StatusRunning:
		return display.TagSuccess
	case application.StatusStarting, application.StatusStopping:
		return display.TagWarning
	case application.StatusError:
		return display.TagError
	default:
		return ""
	}
}

func (w *workbench) rail(c *ui.Context, tk *theme.Tokens, narrow bool) {
	if narrow {
		ui.Column(c).Padding(10).Gap(4).Background(tk.BgLayout).Grow(1).MinWidth(0).Children(func() { w.railBody(c, tk) })
		return
	}
	expanded := true
	layout.DraggableSideNav(c, &w.railWidth, &expanded, layout.DraggableSideNavProps{MinWidth: 240, MaxWidth: max(240, w.width-480), NoExpand: true},
		func(layout.SideNavState) {
			ui.Column(c).Padding(10).Gap(4).MinHeight(w.height).Children(func() { w.railBody(c, tk) })
		})
}

func (w *workbench) detail(c *ui.Context, tk *theme.Tokens, narrow bool) {
	sel := w.frame.Selected
	pad := narrow2(narrow, 14, 24)
	ui.Column(c).Grow(1).Basis(0).MinWidth(0).Background(tk.BgContainer).Children(func() {
		ui.Column(c).Padding(pad, pad, 14, pad).Gap(10).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(tk.BorderSecondary).Children(func() {
			if narrow {
				ui.Row(c).Children(func() {
					if action.Button(c.Key("rail"), "← "+tr("Profiles"), action.ButtonProps{Variant: action.Link, Size: w.size()}).Clicked() {
						w.railOpen = true
					}
				})
			}
			if sel != nil {
				w.syncDraft()
				w.profileHeader(c, tk, sel)
			}
		})
		ui.Scroll(c).Grow(1).Basis(0).MinHeight(0).Children(func() {
			ui.Column(c).Padding(16, pad, pad, pad).Gap(16).MaxWidth(contentWidth + 2*pad).Children(func() {
				if w.notice != "" && !w.noticeShut {
					feedback.Alert(c, w.notice, feedback.AlertProps{Type: feedback.Error, Closed: &w.noticeShut})
				}
				if sel == nil {
					display.Empty(c, display.EmptyProps{Icon: lucide.Inbox(), Title: tr("No profiles yet"),
						Description: tr("Add a profile or import WireGuard configuration to begin.")})
					ui.Row(c).Justify(ui.Center).Gap(8).Children(func() { w.addImport(c, "empty") })
					return
				}
				if w.info != "" && !w.infoShut {
					feedback.Alert(c, w.info, feedback.AlertProps{Type: feedback.Success, Closed: &w.infoShut})
				}
				w.form(c, tk, sel.Locked)
				w.activity(c, tk, sel)
			})
		})
		if sel != nil {
			w.actions(c, tk, pad)
		}
	})
}

func narrow2(narrow bool, a, b float32) float32 {
	if narrow {
		return a
	}
	return b
}

func (w *workbench) profileHeader(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, sel.Profile.Name).FontSize(22).FontWeight(600).TextColor(tk.Text).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
				display.Tag(c, statusTitleText(string(sel.Status)), display.TagProps{Color: statusColor(sel.Status), Variant: display.TagFilled, Round: true}).Shrink(0)
			})
			ui.Text(c, profileMetaText(sel.Profile)).FontSize(tk.FontSizeSM).TextColor(tk.TextSecondary).SingleLine().Ellipsis("…")
			if d := profileNetworkDetailText(sel.Profile); d != "" {
				ui.Text(c, d).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).SingleLine().Ellipsis("…")
			}
		})
		w.connectButton(c, sel)
	})
}

func (w *workbench) connectButton(c *ui.Context, sel *selectedProfile) {
	id := sel.Profile.ID
	if sel.Locked {
		label := tr("Disconnect")
		switch sel.Status {
		case application.StatusStarting:
			label = tr("Cancel Connection")
		case application.StatusStopping:
			label = tr("Disconnecting…")
		}
		if action.Button(c.Key("disconnect"), label, action.ButtonProps{Size: w.size()}).Shrink(0).Disabled(sel.Status == application.StatusStopping).Clicked() {
			w.core.Disconnect(id)
		}
	} else if action.Button(c.Key("connect"), tr("Connect"), action.ButtonProps{Variant: action.Primary, Size: w.size()}).Shrink(0).Clicked() {
		d, sel := w.draft, w.frame.Selected
		go func() {
			if w.persist("Connect profile", d, sel) {
				w.report("Connect profile", w.core.Connect(id))
			}
		}()
	}
}

func (w *workbench) activity(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	w.section(c, tk, "seclog", tr("Activity"), tr("Recent events for this profile."), func() { w.activityLog(c, tk, sel) })
}

func (w *workbench) activityLog(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	id := sel.Profile.ID
	if w.logID != "" {
		w.logTail[w.logID] = w.logScroll.MaxY-w.logScroll.Y < 4
		w.logY[w.logID] = w.logScroll.Y
	}
	if w.logID != id {
		w.logID = id
		w.logScroll.Y = w.logY[id]
	}
	if tail, seen := w.logTail[id]; !seen || tail {
		w.logScroll.Y = math.MaxFloat32
	}
	panel := ui.Scroll(c).MaxHeight(280).Background(tk.BgLayout).Radius(tk.BorderRadiusLG).Padding(10).TrackScroll(&w.logScroll)
	panel.Children(func() {
		text := profileLogText(sel.Logs)
		if text == "" {
			ui.Text(c, tr("No activity yet.")).TextColor(tk.TextDescription)
			return
		}
		ui.Text(c, text).Font("monospace").FontSize(tk.FontSizeSM).TextColor(tk.TextSecondary).Selectable()
	})
}

func (w *workbench) addImport(c *ui.Context, key string) {
	sz := w.size()
	if action.Button(c.Key(key+"add"), tr("Add Profile"), action.ButtonProps{Variant: action.Primary, Size: sz}).Clicked() {
		w.add()
	}
	if action.Button(c.Key(key+"import"), tr("Import"), action.ButtonProps{Size: sz}).Clicked() {
		w.importFile()
	}
}

func (w *workbench) usageGuide(c *ui.Context) {
	feedback.Modal(c, &w.helpOpen, feedback.ModalProps{Title: tr("Usage guide"), Width: 720, NoFooter: true}, func() {
		ui.Scroll(c).MaxHeight(440).Children(func() {
			ui.Column(c).Gap(12).Children(func() {
				for _, p := range usageGuideParagraphs {
					ui.Text(c, tr(p)).Selectable()
				}
			})
		})
		ui.Row(c).Justify(ui.End).Margin(16, 0, 0, 0).Children(func() {
			if action.Button(c.Key("helpclose"), tr("Close"), action.ButtonProps{Variant: action.Primary, Size: w.size()}).Clicked() {
				w.helpOpen = false
			}
		})
	})
}
