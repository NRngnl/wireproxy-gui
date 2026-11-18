//go:build js

package main

import (
	"context"
	"slices"

	"github.com/NRngnl/wireproxy-gui/internal/application"
	"github.com/NRngnl/wireproxy-gui/internal/profile"
)

func rule(proto profile.PortForwardProtocol, listen, target string) profile.LocalForward {
	return profile.LocalForward{Protocol: proto, ListenAddr: listen, TargetAddr: target}
}

func (f *fixture) setStates(id string, status map[string]application.LocalForwardStatus, errs map[string]string, rules []profile.LocalForward) {
	for _, r := range rules {
		st := application.LocalForwardState{Key: r.Key(), Status: application.LocalForwardActive}
		if s, ok := status[r.ListenAddr]; ok {
			st.Status = s
		}
		st.Err = errs[r.ListenAddr]
		f.states[id] = append(f.states[id], st)
	}
}

func (f *fixture) localForwardScenario(name string, home, ts *profile.Profile) {
	switch name {
	case "lf-empty":
		f.profiles = []profile.Profile{*home}
	case "lf-ts":
		ts.TailscaleConfig.Hostname, ts.TailscaleConfig.Authenticated = "travel-node", true
		ts.LocalForwards = []profile.LocalForward{
			rule(profile.PortForwardTCP, "127.0.0.1:2222", "remote-pc:22"),
			rule(profile.PortForwardTCP, "[::1]:3306", "10.20.0.5:3306"),
		}
		f.statuses[ts.ID] = application.StatusRunning
		f.setStates(ts.ID, nil, nil, ts.LocalForwards)
		f.profiles = []profile.Profile{*ts}
	case "lf-saved":
		home.LocalForwards = []profile.LocalForward{rule(profile.PortForwardTCP, "127.0.0.1:2222", "remote-pc:22"), rule(profile.PortForwardUDP, "127.0.0.1:5353", "dns-box:53")}
		delete(f.statuses, home.ID)
		f.profiles = []profile.Profile{*home}
	default:
		home.LocalForwards = []profile.LocalForward{
			rule(profile.PortForwardTCP, "127.0.0.1:2222", "remote-pc:22"),
			rule(profile.PortForwardUDP, "127.0.0.1:5353", "dns-box.tail1234.ts.net:53"),
			rule(profile.PortForwardTCP, "127.0.0.1:8080", "192.168.1.10:80"),
			rule(profile.PortForwardTCP, "0.0.0.0:3389", "10.20.0.5:3389"),
		}
		f.setStates(home.ID, map[string]application.LocalForwardStatus{"127.0.0.1:8080": application.LocalForwardError}, map[string]string{"127.0.0.1:8080": application.ErrLocalForwardPortInUse.Error()}, home.LocalForwards)
		f.profiles = []profile.Profile{*home}
	}
}

func (f *fixture) AddLocalForward(_ context.Context, id string, r profile.LocalForward) error {
	return f.mutate(id, func(p *profile.Profile) error {
		if slices.ContainsFunc(p.LocalForwards, func(o profile.LocalForward) bool { return o.Key() == r.Key() }) {
			return errPreviewDuplicate
		}
		p.LocalForwards = append(p.LocalForwards, r)
		f.states[id] = append(f.states[id], application.LocalForwardState{Key: r.Key(), Status: application.LocalForwardActive})
		return nil
	})
}

func (f *fixture) UpdateLocalForward(_ context.Context, id string, old profile.LocalForwardKey, r profile.LocalForward) error {
	return f.mutate(id, func(p *profile.Profile) error {
		for i := range p.LocalForwards {
			if p.LocalForwards[i].Key() == old {
				p.LocalForwards[i] = r
			}
		}
		for i := range f.states[id] {
			if f.states[id][i].Key == old {
				f.states[id][i] = application.LocalForwardState{Key: r.Key(), Status: application.LocalForwardActive}
			}
		}
		return nil
	})
}

func (f *fixture) RemoveLocalForward(_ context.Context, id string, key profile.LocalForwardKey) error {
	return f.mutate(id, func(p *profile.Profile) error {
		p.LocalForwards = slices.DeleteFunc(p.LocalForwards, func(o profile.LocalForward) bool { return o.Key() == key })
		f.states[id] = slices.DeleteFunc(f.states[id], func(s application.LocalForwardState) bool { return s.Key == key })
		return nil
	})
}

func (f *fixture) RetryLocalForward(_ context.Context, id string, key profile.LocalForwardKey) error {
	return f.mutate(id, func(*profile.Profile) error {
		for i := range f.states[id] {
			if f.states[id][i].Key == key {
				f.states[id][i] = application.LocalForwardState{Key: key, Status: application.LocalForwardActive}
			}
		}
		return nil
	})
}

func (f *fixture) mutate(id string, fn func(*profile.Profile) error) error {
	for i := range f.profiles {
		if f.profiles[i].ID == id {
			err := fn(&f.profiles[i])
			f.poke()
			return err
		}
	}
	return errPreview
}

func (f *fixture) LocalForwardStates(id string) []application.LocalForwardState {
	return slices.Clone(f.states[id])
}

func (*fixture) LocalForwardTargets(string) []string {
	return []string{"remote-pc", "dns-box", "nas.tail1234.ts.net", "192.168.1.10"}
}
