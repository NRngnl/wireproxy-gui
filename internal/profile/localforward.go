package profile

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

var (
	ErrLocalForwardListenInvalid = errors.New("local forward listen address must be an IP address and port")
	ErrLocalForwardTargetInvalid = errors.New("local forward target must be host:port")
	ErrLocalForwardDuplicate     = errors.New("duplicate local forward listen address")
	ErrLocalForwardSocksClash    = errors.New("local forward listen address clashes with the SOCKS5 bind address")
)

// LocalForward relays a host listen address to a target reached through the backend.
type LocalForward struct {
	Protocol   PortForwardProtocol
	ListenAddr string
	TargetAddr string
}

// LocalForwardKey identifies a local forward within one profile.
type LocalForwardKey struct {
	Protocol   PortForwardProtocol
	ListenAddr string
}

// Key returns the identity of the rule.
func (f LocalForward) Key() LocalForwardKey {
	return LocalForwardKey{Protocol: f.Protocol, ListenAddr: f.ListenAddr}
}

// Normalize trims the rule, defaults the protocol to tcp and turns a bare listen port into 127.0.0.1:<port>.
func (f *LocalForward) Normalize() {
	f.Protocol = PortForwardProtocol(strings.ToLower(strings.TrimSpace(string(f.Protocol))))
	if f.Protocol == "" {
		f.Protocol = PortForwardTCP
	}
	f.TargetAddr = strings.TrimSpace(f.TargetAddr)
	listen := strings.TrimSpace(f.ListenAddr)
	if validPort(listen) {
		listen = net.JoinHostPort(DefaultSocksHost, listen)
	}
	host, port, err := net.SplitHostPort(listen)
	if err == nil {
		ip, err := netip.ParseAddr(host)
		if err == nil && ip.Zone() == "" {
			listen = net.JoinHostPort(ip.String(), port)
		}
	}
	f.ListenAddr = listen
}

// NormalizeLocalForwardKey normalizes a lookup key the same way as a rule.
func NormalizeLocalForwardKey(key LocalForwardKey) LocalForwardKey {
	rule := LocalForward{Protocol: key.Protocol, ListenAddr: key.ListenAddr}
	rule.Normalize()
	return rule.Key()
}

// Validate checks one normalized rule.
func (f LocalForward) Validate() error {
	var errs []error
	switch f.Protocol {
	case PortForwardTCP, PortForwardUDP:
	default:
		errs = append(errs, ErrPortForwardProtocolInvalid)
	}
	if !validListenAddr(f.ListenAddr) {
		errs = append(errs, ErrLocalForwardListenInvalid)
	}
	host, port, err := net.SplitHostPort(f.TargetAddr)
	if err != nil || host == "" || !validPort(port) {
		errs = append(errs, ErrLocalForwardTargetInvalid)
	}
	return errors.Join(errs...)
}

func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

func validListenAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !validPort(port) {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Zone() == ""
}

func validateLocalForwards(p Profile) error {
	var errs []error
	seen := map[LocalForwardKey]bool{}
	for _, rule := range p.LocalForwards {
		err := rule.Validate()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[rule.Key()] {
			errs = append(errs, ErrLocalForwardDuplicate)
		}
		seen[rule.Key()] = true
		if rule.Protocol == PortForwardTCP && listenClashesSocks(rule.ListenAddr, p) {
			errs = append(errs, ErrLocalForwardSocksClash)
		}
	}
	return errors.Join(errs...)
}

func listenClashesSocks(listen string, p Profile) bool {
	host, port, _ := net.SplitHostPort(listen)
	socksHost, socksPort, err := net.SplitHostPort(p.BindAddress())
	if err != nil || port != socksPort {
		return false
	}
	a, errA := netip.ParseAddr(host)
	b, errB := netip.ParseAddr(socksHost)
	if errA != nil || errB != nil {
		return host == socksHost
	}
	return a == b || a.IsUnspecified() || b.IsUnspecified()
}

func localForwardsEqual(a, b []LocalForward) bool {
	return len(a) == len(b) && (len(a) == 0 || equalForwards(a, b))
}

func equalForwards(a, b []LocalForward) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
