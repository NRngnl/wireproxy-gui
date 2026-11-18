//go:build js

package ui

import (
	"context"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

type PreviewOptions struct {
	Dark     bool
	Compact  bool
	RailOpen bool
	Help     bool
	Scene    string
}

func Preview(core Application, opts PreviewOptions) error {
	look := appearanceLight
	if opts.Dark {
		look = appearanceDark
	}
	w := newWorkbench(core, look, opts.Compact, opts.RailOpen)
	w.helpOpen = opts.Help
	switch opts.Scene {
	case "form":
		w.lf = localForwardForm{Open: true, Listen: listenLoopback4, Port: "2222", Target: "remote-pc"}
		w.lf.Err = "Target must look like host:port, for example remote-pc:22"
	case "form-exposed":
		w.lf = localForwardForm{Open: true, Listen: listenAll, Port: "5353", Target: "dns-box:53", Proto: 1}
	case "form-custom":
		w.lf = localForwardForm{Open: true, Listen: listenCustom, CustomIP: "192.0.2", Port: "8080", Target: "192.168.1.10:80"}
		w.lf.Err = "Enter a valid IP address, such as 192.0.2.10"
	case "edit":
		w.lf.load(profile.LocalForward{Protocol: profile.PortForwardTCP, ListenAddr: "127.0.0.1:2222", TargetAddr: "remote-pc:22"})
	case "remove":
		key := profile.LocalForwardKey{Protocol: profile.PortForwardTCP, ListenAddr: "127.0.0.1:2222"}
		w.lf.Remove, w.lf.Removing = &key, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mygo.App.WhenReady(func() {
		w.window = mygo.NewWindow(mygo.WindowOptions{Title: "wireproxy", Width: 1280, Height: 800, Content: ui.View(w.view)})
		w.window.OnResize(func() { w.window.Update(func() {}) })
		go w.observe(ctx)
	})
	return mygo.App.Run()
}
