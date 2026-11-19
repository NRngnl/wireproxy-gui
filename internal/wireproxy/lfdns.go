package wireproxy

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/netip"

	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

var errTargetNameNotFound = errors.New("target name has no address in the WireGuard DNS")

func lookupTunnel(ctx context.Context, tnet *netstack.Net, servers []netip.Addr, host string) (netip.Addr, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return netip.Addr{}, err
	}
	lastErr := errTargetNameNotFound
	for _, server := range servers {
		for _, qtype := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
			ip, err := queryTunnel(ctx, tnet, netip.AddrPortFrom(server, 53), name, qtype)
			if err == nil {
				return ip, nil
			}
			if ctx.Err() != nil {
				return netip.Addr{}, ctx.Err()
			}
			lastErr = err
		}
	}
	return netip.Addr{}, lastErr
}

func queryTunnel(ctx context.Context, tnet *netstack.Net, server netip.AddrPort, name dnsmessage.Name, qtype dnsmessage.Type) (netip.Addr, error) {
	conn, err := tnet.DialContext(ctx, "udp", server.String())
	if err != nil {
		return netip.Addr{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var idBytes [2]byte
	_, _ = rand.Read(idBytes[:])
	id := binary.BigEndian.Uint16(idBytes[:])
	query := dnsmessage.Message{
		ID: id, RecursionDesired: true,
		Questions: []dnsmessage.Question{{Name: name, Type: qtype, Class: dnsmessage.ClassINET}},
	}
	packed, err := query.Pack()
	if err != nil {
		return netip.Addr{}, err
	}
	_, err = conn.Write(packed)
	if err != nil {
		return netip.Addr{}, err
	}
	buf := make([]byte, 512)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return netip.Addr{}, err
		}
		var reply dnsmessage.Message
		if reply.Unpack(buf[:n]) != nil || reply.ID != id || !reply.Response {
			continue
		}
		for _, answer := range reply.Answers {
			switch body := answer.Body.(type) {
			case *dnsmessage.AResource:
				return netip.AddrFrom4(body.A), nil
			case *dnsmessage.AAAAResource:
				return netip.AddrFrom16(body.AAAA), nil
			}
		}
		return netip.Addr{}, errTargetNameNotFound
	}
}
