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

// Normalize trims the rule, defaults the protocol to tcp, turns a bare listen port into 127.0.0.1:<port>
// and writes the listen address and target port in canonical form.
func (f *LocalForward) Normalize() {
	f.Protocol = PortForwardProtocol(strings.ToLower(strings.TrimSpace(string(f.Protocol))))
	if f.Protocol == "" {
		f.Protocol = PortForwardTCP
	}
	f.TargetAddr = canonicalHostPort(strings.TrimSpace(f.TargetAddr), false)
	listen := strings.TrimSpace(f.ListenAddr)
	if validPort(listen) {
		listen = net.JoinHostPort(DefaultSocksHost, listen)
	}
	f.ListenAddr = canonicalHostPort(listen, true)
}

func canonicalHostPort(addr string, ipHost bool) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !validPort(port) {
		return addr
	}
	number, _ := strconv.Atoi(port)
	ip, parseErr := netip.ParseAddr(host)
	if parseErr == nil && ip.Zone() == "" {
		host = ip.Unmap().String()
	} else if ipHost {
		return addr
	}
	if !ipHost {
		host = strings.TrimSuffix(host, ".")
	}
	return net.JoinHostPort(host, strconv.Itoa(number))
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
	if err != nil || !validTargetHost(host) || !validPort(port) {
		errs = append(errs, ErrLocalForwardTargetInvalid)
	}
	return errors.Join(errs...)
}

func validPort(s string) bool {
	n, err := strconv.ParseUint(s, 10, 16)
	return err == nil && n >= 1
}

func validTargetHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err == nil {
		return ip.Zone() == ""
	}
	for _, r := range host {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (r < '0' || r > '9') && r != '-' && r != '.' && r != '_' {
			return false
		}
	}
	return !strings.HasPrefix(host, ".") && !strings.HasPrefix(host, "-")
}

func validListenAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !validPort(port) {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Zone() == "" && !ip.Is4In6() && !ip.IsMulticast() && ip != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// SanitizeLocalForwards drops rules that fail validation, duplicate an earlier key or clash with the SOCKS5 bind,
// and returns the number dropped. Call after Normalize on data that did not come through Validate.
func (p *Profile) SanitizeLocalForwards() int {
	kept := make([]LocalForward, 0, len(p.LocalForwards))
	seen := map[LocalForwardKey]bool{}
	for _, rule := range p.LocalForwards {
		if rule.Validate() != nil || seen[rule.Key()] || (rule.Protocol == PortForwardTCP && listenClashesSocks(rule.ListenAddr, *p)) {
			continue
		}
		seen[rule.Key()] = true
		kept = append(kept, rule)
	}
	dropped := len(p.LocalForwards) - len(kept)
	if dropped > 0 {
		p.LocalForwards = kept
	}
	return dropped
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
	if err != nil {
		return false
	}
	a, errA := netip.ParseAddr(host)
	b, errB := netip.ParseAddr(socksHost)
	if errA != nil || errB != nil {
		return false
	}
	pa, _ := strconv.Atoi(port)
	pb, err := strconv.Atoi(socksPort)
	if err != nil || pa != pb {
		return false
	}
	a, b = a.Unmap(), b.Unmap()
	return a == b || coversAddr(a, b) || coversAddr(b, a)
}

func coversAddr(wild, other netip.Addr) bool {
	if !wild.IsUnspecified() {
		return false
	}
	return wild.Is6() || other.Is4()
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
