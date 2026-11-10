//go:build js

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

var errPreview = errors.New("preview has no network backend")

const previewConfig = "[Interface]\nAddress = 10.2.0.2/32\nPrivateKey = preview\n\n[Peer]\nPublicKey = preview\nAllowedIPs = 0.0.0.0/0\n"

type fixture struct {
	profiles []profile.Profile
	statuses map[string]application.Status
	logs     map[string][]application.LogEntry
	changes  chan application.Change
}

func newFixture(name string) *fixture {
	f := &fixture{statuses: map[string]application.Status{}, logs: map[string][]application.LogEntry{}, changes: make(chan application.Change, 8)}
	if name == "empty" {
		return f
	}
	home := profile.New("home-vpn", previewConfig, 1080)
	office := profile.New("office", previewConfig, 1081)
	ts := profile.NewTailscale("travel", 1082)
	f.profiles = []profile.Profile{home, office, ts}
	f.statuses[home.ID] = application.StatusRunning
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f.logs[home.ID] = []application.LogEntry{{At: now, Message: "connected on 127.0.0.1:1080"}}
	switch name {
	case "error":
		f.statuses[office.ID] = application.StatusError
		f.logs[office.ID] = []application.LogEntry{{At: now, Message: "dial udp 198.51.100.7:51820: i/o timeout"}}
		f.profiles[0], f.profiles[1] = office, home
	case "logs":
		var entries []application.LogEntry
		for i := range 300 {
			entries = append(entries, application.LogEntry{At: now.Add(time.Duration(i) * time.Second), Message: fmt.Sprintf("handshake %d with peer 203.0.113.%d completed", i, i%250)})
		}
		f.logs[home.ID] = entries
	}
	return f
}

func (f *fixture) Changes() <-chan application.Change { return f.changes }
func (f *fixture) Profiles() []profile.Profile        { return append([]profile.Profile(nil), f.profiles...) }
func (f *fixture) Profile(id string) (profile.Profile, bool) {
	for _, p := range f.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return profile.Profile{}, false
}
func (f *fixture) Status(id string) application.Status {
	if s, ok := f.statuses[id]; ok {
		return s
	}
	return application.StatusStopped
}
func (f *fixture) Logs(id string) []application.LogEntry { return f.logs[id] }
func (f *fixture) RuntimeLocked(id string) bool {
	s := f.Status(id)
	return s == application.StatusRunning || s == application.StatusStarting || s == application.StatusStopping
}
func (*fixture) Add(string) (profile.Profile, error) { return profile.Profile{}, errPreview }
func (*fixture) Save(profile.Profile) (application.SaveResult, error) {
	return application.SaveResult{}, errPreview
}
func (*fixture) Delete(string) error                              { return errPreview }
func (*fixture) Import(string, []byte) ([]profile.Profile, error) { return nil, errPreview }
func (*fixture) Export([]string) ([]byte, error)                  { return nil, errPreview }
func (*fixture) Connect(string) error                             { return errPreview }
func (*fixture) Login(string) error                               { return errPreview }
func (*fixture) ConnectAll() error                                { return errPreview }
func (*fixture) AutoConnect() error                               { return nil }
func (*fixture) Disconnect(string) bool                           { return false }
func (*fixture) DisconnectFromTray(string) bool                   { return false }
func (*fixture) DisconnectAll()                                   {}
func (*fixture) ExitNodes(context.Context, string) ([]connection.ExitNode, error) {
	return nil, errPreview
}
func (*fixture) Logout(context.Context, string) error { return errPreview }
func (*fixture) Shutdown(context.Context) error       { return nil }
