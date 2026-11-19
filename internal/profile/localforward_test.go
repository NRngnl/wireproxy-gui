package profile

import (
	"errors"
	"testing"
)

func TestLocalForwardNormalizeValidate(t *testing.T) {
	base := New("n", "[Interface]\nAddress=10.0.0.2/32\nPrivateKey=k\n[Peer]\nPublicKey=p\n", 1080)
	tests := []struct {
		name    string
		rules   []LocalForward
		listen  string
		wantErr bool
	}{
		{"bare port", []LocalForward{{ListenAddr: "2222", TargetAddr: "pc:22"}}, "127.0.0.1:2222", false},
		{"ipv6", []LocalForward{{ListenAddr: "[::1]:2222", TargetAddr: "pc:22"}}, "[::1]:2222", false},
		{"duplicate", []LocalForward{{ListenAddr: "2222", TargetAddr: "a:1"}, {ListenAddr: "2222", TargetAddr: "b:1"}}, "", true},
		{"udp same port ok", []LocalForward{{ListenAddr: "2222", TargetAddr: "a:1"}, {Protocol: PortForwardUDP, ListenAddr: "2222", TargetAddr: "b:1"}}, "", false},
		{"socks clash", []LocalForward{{ListenAddr: "0.0.0.0:1080", TargetAddr: "a:1"}}, "", true},
		{"hostname listen", []LocalForward{{ListenAddr: "localhost:2222", TargetAddr: "a:1"}}, "", true},
		{"bad target", []LocalForward{{ListenAddr: "2222", TargetAddr: "pc"}}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.LocalForwards = tc.rules
			p.Normalize()
			err := p.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.listen != "" && p.LocalForwards[0].ListenAddr != tc.listen {
				t.Fatalf("listen = %q, want %q", p.LocalForwards[0].ListenAddr, tc.listen)
			}
		})
	}
}

func TestLocalForwardCanonicalAndValidate(t *testing.T) {
	p := New("n", "x", 1080)
	p.LocalForwards = []LocalForward{
		{ListenAddr: "[::ffff:127.0.0.1]:002222", TargetAddr: "PC:0022"},
		{ListenAddr: "127.0.0.1:2222", TargetAddr: "pc:22"},
		{Protocol: PortForwardUDP, ListenAddr: "224.0.0.1:53", TargetAddr: "a:1"},
		{Protocol: PortForwardUDP, ListenAddr: "255.255.255.255:53", TargetAddr: "a:1"},
		{ListenAddr: "[::]:1080", TargetAddr: "a:1"},
		{ListenAddr: "127.0.0.1:3333", TargetAddr: "bad host:1"},
	}
	p.Normalize()
	if got := p.LocalForwards[0]; got.ListenAddr != "127.0.0.1:2222" || got.TargetAddr != "PC:22" {
		t.Fatalf("canonical = %#v", got)
	}
	err := p.ValidateLocalForwards()
	if !errors.Is(err, ErrLocalForwardDuplicate) || !errors.Is(err, ErrLocalForwardSocksClash) {
		t.Fatalf("ValidateLocalForwards() = %v", err)
	}
}
