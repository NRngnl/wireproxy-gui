//go:build js

package ui

import (
	"context"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type PreviewOptions struct {
	Dark     bool
	Compact  bool
	RailOpen bool
}

func Preview(core Application, opts PreviewOptions) error {
	look := appearanceLight
	if opts.Dark {
		look = appearanceDark
	}
	w := newWorkbench(core, look, opts.Compact, opts.RailOpen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mygo.App.WhenReady(func() {
		w.window = mygo.NewWindow(mygo.WindowOptions{Title: "wireproxy", Width: 1280, Height: 800, Content: ui.View(w.view)})
		w.window.OnResize(func() { w.window.Update(func() {}) })
		go w.observe(ctx)
	})
	return mygo.App.Run()
}
