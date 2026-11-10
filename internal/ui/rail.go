package ui

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/overlay"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/NRngnl/wireproxy-gui/internal/application"
)

func (w *workbench) railBody(c *ui.Context, tk *theme.Tokens) {
	ui.Row(c).AlignItems(ui.Center).Padding(4, 4, 0, 8).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, tr("Profiles")).FontSize(tk.FontSizeLG).FontWeight(600).TextColor(tk.Text)
			ui.Text(c, w.frame.Summary).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).SingleLine().Ellipsis("…")
		})
		w.railMenu(c)
	})
	ui.Box(c).Height(8)
	ui.Scroll(c).Grow(1).Basis(0).Children(func() {
		ui.Column(c).Gap(2).Children(func() {
			for _, r := range w.frame.Rows {
				w.railRow(c, tk, r)
			}
		})
	})
	ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(10, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(tk.BorderSecondary).Children(func() {
		sz := w.size()
		if action.Button(c.Key("add"), tr("Add Profile"), action.ButtonProps{Variant: action.Primary, Size: sz}).Grow(1).Clicked() {
			w.add()
		}
		if action.ActionIcon(c.Key("import"), lucide.FileDown(), action.ActionIconProps{Variant: action.ActionIconOutlined, Size: sz, Title: tr("Import")}).Clicked() {
			w.importFile()
		}
		w.lookPopover(c, tk, sz)
	})
}

func (w *workbench) railMenu(c *ui.Context) {
	items := []overlay.MenuItem{
		{Key: "connall", Label: tr("Connect All"), Icon: lucide.Plug()},
		{Key: "discall", Label: tr("Disconnect All"), Icon: lucide.Unplug()},
		{Key: "exportall", Label: tr("Export All"), Icon: lucide.FileUp()},
		{Divider: true},
		{Key: "help", Label: tr("Help"), Icon: lucide.CircleHelp()},
	}
	switch overlay.DropdownMenu(c.Key("more"), &w.moreOpen, overlay.DropdownMenuProps{Items: items}, func(t ui.Element) {
		action.StyleActionIcon(c, t, lucide.Ellipsis(), action.ActionIconProps{Active: w.moreOpen, Title: tr("All Profiles")}).Label(tr("All Profiles"))
	}) {
	case "connall":
		d, sel := w.draft, w.frame.Selected
		go func() {
			if w.persist("Connect All", d, sel) {
				w.report("Connect All", w.core.ConnectAll())
			}
		}()
	case "discall":
		w.core.DisconnectAll()
	case "exportall":
		w.export(w.core.Profiles(), "wireproxy-profiles.json")
	case "help":
		w.helpOpen = true
	}
}

func (w *workbench) lookPopover(c *ui.Context, tk *theme.Tokens, sz theme.Size) {
	anchor := action.ActionIcon(c.Key("looks"), lucide.Settings2(), action.ActionIconProps{Variant: action.ActionIconOutlined, Size: sz, Active: w.lookOpen, Title: tr("Appearance")})
	overlay.Popover(c, anchor, &w.lookOpen, overlay.PopoverProps{Placement: overlay.Top}, func() {
		ui.Column(c).Gap(8).Width(220).Children(func() {
			ui.Text(c, tr("Theme")).FontSize(tk.FontSizeSM).TextColor(tk.TextSecondary)
			action.Segmented(c.Key("look"), &w.look, []string{tr("Auto"), tr("Light"), tr("Dark")}, action.SegmentedProps{Size: theme.Small, Block: true})
			ui.Text(c, tr("Density")).FontSize(tk.FontSizeSM).TextColor(tk.TextSecondary)
			action.Segmented(c.Key("density"), &w.density, []string{tr("Comfortable"), tr("Compact")}, action.SegmentedProps{Size: theme.Small, Block: true})
		})
	})
}

func (w *workbench) railRow(c *ui.Context, tk *theme.Tokens, r profileRow) {
	active := w.frame.Selected != nil && w.frame.Selected.Profile.ID == r.ID
	row := ui.ButtonBase(c.Key(r.ID)).Label(r.Name).Row().MinHeight(52).Padding(8, 10).Gap(10).
		AlignItems(ui.Center).Radius(tk.BorderRadius).Cursor(ui.CursorPointer)
	switch {
	case active:
		row.Background(tk.BgContainer).Border(1, tk.BorderSecondary)
	case row.Hovered():
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
		ui.Box(c).Size(10, 10).Radius(5).Background(dot).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			name := ui.Text(c, r.Name).TextColor(tk.Text).FontWeight(500).SingleLine().Ellipsis("…")
			if active {
				name.FontWeight(600)
			}
			ui.Text(c, r.Summary).TextColor(tk.TextDescription).FontSize(tk.FontSizeSM).SingleLine().Ellipsis("…")
		})
	})
	if row.Clicked() {
		w.railOpen = false
		w.selectProfile(r.ID)
	}
}
