//go:build !js

package ui

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/NRngnl/wireproxy-gui/internal/buildinfo"
	"github.com/NRngnl/wireproxy-gui/internal/svcinstall"
)

const shutdownWaitTimeout = 5 * time.Second

//go:embed appicon.png
var appIconPNG []byte

//go:embed trayicon.png
var macTrayIconPNG []byte

type host struct {
	w      *workbench
	ctx    context.Context
	svc    svcinstall.Installer
	tray   *mygo.Tray
	prefs  prefs
	busy   bool
	svcOn  bool
	quitOK bool
}

// Run opens the native workbench window and tray, and blocks until the app quits.
func Run(core Application, loadErr error) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &host{w: newWorkbench(core, appearanceAuto, false, true), ctx: ctx, svc: svcinstall.New(), prefs: loadPrefs()}
	if loadErr != nil {
		d.w.notice = displayErrorText("Load profiles", loadErr)
	}
	_ = core.AutoConnect()
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		if d.quitOK {
			return
		}
		d.quitOK = true
		cancel()
		sctx, stop := context.WithTimeout(context.Background(), shutdownWaitTimeout)
		defer stop()
		err := core.Shutdown(sctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, tr("Error stopping profiles during shutdown")+":", err)
		}
	})
	mygo.App.WhenReady(func() { d.open(ctx) })
	err := mygo.App.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wireproxy-gui:", err)
	}
	cancel()
}

func (d *host) open(ctx context.Context) {
	w := d.w
	w.window = mygo.NewWindow(mygo.WindowOptions{Title: tr(buildinfo.WindowTitle()), Width: 1180, Height: 760, Content: ui.View(w.view)})
	w.window.OnResize(func() { w.window.Update(func() {}) })
	w.onChange = d.refreshTray
	d.installTray()
	if d.tray != nil {
		mygo.App.OnWindowAllClosed(func() {})
		w.window.OnClose(func(e *mygo.CloseEvent) {
			if !d.quitOK {
				e.PreventDefault()
				w.window.Hide()
			}
		})
		if d.prefs.HideDock {
			mygo.App.Dock.Hide()
		}
	}
	w.window.Show()
	go w.observe(ctx)
	// Release smoke test: start the real GUI, then quit once the window is up.
	if os.Getenv("WIREPROXY_GUI_SMOKE_TEST") == "1" {
		time.AfterFunc(2*time.Second, mygo.App.Quit)
	}
}

func (d *host) message(kind mygo.MessageType, title, msg string, buttons ...string) int {
	r, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: d.w.window, Type: kind, Title: title, Message: msg, Buttons: buttons})
	if err != nil {
		return -1
	}
	return r.Button
}

func (d *host) confirm(title, msg, ok string) bool {
	return d.message(mygo.MessageQuestion, title, msg, tr("Cancel"), ok) == 1
}
