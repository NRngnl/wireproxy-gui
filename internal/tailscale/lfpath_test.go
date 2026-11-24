package tailscale

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NRngnl/wireproxy-gui/internal/connection"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestTargetPath(t *testing.T) {
	peer := func(name, ip, cur, relay string, active bool) *ipnstate.PeerStatus {
		return &ipnstate.PeerStatus{DNSName: name + ".tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr(ip)}, CurAddr: cur, Relay: relay, Active: active}
	}
	status := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
		key.NewNode().Public(): peer("direct", "100.64.0.2", "192.0.2.1:41641", "fra", true),
		key.NewNode().Public(): peer("relayed", "100.64.0.3", "", "tok", true),
		key.NewNode().Public(): peer("idle", "100.64.0.4", "", "tok", false),
		key.NewNode().Public(): peer("idledirect", "100.64.0.8", "192.0.2.1:41641", "fra", false),
		key.NewNode().Public(): {DNSName: "viapeer.tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.5")}, Relay: "tok", PeerRelay: "192.0.2.9:7777:vni:3", Active: true},
		key.NewNode().Public(): {DNSName: "viapeer6.tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.6")}, Relay: "tok", PeerRelay: "[2001:db8::9]:7777:vni:12", Active: true},
		key.NewNode().Public(): {DNSName: "idlepeer.tail.ts.net.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.7")}, Relay: "tok", PeerRelay: "192.0.2.9:7777:vni:3", Active: false},
	}}
	cases := map[string]connection.PeerPath{
		"direct:22":       {Kind: connection.PeerPathDirect},
		"relayed:22":      {Kind: connection.PeerPathRelay, Relay: "tok"},
		"idle:22":         {},
		"idledirect:22":   {},
		"100.64.0.3:22":   {Kind: connection.PeerPathRelay, Relay: "tok"},
		"192.168.1.10:22": {},
		"missing:22":      {},
		"viapeer:22":      {Kind: connection.PeerPathPeerRelay, Relay: "192.0.2.9:7777"},
		"viapeer6:22":     {Kind: connection.PeerPathPeerRelay, Relay: "[2001:db8::9]:7777"},
		"idlepeer:22":     {},
	}
	for target, want := range cases {
		if got := targetPath(status, target); got != want {
			t.Errorf("targetPath(%q) = %+v, want %+v", target, got, want)
		}
	}
}

func TestRealNodeHandleLogTriggersPathWakeup(t *testing.T) {
	node := &realNode{}
	var hits atomic.Int32
	cb := func() { hits.Add(1) }
	node.onPath.Store(&cb)

	triggers := []string{
		"magicsock: disco: node %v %v now using %v mtu=%v",
		"magicsock: disco: node %v %v now using %v mtu=%v tx=%x",
		"magicsock: new contact: peer=%s usec=%d cached=%v via=derp",
		"magicsock: new contact: peer=%s usec=%d cached=%v via=direct",
		"[v1] magicsock: derp route for %s set to derp-%d (%s)",
		"[v1] magicsock: derp route for %s changed from derp-%d => derp-%d (%s)",
		"magicsock: derp-%d does not know about peer %s, removing route",
		"magicsock: home is now derp-%v (%v)",
		"magicsock: performing rebind due to %q",
		"magicsock: disco: failed to send %v to %v %s: %v",
		"[v1] magicsock: doing cleanup for wireguard key %s",
	}
	for _, format := range triggers {
		before := hits.Load()
		node.handleLog(format)
		if hits.Load() != before+1 {
			t.Errorf("handleLog(%q) did not wake path watcher", format)
		}
	}

	ignored := []string{
		"control: map response received",
		"magicsock: disco key = %v",
		"magicsock: %v active derp conns%s",
		"wgengine: setConfig",
	}
	for _, format := range ignored {
		before := hits.Load()
		node.handleLog(format)
		if hits.Load() != before {
			t.Errorf("handleLog(%q) unexpectedly woke path watcher", format)
		}
	}
}

type dynamicStatusClient struct {
	mu     sync.Mutex
	status *ipnstate.Status
	calls  int
}

func (c *dynamicStatusClient) GetPrefs(context.Context) (*ipn.Prefs, error) { return &ipn.Prefs{}, nil }
func (c *dynamicStatusClient) EditPrefs(context.Context, *ipn.MaskedPrefs) (*ipn.Prefs, error) {
	return &ipn.Prefs{}, nil
}
func (c *dynamicStatusClient) Status(context.Context) (*ipnstate.Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.status, nil
}
func (c *dynamicStatusClient) setStatus(s *ipnstate.Status) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
}
func (c *dynamicStatusClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type dynamicPeerNode struct {
	tsNode
	initial chan func(*ipnstate.Status)
	changed chan func()
	dialed  chan string
}

func (n *dynamicPeerNode) WatchPeers(ctx context.Context, initial func(*ipnstate.Status), changed func()) error {
	n.initial <- initial
	n.changed <- changed
	<-ctx.Done()
	return ctx.Err()
}

func (n *dynamicPeerNode) DialUserspace(_ context.Context, network string, _ netip.AddrPort) (net.Conn, error) {
	c1, c2 := net.Pipe()
	go func() {
		buf := make([]byte, 16)
		_, _ = c2.Read(buf)
		_ = c2.Close()
	}()
	n.dialed <- network
	return c1, nil
}

func TestPeerViewDynamicPathTransitionsAndIdleExpiryWithoutPolling(t *testing.T) {
	pk := key.NewNode().Public()
	makeStatus := func(active bool, cur, peerRelay, relay string, lastWrite time.Time) *ipnstate.Status {
		return &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			pk: {
				DNSName:      "remote.tail.ts.net.",
				TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.2")},
				CurAddr:      cur,
				PeerRelay:    peerRelay,
				Relay:        relay,
				Active:       active,
				LastWrite:    lastWrite,
			},
		}}
	}

	idle := makeStatus(false, "", "", "tok", time.Time{})
	client := &dynamicStatusClient{status: idle}
	node := &dynamicPeerNode{
		initial: make(chan func(*ipnstate.Status), 1),
		changed: make(chan func(), 1),
		dialed:  make(chan string, 4),
	}
	onPaths := make(chan connection.PeerPath, 8)
	ctx, cancel := context.WithCancel(t.Context())
	var view *peerView
	view = newPeerView(ctx, node, client, func() {
		onPaths <- targetPath(view.status.Load(), "remote:22")
	})
	view.expiryWait = 40 * time.Millisecond
	defer view.wait()
	defer cancel()

	initial := <-node.initial
	notifyChanged := <-node.changed
	initial(idle)

	select {
	case p := <-onPaths:
		if p != (connection.PeerPath{}) {
			t.Fatalf("initial path = %+v, want empty", p)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial path")
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("idle peer triggered %d status calls, want 0", got)
	}

	client.setStatus(makeStatus(true, "", "", "tok", time.Now()))
	conn, err := dialTailnet(t.Context(), node, view, "tcp", "remote:22")
	if err != nil {
		t.Fatalf("dialTailnet tcp: %v", err)
	}
	_ = conn.Close()
	<-node.dialed
	select {
	case p := <-onPaths:
		want := connection.PeerPath{Kind: connection.PeerPathRelay, Relay: "tok"}
		if p != want {
			t.Fatalf("after tcp dial path = %+v, want %+v", p, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for DERP relay path after TCP dial")
	}

	client.setStatus(makeStatus(true, "", "192.0.2.9:7777:vni:3", "tok", time.Now()))
	notifyChanged()
	select {
	case p := <-onPaths:
		want := connection.PeerPath{Kind: connection.PeerPathPeerRelay, Relay: "192.0.2.9:7777"}
		if p != want {
			t.Fatalf("after peer-relay wakeup path = %+v, want %+v", p, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for peer-relay path")
	}

	client.setStatus(makeStatus(true, "192.0.2.1:41641", "", "tok", time.Now()))
	notifyChanged()
	select {
	case p := <-onPaths:
		want := connection.PeerPath{Kind: connection.PeerPathDirect}
		if p != want {
			t.Fatalf("after direct wakeup path = %+v, want %+v", p, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct path")
	}

	client.setStatus(idle)
	select {
	case p := <-onPaths:
		if p != (connection.PeerPath{}) {
			t.Fatalf("after idle expiry path = %+v, want empty", p)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for idle expiry to clear path")
	}

	callsWhenIdle := client.callCount()
	time.Sleep(120 * time.Millisecond)
	if got := client.callCount(); got != callsWhenIdle {
		t.Fatalf("status calls increased from %d to %d while idle (polling detected)", callsWhenIdle, got)
	}

	client.setStatus(makeStatus(true, "", "", "tok", time.Now()))
	uconn, err := dialTailnet(t.Context(), node, view, "udp", "remote:53")
	if err != nil {
		t.Fatalf("dialTailnet udp: %v", err)
	}
	<-node.dialed
	_, err = uconn.Write([]byte("hi"))
	if err != nil {
		t.Fatalf("udp write: %v", err)
	}
	_ = uconn.Close()
	select {
	case p := <-onPaths:
		want := connection.PeerPath{Kind: connection.PeerPathRelay, Relay: "tok"}
		if p != want {
			t.Fatalf("after udp write path = %+v, want %+v", p, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for path update after UDP write")
	}
}
