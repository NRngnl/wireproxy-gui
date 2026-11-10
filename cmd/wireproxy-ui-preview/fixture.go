//go:build js

package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	case "ts", "ts-authed", "ts-running":
		ts.TailscaleConfig.Hostname = "travel-node"
		ts.TailscaleConfig.PortForwards = []profile.PortForward{{ListenPort: 8080, Protocol: profile.PortForwardTCP, TargetAddr: "192.168.1.10:80"}}
		if name != "ts" {
			ts.TailscaleConfig.Authenticated = true
			ts.TailscaleConfig.ExitNode = "exit-1"
		}
		if name == "ts-running" {
			f.statuses[ts.ID] = application.StatusRunning
		}
		f.profiles = []profile.Profile{ts, home, office}
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

func (f *fixture) poke() {
	select {
	case f.changes <- application.Change{}:
	default:
	}
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
func (f *fixture) Add(name string) (profile.Profile, error) {
	p := profile.New(name, previewConfig, 1090+len(f.profiles))
	f.profiles = append(f.profiles, p)
	f.poke()
	return p, nil
}
func (f *fixture) Save(p profile.Profile) (application.SaveResult, error) {
	for i := range f.profiles {
		if f.profiles[i].ID == p.ID {
			f.profiles[i] = p
		}
	}
	f.poke()
	return application.SaveResult{Changed: true}, nil
}
func (f *fixture) Delete(id string) error {
	f.profiles = slices.DeleteFunc(f.profiles, func(p profile.Profile) bool { return p.ID == id })
	f.poke()
	return nil
}
func (*fixture) Import(string, []byte) ([]profile.Profile, error) { return nil, errPreview }
func (*fixture) Export([]string) ([]byte, error)                  { return nil, errPreview }
func (f *fixture) Connect(id string) error {
	f.statuses[id] = application.StatusRunning
	f.poke()
	return nil
}
func (f *fixture) ConnectAll() error {
	for _, p := range f.profiles {
		f.statuses[p.ID] = application.StatusRunning
	}
	f.poke()
	return nil
}
func (f *fixture) Login(id string) error { return f.setAuth(id, true) }
func (f *fixture) setAuth(id string, on bool) error {
	for i := range f.profiles {
		if f.profiles[i].ID == id {
			f.profiles[i].TailscaleConfig.Authenticated = on
			f.profiles[i].TailscaleConfig.AuthKey = ""
		}
	}
	f.poke()
	return nil
}
func (*fixture) AutoConnect() error { return nil }
func (f *fixture) Disconnect(id string) bool {
	delete(f.statuses, id)
	f.poke()
	return true
}
func (*fixture) DisconnectFromTray(string) bool { return false }
func (f *fixture) DisconnectAll() {
	clear(f.statuses)
	f.poke()
}
func (*fixture) ExitNodes(context.Context, string) ([]connection.ExitNode, error) {
	return []connection.ExitNode{{ID: "exit-1", Name: "frankfurt-exit", Online: true}, {ID: "exit-2", Name: "tokyo-exit", Online: false}}, nil
}
func (f *fixture) Logout(_ context.Context, id string) error { return f.setAuth(id, false) }
func (*fixture) Shutdown(context.Context) error              { return nil }
