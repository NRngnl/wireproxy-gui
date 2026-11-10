//go:build js

package ui

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
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
		cw, _ := w.window.ContentSize()
		w.width = float32(cw)
	}
	narrow := w.width > 0 && w.width < narrowBelow
	root.Children(func() {
		if !narrow || w.railOpen {
			w.rail(c, tk, narrow)
		}
		if !narrow || !w.railOpen {
			w.detail(c, tk, narrow)
		}
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
	col := ui.Column(c).Padding(10).Gap(4).Background(tk.BgLayout)
	if narrow {
		col.Grow(1).MinWidth(0)
	} else {
		col.Width(264).Shrink(0)
	}
	col.Children(func() {
		ui.Text(c, tr("Profiles")).FontSize(tk.FontSizeLG).FontWeight(600).TextColor(tk.Text).Padding(6, 8)
		ui.Text(c, w.frame.Summary).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).PaddingX(8).Margin(0, 0, 6, 0)
		ui.Scroll(c).Grow(1).Basis(0).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				for _, r := range w.frame.Rows {
					w.railRow(c, tk, r)
				}
			})
		})
		w.railActions(c)
		w.appearanceControls(c)
	})
}

func (w *workbench) railActions(c *ui.Context) {
	sz := w.size()
	ui.Row(c).Gap(6).Wrap().Children(func() {
		if action.Button(c.Key("add"), tr("Add"), action.ButtonProps{Size: sz}).Clicked() {
			w.add()
		}
		if action.Button(c.Key("import"), tr("Import"), action.ButtonProps{Size: sz}).Clicked() {
			w.importFile()
		}
		if action.Button(c.Key("exportall"), tr("Export All"), action.ButtonProps{Size: sz}).Clicked() {
			w.export(w.core.Profiles(), "wireproxy-profiles.json")
		}
		if action.Button(c.Key("connall"), tr("Connect All"), action.ButtonProps{Size: sz}).Clicked() {
			go func() {
				if w.persist("Connect All") {
					w.report("Connect All", w.core.ConnectAll())
				}
			}()
		}
		if action.Button(c.Key("discall"), tr("Disconnect All"), action.ButtonProps{Size: sz}).Clicked() {
			w.core.DisconnectAll()
		}
	})
}

func (w *workbench) railRow(c *ui.Context, tk *theme.Tokens, r profileRow) {
	active := w.frame.Selected != nil && w.frame.Selected.Profile.ID == r.ID
	row := ui.ButtonBase(c.Key(r.ID)).Label(r.Name).Row().MinHeight(48).Padding(6, 10).Gap(10).
		AlignItems(ui.Center).Radius(tk.BorderRadius).Cursor(ui.CursorPointer)
	if active || row.Hovered() {
		row.Background(tk.FillTertiary)
	}
	row.Children(func() {
		dot := tk.TextQuaternary
		switch r.Status {
		case application.StatusRunning:
			dot = tk.Success
		case application.StatusStarting, application.StatusStopping:
			dot = tk.Warning
		case application.StatusError:
			dot = tk.Error
		}
		ui.Box(c).Size(8, 8).Radius(4).Background(dot).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, r.Name).TextColor(tk.Text).FontWeight(500).SingleLine().Ellipsis("…")
			ui.Text(c, r.Summary).TextColor(tk.TextDescription).FontSize(tk.FontSizeSM).SingleLine().Ellipsis("…")
		})
	})
	if row.Clicked() {
		w.railOpen = false
		w.selectProfile(r.ID)
	}
}

func (w *workbench) appearanceControls(c *ui.Context) {
	ui.Column(c).Gap(6).Margin(8, 0, 0, 0).Children(func() {
		action.Segmented(c.Key("look"), &w.look, []string{tr("Auto"), tr("Light"), tr("Dark")}, action.SegmentedProps{Size: theme.Small, Block: true})
		action.Segmented(c.Key("density"), &w.density, []string{tr("Comfortable"), tr("Compact")}, action.SegmentedProps{Size: theme.Small, Block: true})
	})
}

func (w *workbench) detail(c *ui.Context, tk *theme.Tokens, narrow bool) {
	sel := w.frame.Selected
	ui.Column(c).Grow(1).Basis(0).MinWidth(0).Padding(narrow2(narrow, 14, 24)).Gap(14).Background(tk.BgContainer).Children(func() {
		if narrow {
			if action.Button(c.Key("rail"), tr("Profiles"), action.ButtonProps{Size: w.size()}).Clicked() {
				w.railOpen = true
			}
		}
		if w.notice != "" && !w.noticeShut {
			feedback.Alert(c, w.notice, feedback.AlertProps{Type: feedback.Error, Closed: &w.noticeShut})
		}
		if sel == nil {
			display.Empty(c, display.EmptyProps{Icon: lucide.Inbox(), Title: tr("No profiles"),
				Description: tr("Add a WireGuard or Tailscale profile, then choose a SOCKS5 host and port for that profile.")})
			return
		}
		w.syncDraft()
		if w.info != "" && !w.infoShut {
			feedback.Alert(c, w.info, feedback.AlertProps{Type: feedback.Success, Closed: &w.infoShut})
		}
		w.profileHeader(c, tk, sel)
		w.facts(c, tk, sel)
		w.form(c, tk, sel.Locked)
		w.actions(c)
		w.activity(c, tk, sel)
	})
}

func narrow2(narrow bool, a, b float32) float32 {
	if narrow {
		return a
	}
	return b
}

func (w *workbench) profileHeader(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Text(c, sel.Profile.Name).FontSize(22).FontWeight(600).TextColor(tk.Text).SingleLine().Ellipsis("…").MinWidth(0).Shrink(1)
		color := statusColor(sel.Status)
		display.Tag(c, statusTitleText(string(sel.Status)), display.TagProps{Color: color, Round: true})
	})
	ui.Row(c).Gap(8).Wrap().Children(func() {
		id := sel.Profile.ID
		if sel.Locked {
			if action.Button(c.Key("disconnect"), tr("Disconnect"), action.ButtonProps{Size: w.size()}).Clicked() {
				w.core.Disconnect(id)
			}
		} else if action.Button(c.Key("connect"), tr("Connect"), action.ButtonProps{Variant: action.Primary, Size: w.size()}).Clicked() {
			go func() {
				if w.persist("Connect profile") {
					w.report("Connect profile", w.core.Connect(id))
				}
			}()
		}
	})
}

func (w *workbench) facts(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Text(c, profileMetaText(sel.Profile)).TextColor(tk.TextSecondary)
		ui.Text(c, profileNetworkDetailText(sel.Profile)).TextColor(tk.TextDescription)
	})
}

func (w *workbench) activity(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	ui.Text(c, tr("Activity")).FontWeight(600).TextColor(tk.Text)
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
