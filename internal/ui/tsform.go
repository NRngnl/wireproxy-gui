package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/choice"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/NRngnl/wireproxy-gui/internal/application"
)

func (w *workbench) hint(c *ui.Context, tk *theme.Tokens, text string) {
	ui.Text(c, text).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
}

func (w *workbench) tailscaleForm(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	sz, locked := w.size(), sel.Locked
	exitLocked := locked && sel.Status != application.StatusRunning
	w.section(c, tk, "sects", tr("Tailscale device"), tr("How this app registers itself on your tailnet."), func() {
		w.field(c, tk, tr("Tailscale hostname"), tr("Used when registering this app as a Tailscale device."), func() {
			field.Input(c.Key("tshost"), &w.draft.Hostname, field.InputProps{Size: sz, Placeholder: tr("Defaults to profile name")}).Disabled(locked)
		})
		w.field(c, tk, tr("Control URL"), tr("Only needed for a custom control server."), func() {
			field.Input(c.Key("tsctl"), &w.draft.ControlURL, field.InputProps{Size: sz, Placeholder: tr("Optional; leave empty for Tailscale")}).Disabled(locked)
		})
		w.field(c, tk, tr("Node lifetime"), "", func() {
			choice.Checkbox(c.Key("tseph"), &w.draft.Ephemeral, tr("Register as ephemeral node"), choice.CheckboxProps{}).Disabled(locked)
		})
	})
	w.section(c, tk, "secauth", tr("Authentication"), tr("Sign this device in to your tailnet."), func() {
		w.authRow(c, tk, sz, locked, sel.Profile.TailscaleConfig.Authenticated)
	})
	w.section(c, tk, "secexit", tr("Exit node"), tr("Route traffic through another device on your tailnet."), func() {
		w.exitNodeRow(c, tk, sz, exitLocked)
	})
	w.section(c, tk, "secfwd", tr("Port forwards"), tr("Relays this node's tailnet IP at the listen port to a local network address."), func() {
		w.forwards(c, tk, sz, locked)
	})
}

func (w *workbench) authRow(c *ui.Context, tk *theme.Tokens, sz theme.Size, locked, authed bool) {
	if authed {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			display.Tag(c, tr("Authenticated"), display.TagProps{Color: display.TagSuccess, Round: true, Icon: lucide.ShieldCheck()})
			ui.Box(c).Grow(1)
			if action.Button(c.Key("tslogout"), tr("Logout"), action.ButtonProps{Size: sz}).Disabled(locked).Clicked() {
				w.logout()
			}
		})
		return
	}
	w.field(c, tk, tr("Auth key"), tr("Paste a key, then click Login, or leave empty for browser sign-in."), func() {
		ui.Row(c).Gap(8).Children(func() {
			field.Input(c.Key("tskey"), &w.draft.AuthKey, field.InputProps{Size: sz, Placeholder: tr("Optional; paste auth key, or leave empty for browser sign-in")}).Password().Grow(1).Disabled(locked)
			if action.Button(c.Key("tslogin"), tr("Login"), action.ButtonProps{Variant: action.Primary, Size: sz}).Disabled(locked).Clicked() {
				w.login()
			}
		})
	})
}

func (w *workbench) exitNodeRow(c *ui.Context, tk *theme.Tokens, sz theme.Size, locked bool) {
	w.field(c, tk, tr("Exit node mode"), tr("Automatic exit asks Tailscale to choose an available exit node."), func() {
		choice.Checkbox(c.Key("tsauto"), &w.draft.AutoExit, tr("Use automatic exit node"), choice.CheckboxProps{}).Disabled(locked)
	})
	if !w.draft.AutoExit {
		w.field(c, tk, tr("Exit node"), tr("Refresh to list tailnet devices, or type a node, host, or IP."), func() { w.exitPicker(c, sz, locked) })
	}
	w.field(c, tk, tr("LAN access"), "", func() {
		choice.Checkbox(c.Key("tslan"), &w.draft.AllowLAN, tr("Allow LAN access while using exit node"), choice.CheckboxProps{}).Disabled(locked)
	})
}

func (w *workbench) exitPicker(c *ui.Context, sz theme.Size, locked bool) {
	opts := []field.SelectOption{{Value: "", Label: tr("None")}}
	known := w.draft.ExitNode == ""
	for _, n := range w.exitNodes {
		label := exitNodeOptionLabel(n)
		if label != "" && n.ID != "" {
			opts = append(opts, field.SelectOption{Value: n.ID, Label: label, Disabled: !n.Online})
			known = known || n.ID == w.draft.ExitNode
		}
	}
	if !known {
		opts = append(opts, field.SelectOption{Value: w.draft.ExitNode, Label: w.draft.ExitNode})
	}
	ui.Row(c).Gap(8).Children(func() {
		sel := field.Select(c.Key("tsexit"), &w.draft.ExitNode, field.SelectProps{Options: opts, Size: sz, Disabled: locked})
		sel.Grow(1)
		label := tr("Refresh")
		if w.exitBusy {
			label = tr("Refreshing…")
		}
		if action.Button(c.Key("tsrefresh"), label, action.ButtonProps{Size: sz}).Disabled(locked || w.exitBusy).Clicked() {
			w.refreshExitNodes()
		}
	})
	field.Input(c.Key("tsexitman"), &w.draft.ExitNode, field.InputProps{Size: sz, Placeholder: tr("Refresh to choose a device, or type a node/IP")}).Disabled(locked)
}

func (w *workbench) forwards(c *ui.Context, tk *theme.Tokens, sz theme.Size, locked bool) {
	for i, f := range w.draft.Forwards {
		ui.Row(c.Key("fwd" + strconv.Itoa(i))).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("%s %d → %s", strings.ToUpper(string(f.Protocol)), f.ListenPort, f.TargetAddr)).Grow(1).MinWidth(0).Font("monospace").FontSize(tk.FontSizeSM)
			if action.Button(c.Key("rm"+strconv.Itoa(i)), tr("Remove"), action.ButtonProps{Variant: action.Text, Size: sz}).TextColor(tk.Error).Disabled(locked).Clicked() {
				w.draft.Forwards = append(w.draft.Forwards[:i:i], w.draft.Forwards[i+1:]...)
			}
		})
	}
	ui.Row(c).Gap(8).Wrap().Children(func() {
		action.Segmented(c.Key("fwdproto"), &w.fwdProto, []string{"TCP", "UDP"}, action.SegmentedProps{Size: theme.Small})
		field.Input(c.Key("fwdport"), &w.fwdPort, field.InputProps{Size: sz, Placeholder: tr("Listen port")}).Width(110).Disabled(locked)
		field.Input(c.Key("fwdtarget"), &w.fwdTarget, field.InputProps{Size: sz, Placeholder: tr("Target address, e.g. 192.168.1.10:80")}).Grow(1).Basis(160).Disabled(locked)
		if action.Button(c.Key("fwdadd"), tr("Add"), action.ButtonProps{Size: sz}).Disabled(locked).Clicked() {
			w.addForward()
		}
	})
}
