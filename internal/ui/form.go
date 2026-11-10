//go:build js

package ui

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/choice"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func (w *workbench) label(c *ui.Context, tk *theme.Tokens, text string) {
	ui.Text(c, text).FontSize(tk.FontSizeSM).FontWeight(500).TextColor(tk.TextSecondary)
}

func (w *workbench) form(c *ui.Context, tk *theme.Tokens, locked bool) {
	sz := w.size()
	ui.Column(c).Gap(10).MaxWidth(640).Children(func() {
		w.label(c, tk, tr("Name"))
		field.Input(c.Key("name"), &w.draft.Name, field.InputProps{Size: sz})
		ui.Row(c).Gap(10).Wrap().Children(func() {
			ui.Column(c).Gap(4).Grow(1).Basis(200).Children(func() {
				w.label(c, tk, tr("SOCKS5 host"))
				field.Input(c.Key("host"), &w.draft.Host, field.InputProps{Size: sz}).Disabled(locked)
			})
			ui.Column(c).Gap(4).Width(120).Children(func() {
				w.label(c, tk, tr("SOCKS5 port"))
				field.Input(c.Key("port"), &w.draft.Port, field.InputProps{Size: sz}).Disabled(locked)
			})
		})
		choice.Checkbox(c.Key("auto"), &w.draft.AutoStart, tr("Start automatically"), choice.CheckboxProps{})
		w.label(c, tk, tr("WireGuard configuration"))
		field.TextArea(c.Key("conf"), &w.draft.Config, field.TextAreaProps{MinRows: 6, MaxRows: 14}).Disabled(locked)
	})
}

func (w *workbench) actions(c *ui.Context) {
	sz := w.size()
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if action.Button(c.Key("save"), tr("Save"), action.ButtonProps{Size: sz}).Clicked() {
			w.save()
		}
		if action.Button(c.Key("delete"), tr("Delete"), action.ButtonProps{Size: sz}).Clicked() {
			w.confirmDel = true
		}
		if action.Button(c.Key("export"), tr("Export"), action.ButtonProps{Size: sz}).Clicked() {
			p := w.frame.Selected.Profile
			w.export([]profile.Profile{p}, safeFileName(p.Name)+".json")
		}
	})
	if feedback.Modal(c, &w.confirmDel, feedback.ModalProps{Title: tr("Delete profile"), OkText: tr("Delete"), CancelText: tr("Cancel"), Width: 360}, func() {
		ui.Text(c, tr("Delete {{.Name}}?", map[string]any{"Name": w.frame.Selected.Profile.Name}))
	}) {
		w.confirmDel = false
		w.remove(w.frame.Selected.Profile.ID)
	}
}
