//go:build js

package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

type draft struct {
	ID, Name, Host, Port, Config string
	AutoStart                    bool
}

func newDraft(p profile.Profile) draft {
	return draft{ID: p.ID, Name: p.Name, Host: p.SocksHost, Port: strconv.Itoa(p.SocksPort), Config: p.WireGuardConfig, AutoStart: p.AutoStart}
}

func (d draft) apply(p profile.Profile) (profile.Profile, error) {
	port, err := strconv.Atoi(strings.TrimSpace(d.Port))
	if err != nil {
		return p, profile.ErrSocksPortNotNumber
	}
	p.Name, p.SocksHost, p.SocksPort, p.AutoStart = strings.TrimSpace(d.Name), strings.TrimSpace(d.Host), port, d.AutoStart
	p.Kind = profile.BackendWireGuard
	p.WireGuardConfig = strings.TrimSpace(d.Config)
	p.Normalize()
	return p, p.Validate()
}

func (w *workbench) syncDraft() {
	if sel := w.frame.Selected; sel != nil && w.draft.ID != sel.Profile.ID {
		w.draft = newDraft(sel.Profile)
	}
}

func (w *workbench) persist(title string) bool {
	sel := w.frame.Selected
	if sel == nil {
		return false
	}
	p, err := w.draft.apply(sel.Profile)
	if err == nil && !sel.Locked {
		_, err = w.core.Save(p)
	}
	if err != nil {
		w.report(title, err)
		return false
	}
	return true
}

func (w *workbench) save() {
	if w.persist("Save profile") {
		w.say(tr("Saved"))
	}
}

func (w *workbench) say(msg string) {
	w.window.Update(func() { w.info, w.infoShut = msg, false })
	w.selectProfile(w.selectedIDString())
}

func (w *workbench) selectedIDString() string {
	id, _ := w.selectedID.Load().(string)
	return id
}

func (w *workbench) add() {
	p, err := w.core.Add(tr("New profile"))
	if err != nil {
		w.report("Save profile", err)
		return
	}
	w.railOpen = false
	w.selectProfile(p.ID)
}

func (w *workbench) remove(id string) {
	if err := w.core.Delete(id); err != nil {
		w.report("Delete profile", err)
		return
	}
	w.selectProfile("")
}

func (w *workbench) importFile() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: w.window, Title: tr("Import profiles"),
			Filters: []mygo.FileFilter{{Name: tr("WireGuard or JSON profiles"), Extensions: []string{"conf", "json"}}}})
		if err != nil || len(paths) == 0 {
			w.report("Import profiles", err)
			return
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			w.report("Import profiles", err)
			return
		}
		imported, err := w.core.Import(filepath.Base(paths[0]), data)
		if err != nil {
			w.report("Import profiles", err)
			return
		}
		if len(imported) > 0 {
			w.selectedID.Store(imported[0].ID)
		}
		w.say(tr("Imported profiles"))
	}()
}

func (w *workbench) export(profiles []profile.Profile, name string) {
	if len(profiles) == 0 {
		return
	}
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: w.window, Title: tr("Export profiles"), DefaultPath: name,
			Filters: []mygo.FileFilter{{Name: tr("JSON profiles"), Extensions: []string{"json"}}}})
		if err != nil || path == "" {
			w.report("Export profiles", err)
			return
		}
		ids := make([]string, len(profiles))
		for i, p := range profiles {
			ids[i] = p.ID
		}
		data, err := w.core.Export(ids)
		if err == nil {
			err = exportProfilesToPath(data, path)
		}
		if err != nil {
			w.report("Export profiles", err)
			return
		}
		w.say(tr("Exported"))
	}()
}
