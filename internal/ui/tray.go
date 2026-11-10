//go:build !js

package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/egoist/mygo"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/NRngnl/wireproxy-gui/internal/svcinstall"
)

func (d *host) installTray() {
	icon, template := appIconPNG, false
	if runtime.GOOS == "darwin" {
		icon, template = macTrayIconPNG, true
	}
	tray, err := mygo.NewTray(mygo.TrayOptions{ID: "wireproxy-gui", Icon: icon, IconIsTemplate: template, ToolTip: tr("Wireproxy GUI"), Menu: d.trayMenu()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "wireproxy-gui: tray unavailable:", err)
		return
	}
	d.tray = tray
	go d.refreshService()
}

func (d *host) refreshTray() {
	if d.tray != nil {
		d.tray.SetMenu(d.trayMenu())
	}
}

func (d *host) refreshService() {
	if d.svc == nil || !d.svc.Supported() {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	status, err := d.svc.Status(ctx)
	cancel()
	mygo.RunOnMain(func() {
		d.svcOn = err == nil && status.Installed
		d.refreshTray()
	})
}

func item(label string, click func()) *mygo.MenuItem {
	return &mygo.MenuItem{Label: label, Click: func(*mygo.MenuItem, *mygo.Window) { go click() }}
}

func disabled(label string) *mygo.MenuItem { return &mygo.MenuItem{Label: label, Disabled: true} }

var separator = &mygo.MenuItem{Type: mygo.MenuItemSeparator}

func (d *host) trayMenu() *mygo.Menu {
	core := d.w.core
	items := []*mygo.MenuItem{item(tr("Show"), func() { d.w.window.Show(); d.w.window.Focus() })}
	if profiles := core.Profiles(); len(profiles) > 0 {
		items = append(items, separator)
		for _, p := range profiles {
			items = append(items, d.profileItem(p))
		}
	}
	items = append(items, separator,
		item(tr("Connect All"), func() { d.w.report("Connect All", core.ConnectAll()) }),
		item(tr("Disconnect All"), core.DisconnectAll),
		separator, d.serviceItem())
	if runtime.GOOS == "darwin" {
		items = append(items, &mygo.MenuItem{Label: tr("Hide Dock Icon"), Type: mygo.MenuItemCheckbox, Checked: d.prefs.HideDock,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) { d.setHideDock(it.Checked) }})
	}
	return mygo.NewMenu(append(items, separator, &mygo.MenuItem{Label: tr("Quit"), Role: mygo.RoleQuit}))
}

func (d *host) setHideDock(hide bool) {
	d.prefs.HideDock = hide
	if hide {
		mygo.App.Dock.Hide()
	} else {
		mygo.App.Dock.Show()
	}
	d.w.report("Hide Dock Icon", d.prefs.save())
}

func (d *host) profileItem(p profile.Profile) *mygo.MenuItem {
	core := d.w.core
	status := string(core.Status(p.ID))
	active := trayStatusActive(status)
	connect := item(tr("Connect"), func() { d.w.report("Connect profile", core.Connect(p.ID)) })
	disconnect := item(tr("Disconnect"), func() { core.DisconnectFromTray(p.ID) })
	connect.Disabled, disconnect.Disabled = active, !active
	return &mygo.MenuItem{Label: p.Name, Subtitle: trayStatusText(status), Submenu: []*mygo.MenuItem{
		disabled(tr("Status: {{.Status}}", map[string]any{"Status": trayStatusText(status)})),
		disabled(tr("SOCKS5 bind: {{.Address}}", map[string]any{"Address": p.BindAddress()})),
		disabled(profileNetworkDetailText(p)),
		separator, connect, disconnect,
	}}
}

// serviceItem toggles the per-user login-time wireproxy-daemon service; it never needs elevation.
func (d *host) serviceItem() *mygo.MenuItem {
	switch {
	case d.svc == nil || !d.svc.Supported():
		return disabled(tr("Start on Login (not yet supported on this OS)"))
	case d.busy:
		return disabled(tr("Start on Login (updating…)"))
	case d.svcOn:
		return item(tr("Disable Start on Login"), func() {
			if d.confirm(tr("Start on Login"), tr("Disable and remove the per-user login service for wireproxy-daemon?"), tr("Disable")) {
				_ = d.runService("Disable start on login", tr("The per-user login service has been removed."), d.svc.Uninstall)
			}
		})
	}
	return item(tr("Enable Start on Login"), func() { d.install(svcinstall.Options{}) })
}

func (d *host) install(opts svcinstall.Options) {
	err := d.runService("Enable start on login", tr("wireproxy-daemon is now installed as a per-user login service."),
		func(ctx context.Context) error { return d.svc.Install(ctx, opts) })
	if errors.Is(err, svcinstall.ErrLockHeld) && !opts.Force &&
		d.confirm(tr("Start on Login"), tr("{{.Error}}\n\nInstall anyway?", map[string]any{"Error": localizedErrorText(err)}), tr("Install")) {
		d.install(svcinstall.Options{Force: true})
	}
}

// runService runs one service action off the main thread and reports its outcome.
func (d *host) runService(title, done string, action func(context.Context) error) error {
	setBusy := func(b bool) { mygo.RunOnMain(func() { d.busy = b; d.refreshTray() }) }
	setBusy(true)
	err := action(d.ctx)
	setBusy(false)
	d.refreshService()
	switch {
	case errors.Is(err, svcinstall.ErrLockHeld):
	case err != nil:
		d.message(mygo.MessageError, tr(title), localizedErrorText(err))
	default:
		d.message(mygo.MessageInfo, tr("Start on Login"), done)
	}
	return err
}
