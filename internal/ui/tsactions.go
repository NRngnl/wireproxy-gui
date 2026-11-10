//go:build js

package ui

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func (w *workbench) login() {
	id, d, sel := w.draft.ID, w.draft, w.frame.Selected
	go func() {
		if w.persist("Login to Tailscale", d, sel) {
			w.report("Login to Tailscale", w.core.Login(id))
		}
	}()
}

func (w *workbench) logout() {
	id := w.draft.ID
	go func() {
		err := w.core.Logout(context.Background(), id)
		w.report("Logout from Tailscale", err)
		w.selectProfile(id)
	}()
}

func (w *workbench) refreshExitNodes() {
	if w.exitBusy {
		return
	}
	w.exitBusy = true
	w.exitSeq++
	id, seq := w.draft.ID, w.exitSeq
	go func() {
		nodes, err := w.core.ExitNodes(context.Background(), id)
		w.window.Update(func() { w.applyExitNodes(id, seq, nodes, err) })
	}()
}

func (w *workbench) applyExitNodes(id string, seq int, nodes []connection.ExitNode, err error) {
	if w.draft.ID != id || w.exitSeq != seq {
		return
	}
	w.exitBusy = false
	if err != nil {
		w.notice, w.noticeShut = displayErrorText("Refresh exit nodes", err), false
		return
	}
	w.exitNodes = nodes
}

func (w *workbench) addForward() {
	port, err := strconv.Atoi(strings.TrimSpace(w.fwdPort))
	if err != nil {
		err = profile.ErrPortForwardPortOutOfRange
	}
	proto := profile.PortForwardTCP
	if w.fwdProto == 1 {
		proto = profile.PortForwardUDP
	}
	next := append(slices.Clone(w.draft.Forwards), profile.PortForward{ListenPort: port, Protocol: proto, TargetAddr: w.fwdTarget})
	if err == nil {
		cfg := profile.TailscaleConfig{PortForwards: next}
		cfg.Normalize()
		err = cfg.Validate()
		next = cfg.PortForwards
	}
	if err != nil {
		w.report("Add port forward", err)
		return
	}
	w.draft.Forwards, w.fwdPort, w.fwdTarget = next, "", ""
}
