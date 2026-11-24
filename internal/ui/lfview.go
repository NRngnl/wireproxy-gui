package ui

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/overlay"
	"github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func (w *workbench) localForwards(c *ui.Context, tk *theme.Tokens, sel *selectedProfile) {
	rules, sz := sel.Profile.LocalForwards, w.size()
	w.section(c, tk, "seclf", tr("Local forwards"), tr("Open a port on this computer that reaches a device or service through this profile."), func() {
		if len(rules) == 0 && !w.lf.Open {
			w.localForwardsEmpty(c, tk)
		}
		if len(rules) > 0 {
			ui.Column(c).Radius(tk.BorderRadius).Border(1, tk.BorderSecondary).Children(func() {
				for i, r := range rules {
					w.localForwardRow(c, tk, sel, i, r)
				}
			})
		}
		if w.lf.Open {
			w.localForwardForm(c, tk, sel)
		} else if action.Button(c.Key("lfadd"), tr("Add local forward"), action.ButtonProps{Size: sz}).AlignSelf(ui.Start).Clicked() {
			w.setForwardForm(localForwardForm{Open: true, Listen: listenLoopback4})
		}
	})
	w.localForwardRemoveModal(c, sel)
}

func (w *workbench) localForwardsEmpty(c *ui.Context, tk *theme.Tokens) {
	ui.Column(c).Gap(6).Padding(12).Radius(tk.BorderRadius).Background(tk.FillQuaternary).Children(func() {
		ui.Text(c, tr("No local forwards yet")).FontWeight(600).TextColor(tk.Text)
		ui.Text(c, tr("Like ssh -L: TCP 127.0.0.1:2222 to remote-pc:22 lets ssh -p 2222 localhost reach remote-pc through this profile.")).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
	})
}

func (w *workbench) forwardState(sel *selectedProfile, r profile.LocalForward) application.LocalForwardState {
	if !sel.Locked || sel.Status != application.StatusRunning {
		return application.LocalForwardState{Key: r.Key(), Status: application.LocalForwardSaved}
	}
	for _, s := range sel.Forwards {
		if s.Key == r.Key() {
			return s
		}
	}
	return application.LocalForwardState{Key: r.Key(), Status: application.LocalForwardSaved}
}

func statePill(st application.LocalForwardState) (string, display.TagColor) {
	switch st.Status {
	case application.LocalForwardActive:
		return tr("Active"), display.TagSuccess
	case application.LocalForwardError:
		return tr("Error"), display.TagError
	}
	return tr("Saved, not connected"), display.TagGray
}

func (w *workbench) localForwardRow(c *ui.Context, tk *theme.Tokens, sel *selectedProfile, i int, r profile.LocalForward) {
	st, id := w.forwardState(sel, r), strconv.Itoa(i)
	wide := w.width >= narrowBelow
	ui.Column(c.Key("lf"+id)).Gap(6).Padding(10, 12).BorderWidth(btoi(i > 0), 0, 0, 0).BorderColor(tk.BorderSecondary).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			display.Tag(c, strings.ToUpper(string(r.Protocol)), display.TagProps{Color: display.TagInfo, Size: theme.Small}).Shrink(0)
			if wide {
				w.ruleText(c, tk, r.ListenAddr+" → "+r.TargetAddr).Shrink(1)
				w.localForwardStatus(c, r, st)
				ui.Box(c).Grow(1)
				w.localForwardActions(c, sel, id, r, st)
			} else {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					w.ruleText(c, tk, r.ListenAddr)
					w.ruleText(c, tk, "→ "+r.TargetAddr)
				})
			}
		})
		if !wide {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				w.localForwardStatus(c, r, st)
				ui.Box(c).Grow(1)
				w.localForwardActions(c, sel, id, r, st)
			})
		}
		if st.Err != "" {
			ui.Text(c, forwardErrorText(st.Err, r)).FontSize(tk.FontSizeSM).TextColor(tk.Error)
		}
	})
}

func (w *workbench) ruleText(c *ui.Context, tk *theme.Tokens, text string) ui.Element {
	t := ui.Text(c, text).MinWidth(0).Font("monospace").FontSize(tk.FontSizeSM).TextColor(tk.Text).SingleLine().Ellipsis("…")
	overlay.Tooltip(c, t, text, overlay.TooltipProps{})
	return t
}

func forwardErrorText(msg string, r profile.LocalForward) string {
	if msg == application.ErrLocalForwardPortInUse.Error() {
		_, port, _ := net.SplitHostPort(r.ListenAddr)
		return tr("Port {{.Port}} is already used by another app. Pick another port or close that app, then Retry.", map[string]any{"Port": port})
	}
	return tr(msg)
}

func (w *workbench) localForwardStatus(c *ui.Context, r profile.LocalForward, st application.LocalForwardState) {
	label, color := statePill(st)
	ui.Row(c).Gap(6).Shrink(0).Children(func() {
		display.Tag(c, label, display.TagProps{Color: color, Size: theme.Small})
		peerPathTag(c, st.Path)
		if !loopbackListen(r.ListenAddr) {
			display.Tag(c, tr("Exposed"), display.TagProps{Color: display.TagWarning, Size: theme.Small})
		}
	})
}

func peerPathTag(c *ui.Context, path application.PeerPath) {
	switch path.Kind {
	case application.PeerPathDirect:
		tag := display.Tag(c, tr("Direct"), display.TagProps{Color: display.TagGray, Size: theme.Small})
		overlay.Tooltip(c, tag, tr("Traffic goes straight to this device."), overlay.TooltipProps{})
	case application.PeerPathPeerRelay:
		tip := tr("Traffic goes through your tailnet's peer relay.")
		if path.Relay != "" {
			tip = tr("Traffic goes through your tailnet's peer relay at {{.Addr}}.", map[string]any{"Addr": path.Relay})
		}
		tag := display.Tag(c, tr("Peer relay"), display.TagProps{Color: display.TagGray, Size: theme.Small})
		overlay.Tooltip(c, tag, tip, overlay.TooltipProps{})
	case application.PeerPathRelay:
		label := tr("Relay")
		if path.Relay != "" {
			label = tr("Relay · {{.Region}}", map[string]any{"Region": path.Relay})
		}
		tag := display.Tag(c, label, display.TagProps{Color: display.TagWarning, Size: theme.Small})
		overlay.Tooltip(c, tag, tr("Traffic goes through a relay server, which adds latency. It switches to a direct path when one opens."), overlay.TooltipProps{})
	}
}

func loopbackListen(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	return err == nil && ap.Addr().IsLoopback()
}

func btoi(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

func (w *workbench) localForwardActions(c *ui.Context, sel *selectedProfile, id string, r profile.LocalForward, st application.LocalForwardState) {
	if st.Status == application.LocalForwardError {
		if action.Button(c.Key("lfretry"+id), tr("Retry"), action.ButtonProps{Size: theme.Small}).Clicked() {
			w.forwardCall(func() error { return w.core.RetryLocalForward(w.ctx(), sel.Profile.ID, r.Key()) })
		}
	}
	if action.ActionIcon(c.Key("lfcopy"+id), lucide.Copy(), action.ActionIconProps{Size: theme.Small, Title: tr("Copy listen address")}).Clicked() {
		mygo.Clipboard.WriteText(r.ListenAddr)
		w.info, w.infoShut = tr("Copied {{.Address}}", map[string]any{"Address": r.ListenAddr}), false
	}
	if action.ActionIcon(c.Key("lfedit"+id), lucide.Pencil(), action.ActionIconProps{Size: theme.Small, Title: tr("Edit")}).Clicked() {
		next := localForwardForm{}
		next.load(r)
		w.setForwardForm(next)
	}
	if action.ActionIcon(c.Key("lfrm"+id), lucide.Trash2(), action.ActionIconProps{Size: theme.Small, Danger: true, Title: tr("Remove")}).Clicked() {
		key := r.Key()
		if st.Status == application.LocalForwardActive {
			w.lf.Remove, w.lf.Removing = &key, true
		} else {
			w.forwardCall(func() error { return w.core.RemoveLocalForward(w.ctx(), sel.Profile.ID, key) })
		}
	}
}

func (w *workbench) localForwardRemoveModal(c *ui.Context, sel *selectedProfile) {
	if feedback.Modal(c, &w.lf.Removing, feedback.ModalProps{Title: tr("Remove local forward"), OkText: tr("Remove"), CancelText: tr("Cancel"), Width: 380}, func() {
		key := *w.lf.Remove
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, strings.ToUpper(string(key.Protocol))+" "+key.ListenAddr).Font("monospace")
			ui.Text(c, tr("Closes connections using this port."))
		})
	}) {
		key := *w.lf.Remove
		w.lf.Removing = false
		w.forwardCall(func() error { return w.core.RemoveLocalForward(w.ctx(), sel.Profile.ID, key) })
	}
}
