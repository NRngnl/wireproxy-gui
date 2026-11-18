package ui

import (
	"context"
	"slices"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func (w *workbench) ctx() context.Context { return context.Background() }

func (w *workbench) forwardCall(fn func() error) {
	go func() { w.report("Local forward", fn()) }()
}

func (w *workbench) submitForward(profileID string) {
	rule, msg := w.lf.rule()
	if msg != "" {
		w.lf.Err = msg
		return
	}
	editing := w.lf.Editing
	if slices.ContainsFunc(w.frame.Selected.Profile.LocalForwards, func(o profile.LocalForward) bool {
		return o.Key() == rule.Key() && (editing == nil || *editing != rule.Key())
	}) {
		w.lf.Err = "A forward with this protocol and listen address already exists"
		return
	}
	w.lf.Err, w.lf.Busy = "", true
	go func() {
		var err error
		if editing != nil {
			err = w.core.UpdateLocalForward(w.ctx(), profileID, *editing, rule)
		} else {
			err = w.core.AddLocalForward(w.ctx(), profileID, rule)
		}
		w.window.Update(func() {
			w.lf.Busy = false
			if err != nil {
				w.lf.Err = displayErrorText("Local forward", err)
				return
			}
			w.lf = localForwardForm{}
		})
	}()
}
