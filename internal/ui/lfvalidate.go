package ui

import (
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/NRngnl/wireproxy-gui/internal/profile"
	"github.com/lobehub/lobe-mygo/pkg/field"
)

const (
	listenLoopback4 = "127.0.0.1"
	listenLoopback6 = "::1"
	listenAll       = "0.0.0.0"
	listenCustom    = "custom"
)

type localForwardForm struct {
	Open     bool
	Editing  *profile.LocalForwardKey
	Proto    int
	Listen   string
	CustomIP string
	Port     string
	Target   string
	TPort    string
	Err      string
	Remove   *profile.LocalForwardKey
	Removing bool
	Busy     bool
	Targets  []field.AutoCompleteOption
	Listens  []field.SelectOption
	Fetched  bool
}

func (f *localForwardForm) listenIP() string {
	if f.Listen == listenCustom {
		return strings.TrimSpace(f.CustomIP)
	}
	if f.Listen == "" {
		return listenLoopback4
	}
	return f.Listen
}

func (f *localForwardForm) exposed() bool {
	if f.Listen != listenCustom {
		return f.Listen == listenAll
	}
	addr, err := netip.ParseAddr(f.listenIP())
	return err == nil && !addr.IsLoopback()
}

func (f *localForwardForm) rule() (profile.LocalForward, string) {
	ip, err := netip.ParseAddr(f.listenIP())
	if err != nil || ip.Zone() != "" {
		return profile.LocalForward{}, "Enter a valid IP address, such as 192.0.2.10"
	}
	port, err := strconv.Atoi(strings.TrimSpace(f.Port))
	if err != nil || port < 1 || port > 65535 {
		return profile.LocalForward{}, "Port must be a number from 1 to 65535"
	}
	host := strings.Trim(strings.TrimSpace(f.Target), "[]")
	if host == "" || strings.ContainsAny(host, " /") {
		return profile.LocalForward{}, "Enter a device name or IP, for example remote-pc"
	}
	n, err := strconv.Atoi(strings.TrimSpace(f.TPort))
	if err != nil || n < 1 || n > 65535 {
		return profile.LocalForward{}, "Target port must be a number from 1 to 65535"
	}
	proto := profile.PortForwardTCP
	if f.Proto == 1 {
		proto = profile.PortForwardUDP
	}
	rule := profile.LocalForward{Protocol: proto, ListenAddr: net.JoinHostPort(ip.String(), strconv.Itoa(port)), TargetAddr: net.JoinHostPort(host, strconv.Itoa(n))}
	rule.Normalize()
	if rule.Validate() != nil {
		return profile.LocalForward{}, "Enter a valid IP address, such as 192.0.2.10"
	}
	return rule, ""
}

func (f *localForwardForm) load(r profile.LocalForward) {
	host, port, _ := net.SplitHostPort(r.ListenAddr)
	thost, tport, _ := net.SplitHostPort(r.TargetAddr)
	*f = localForwardForm{Open: true, Editing: new(r.Key()), Port: port, Target: thost, TPort: tport, Listen: listenCustom, CustomIP: host}
	if r.Protocol == profile.PortForwardUDP {
		f.Proto = 1
	}
	switch host {
	case listenLoopback4, listenLoopback6, listenAll:
		f.Listen = host
	}
}
