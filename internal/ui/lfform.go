package ui

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/theme"
)

func (w *workbench) localForwardForm(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	sz, f := w.size(), &w.lf
	if !f.Fetched {
		f.Fetched = true
		id := sel.Profile.ID
		go func() {
			names := w.core.LocalForwardTargets(id)
			w.window.Update(func() { w.lf.Targets = names })
		}()
	}
	box := ui.Column(c).Gap(12).Padding(12).Radius(tk.BorderRadius).Border(1, tk.BorderSecondary)
	if box.Shortcut(0, ui.KeyEscape) {
		w.lf = localForwardForm{}
		return
	}
	box.Children(func() {
		w.field(c, tk, tr("Protocol"), "", func() {
			action.Segmented(c.Key("lfproto"), &f.Proto, []string{"TCP", "UDP"}, action.SegmentedProps{Size: theme.Small}).AlignSelf(ui.Start)
		})
		w.field(c, tk, tr("Listen on"), "", func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.Column(c).Grow(1).Basis(220).Children(func() {
					field.Select(c.Key("lflisten"), &f.Listen, field.SelectProps{Size: sz, Options: listenOptions()})
				})
				ui.Column(c).Width(110).Children(func() {
					field.Input(c.Key("lfport"), &f.Port, field.InputProps{Size: sz, Placeholder: tr("Port")})
				})
			})
			if f.Listen == listenCustom {
				field.Input(c.Key("lfcustom"), &f.CustomIP, field.InputProps{Size: sz, Placeholder: tr("Local IP address, e.g. 192.0.2.10")})
			}
		})
		if f.exposed() {
			feedback.Alert(c, tr("Anyone who can reach this computer can use this forward."), feedback.AlertProps{Type: feedback.Warning})
		}
		w.field(c, tk, tr("Target"), tr("A device name or IP on the network behind this profile, then the port."), func() {
			field.AutoComplete(c.Key("lftarget"), &f.Target, field.AutoCompleteProps{Size: sz, Placeholder: tr("host:port, e.g. remote-pc:22"), Options: targetOptions(f.Targets)})
		})
		if f.Err != "" {
			ui.Text(c, tr(f.Err)).FontSize(tk.FontSizeSM).TextColor(tk.Error)
		}
		ui.Row(c).Gap(8).Children(func() {
			label := tr("Add forward")
			if f.Editing != nil {
				label = tr("Save forward")
			}
			if action.Button(c.Key("lfsave"), label, action.ButtonProps{Variant: action.Primary, Size: sz}).Disabled(f.Busy).Clicked() {
				w.submitForward(sel.Profile.ID)
			}
			if action.Button(c.Key("lfcancel"), tr("Cancel"), action.ButtonProps{Size: sz}).Clicked() {
				w.lf = localForwardForm{}
			}
		})
	})
}

func listenOptions() []field.SelectOption {
	return []field.SelectOption{
		{Value: listenLoopback4, Label: tr("This computer only (127.0.0.1)")},
		{Value: listenLoopback6, Label: tr("This computer only (::1)")},
		{Value: listenAll, Label: tr("All interfaces (0.0.0.0)")},
		{Value: listenCustom, Label: tr("A specific IP address")},
	}
}

func targetOptions(names []string) []field.AutoCompleteOption {
	opts := make([]field.AutoCompleteOption, len(names))
	for i, n := range names {
		opts[i] = field.AutoCompleteOption{Value: n, Label: n}
	}
	return opts
}
