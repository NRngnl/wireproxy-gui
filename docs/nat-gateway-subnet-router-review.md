# Feasibility Review: "NAT-like things" in wireproxy-gui (tsnet embedding)

**Question investigated (verbatim):** *"I mean I want to implement nat like things in this app, is this possible?"*

Scope: this is an independent engineering review of the app's existing `internal/tailscale/runner.go`
(tsnet userspace-networking embedding, one SOCKS5 listener per profile) against three concrete
NAT-adjacent features: (1) port forwarding/DNAT, (2) full LAN subnet-router/SNAT gateway, (3) this
app's node advertising itself as an exit node. Findings below are grounded in reading
`tailscale.com@v1.102.4`'s `tsnet/tsnet.go`, `ipn/prefs.go`, `wgengine/netstack/netstack.go`, and this
repo's `internal/tailscale/runner.go` / `runner_test.go`. No code was changed as part of this review.

---

## 1. Port forwarding / DNAT (inbound tailnet → local LAN)

**Verdict: buildable, no tsnet blocker.**

### The mechanism
`tsnet.Server.Listen(network, addr)` (tsnet.go:1274) is exactly the right primitive:

- It "announces only on the Tailscale network" and returns a plain `net.Listener`.
- Per its doc comment (tsnet.go:1264-1272): *"Listeners which do not specify an IP address will match
  for traffic for the local node... only. To listen for traffic on other addresses such as those routed
  inbound via subnet routes, explicitly specify the listening address."* For this feature you listen on
  the node's own tailnet IP (or `:port` for "any of my addresses"), which is the documented common case
  — no subnet-router complexity needed.
- Internally, `Listen` registers into `s.listeners` keyed by `(network, addr, port, funnel)`
  (`registerListener`), and `getTCPHandlerForFlow` (tsnet.go:1237-1249) dispatches inbound netstack
  flows to it by exact/wildcard match. This is a fully supported, first-class tsnet code path, not an
  edge case or a hack — it's the same primitive `ListenSSH`/`ListenTLS`/`ListenFunnel` build on.
- Once you have a `net.Conn` from `Accept()`, forwarding to a local LAN address is *not* tailnet traffic
  — you `net.Dial("tcp", "192.168.1.50:22")` (plain OS dial, not `node.Dial`, since the LAN target isn't
  a tailnet peer) and `io.Copy` bidirectionally. This is identical in spirit to what `runSocks5` already
  does for the CONNECT/UDP-ASSOCIATE path, just with the copy direction and listener side reversed.

### Fitting the existing runner.go shape
The existing patterns translate cleanly:

- **`tsNode` interface**: add `Listen(network, addr string) (net.Listener, error)` (mirrors the existing
  `Dial` method signature style) implemented on `realNode` by delegating to `n.server.Listen(...)`, and
  on `fakeTSNode` in tests by a fake listener/func field, matching how `dialFunc` is already faked today.
- **Profile config**: add a `PortForwards []PortForward` field to `TailscaleConfig` (or a new top-level
  Profile field, following the existing flat-struct convention seen in `TailscaleConfig`/`Profile`), e.g.:
  ```go
  type PortForward struct {
      ListenPort int    // port on this node's tailnet IP
      TargetAddr string // "host:port" on the local LAN, dialed via net.Dial
  }
  ```
  `Normalize()`/`Validate()` would need port-range and non-empty-target checks, same tier of validation
  as `TailscaleConfig.Validate()` presumably already does for `AuthKey`/`ControlURL`.
- **`Runner.Start()`**: after `node.Up(procCtx)` succeeds and before/alongside the existing
  `go r.serveSocks5(...)` goroutine, loop over the profile's configured forwards, call
  `node.Listen("tcp", fmt.Sprintf(":%d", fw.ListenPort))` for each, and launch one goroutine per forward
  running an accept loop that does `io.Copy` both ways per connection (a small helper, e.g.
  `runPortForward(ctx, listener, targetAddr string)`).
- **Lifecycle/cleanup**: this needs real design work, not just bolting one more listener onto `process`.
  Today `process` holds exactly *one* `listener` (the SOCKS5 one) and `close()` closes it directly
  (runner.go: `process.close`). Multiple port-forward listeners need either a `[]net.Listener` slice on
  `process` or (cleaner) each forward goroutine registering its own listener with the *same* `procCtx`
  cancellation and closing on `<-procCtx.Done()`, since `tsnet.Server.Close()` (called via `node.Close()`
  in `process.close()`) will itself tear down all registered `s.listeners` when the server closes — so
  strictly speaking explicit per-forward `listener.Close()` calls are only needed for fast/clean shutdown
  ordering, not for correctness (the node close will eventually reap them). Still needs:
  - Per-forward goroutine must observe `procCtx` cancellation and stop the accept loop (`Accept()` returns
    an error once the listener is closed — same pattern as `serveSocks5` returning from `server.Serve`).
  - The "started" bookkeeping in `Start()` (the `defer func(){ if started { return }; ... }` block) needs
    to also close/track port-forward listeners on failure paths, not just the SOCKS5 listener + node.
  - Port collisions: two forwards with the same `ListenPort` in one profile, or a `ListenPort` that
    collides with a port tsnet or the app itself uses, need validation before calling `Listen`.
  - Emitting `EventLog`/`EventError` per forward on failure (a `Listen` call failing on one port shouldn't
    necessarily abort the other forwards or the SOCKS5 proxy — needs an explicit decision: partial-start
    vs. all-or-nothing).

### Verdict
No tsnet architectural limitation blocks this. `Listen` + `net.Dial` + `io.Copy` relay is a completely
standard Go network-proxy pattern and tsnet explicitly supports and documents `Listen` for exactly this
kind of "accept inbound tailnet traffic and do something with it" use case. This is genuinely a "just
write the code" feature, gated only by the lifecycle-management (multi-listener-per-process) work above,
which is moderate but bounded scope on top of existing patterns.

---

## 2. Full LAN subnet-router / SNAT gateway ("real" NAT gateway)

**Verdict: fundamentally blocked for the "device on your LAN gets internet via the tailnet" or
"other tailnet peers can reach your whole LAN transparently" use case, as long as tsnet stays in
userspace-networking mode. Not a missing-implementation gap — it is architecturally excluded.**

### What real subnet routing requires
Real Tailscale subnet routing (`AdvertiseRoutes` in `ipn.Prefs`, prefs.go:201-204) is genuinely two
separable things and it matters that they're separable:

1. **Control-plane advertisement**: `AdvertiseRoutes []netip.Prefix` tells the coordination server "peers
   should route traffic destined for this CIDR through me." This is pure metadata — `tsnet` *can* set
   this field via `EditPrefs` exactly like `configureExitNode` already does for `ExitNodeID`. No blocker
   here; tsnet is a full `ipn.Prefs` client.
2. **Actual packet forwarding for the advertised CIDR**: this is where it breaks down for tsnet. In a
   normal `tailscaled` install, the advertised LAN traffic flows through a **kernel TUN device**
   (`/dev/net/tun`), and the OS kernel's IP stack (with `net.ipv4.ip_forward=1` and iptables/nftables
   MASQUERADE rules) does the actual routing between the TUN interface and the real LAN NIC. That's what
   makes "other tailnet peers reach devices on your LAN" and "LAN devices reach the tailnet" work
   transparently and for *arbitrary* protocols/ports, not just ones the app chose to proxy.

`tsnet` in userspace-networking mode has **no kernel TUN device at all** — this repo's own prior
feasibility doc already established this, and it's directly confirmed in the tsnet source:
`tsnet.go` only wires a real `tun.Device` when the (advanced, optional) `Server.Tun` field is explicitly
set; by default `s.Tun == nil` and the comment right there says *"Only process packets in netstack when
using the default fake TUN"* — i.e. by default tsnet runs entirely through gVisor's in-process netstack,
which never touches the host kernel's routing table or network interfaces at all.

Crucially, `ns.ProcessSubnets = true` (tsnet.go, set only in the `s.Tun == nil` branch) means the
*gVisor netstack itself* will happily receive and process packets destined for a subnet this node
advertises — but "process" here means "the netstack can terminate/relay packets that arrive over the
tsnet-registered listeners/dial paths inside this process," not "the OS kernel forwards arbitrary LAN
traffic between two real network interfaces." tsnet's own maintainers explicitly frame this as a test-only
capability, not a real deployment feature: `tsnet_test.go`'s `TestPingSubnetRouteOfDeltaPeer` comments
say outright *"s2 isn't a real subnet router — we don't try to forward traffic through it"* — the
project's own test suite uses "subnet router" there purely as routing-table metadata to exercise peer
selection logic, explicitly disclaiming that real forwarding happens.

### Precisely what is/isn't possible
- **Impossible, fundamentally, with default tsnet**: transparent full-LAN-CIDR forwarding where arbitrary
  devices on the user's LAN (that never run this app or any Tailscale software) can be reached by tailnet
  peers on arbitrary ports/protocols, or where those LAN devices get outbound internet access routed
  through the tailnet. There is no kernel interface for the OS to forward packets into/out of; the
  gVirtual netstack lives entirely inside this one Go process's memory and only understands the sockets
  it explicitly creates (`Listen`, `Dial`, `ListenPacket`). No `iptables`/`ip_forward` equivalent exists
  at the netstack layer for "any packet you didn't originate."
- **Theoretically possible, with real limits, via `RegisterFallbackTCPHandler` + wildcard `Listen`**: you
  *can* register a fallback TCP handler (`s.RegisterFallbackTCPHandler`, tsnet.go:1403) that intercepts
  any inbound TCP flow with no matching listener and, in the callback, `net.Dial` out to an arbitrary LAN
  destination and relay bytes — this is architecturally the same DNAT-relay idea as feature (1) above,
  generalized to "catch everything instead of one configured port." But it is fundamentally limited to:
  - **TCP only** — `RegisterFallbackTCPHandler`'s type is explicitly TCP-flow-shaped
    (`func(src, dst netip.AddrPort) (handler func(net.Conn), intercept bool)`); there's no UDP or raw-IP
    equivalent for "catch every unmatched flow and dial it out," and no ICMP forwarding either (subnet
    routers commonly need to answer/forward ping, which needs TSMP/ICMP handling the netstack does
    internally only for its own local IPs, not for arbitrary via-forwarded destinations).
  - Still requires this app's process to be alive, in the loop, and doing a Go-level `io.Copy` relay for
    every single connection — i.e. it's an *application-layer TCP proxy dressed up as subnet routing*, not
    real IP-layer NAT/forwarding. It cannot forward a raw packet it doesn't understand at the transport
    layer; it can only relay connections it can parse as TCP streams.
  - It still needs `AdvertiseRoutes` set so peers know to route the CIDR here in the first place — that
    part reuses the same `EditPrefs` mechanism `configureExitNode` already uses.
  - This gets you something closer to a manually-coded TCP-only "poor man's subnet gateway" for a *known,
    bounded* CIDR, not the transparent drop-in subnet router the user is likely picturing when they say
    "NAT gateway."
- Separately, the "LAN device without Tailscale reaches the tailnet via this app acting as a NAT box" half
  of the classic subnet-router setup requires OS-level `ip_forward`/`iptables MASQUERADE` on a *kernel* TUN
  interface — completely outside tsnet's userspace mode by construction, and outside what a GUI app doing
  `tsnet.Server{}` embedding can do without escalated privileges and a real TUN device (which is a
  different, heavier architecture change — effectively becoming `tailscaled`, not `tsnet`).

### Verdict
The "device-transparent, protocol-agnostic subnet router / full NAT gateway for a whole LAN" reading of
the question is **fundamentally blocked** by tsnet's userspace-only default networking, not just
unimplemented. A constrained, TCP-only, `RegisterFallbackTCPHandler`-based relay for a *specific known*
CIDR is theoretically buildable but is really just a scaled-up version of feature (1), with materially
weaker guarantees (TCP-only, no ICMP, still an app-layer relay under the hood) — it should not be
marketed or documented as "subnet routing" in the Tailscale sense, since it doesn't do what a real
tailnet subnet router does.

---

## 3. This app's node advertising itself AS an exit node (not just consuming one)

**Verdict: the control-plane advertisement is trivially buildable (same `EditPrefs` mechanism already in
`configureExitNode`), but it would be advertising a capability tsnet's default mode cannot actually
fulfill for real internet traffic. This is a correctness trap, not a feature — implementing only the
advertisement half without the traffic-forwarding half is actively misleading to peers who select this
node as their exit node.**

### The mechanism
`ipn/ipnlocal/local.go:2181` shows the real tailscaled CLI's `--advertise-exit-node` flag maps to
`p.SetAdvertiseExitNode(v)` on `ipn.Prefs`. Looking at `prefs.go`, `AdvertisesExitNode()` (prefs.go:820)
is defined purely in terms of `AdvertiseRoutes` containing both the all-IPv4 (`0.0.0.0/0`) and all-IPv6
(`::/0`) prefixes (`tsaddr.ContainsExitRoutes`, tsaddr.go:190) — i.e. "advertise as exit node" is *the
same* `AdvertiseRoutes` field used for subnet routing, just set to the two default-route CIDRs instead of
a specific LAN CIDR. There is no separate exit-node-specific pref field; it's a convention over
`AdvertiseRoutes`.

This means: this app's `realNode`/`tsNode` already has everything needed to set this — `configureExitNode`
already does `client.GetPrefs`/`client.EditPrefs` with a `MaskedPrefs`; adding
`AdvertiseRoutes: [0.0.0.0/0, ::/0]` + `AdvertiseRoutesSet: true` to an equivalent `EditPrefs` call is
mechanically identical work to what's already in the file.

### Why this is a trap, not a win
Exact same underlying limitation as finding 2: the control plane will happily accept the advertisement and
tell other tailnet peers "route your default route through this node" — peers would then attempt to send
*all* their internet-bound traffic (arbitrary TCP, UDP, ICMP, arbitrary destinations) to this node expecting
it to NAT it out to the real internet. But tsnet's default userspace netstack has no kernel routing table,
no `iptables MASQUERADE`, and (per finding 2) only understands packets it explicitly creates listeners or
dial calls for. There is no way for a tsnet-embedded, non-TUN node to actually forward "any TCP/UDP
connection to any internet host" the way a real `tailscaled` exit node does — `RegisterFallbackTCPHandler`
could theoretically dial *outbound* to arbitrary internet hosts per-TCP-flow (same relay pattern as finding
2), but again TCP-only, no UDP (breaks DNS via UDP, QUIC/HTTP3, WebRTC, games, etc. — a large fraction of
real "exit node" traffic), no ICMP (breaks traceroute/ping through the exit node), and it's an app-layer
relay competing with gVisor's own connection handling rather than genuine IP forwarding.

### Verdict
**Do not implement.** Setting the advertisement without also being able to genuinely forward general
internet traffic produces a node that peers can select as an exit node in the Tailscale admin UI/client,
silently fails or badly degrades (TCP-only best case, or a black hole if not implemented at all) most of
their traffic, and is worse than not offering the feature. The gap here is unlike finding 1 (which is
purely "not implemented yet") — this is a case where implementing only the easy half (control-plane
prefs edit) actively produces incorrect, misleading behavior for other tailnet members who trust the
advertisement. If pursued at all, it needs prominent UI disclaimers that only TCP is relayed and DNS/UDP/
ICMP traffic will fail — at which point it's arguably not honest to call it "exit node" support at all.

---

## 4. Implementation complexity ballpark (for what's actually buildable)

Only feature (1), port forwarding/DNAT, is recommended as real, honest, buildable engineering work.
Feature (2)'s constrained TCP-only fallback-handler variant is buildable but should be scoped and named
honestly (not "subnet router"); feature (3) should not be built as described.

### Feature 1: Port forwarding / DNAT

**New profile config surface:**
- `TailscaleConfig.PortForwards []PortForward` (or new top-level field) with `{ ListenPort int,
  TargetAddr string }`, plus `Normalize()`/`Validate()` additions (port range, non-empty target,
  duplicate-port detection within a profile).
- UI: a small repeated-row editor (list of listen-port → target-addr pairs) per profile — comparable
  scope to whatever the existing exit-node picker UI already is, likely smaller.

**New runner.go code shape:**
- `tsNode` interface: `+ Listen(network, addr string) (net.Listener, error)`.
- `realNode.Listen` — one-line delegation to `n.server.Listen`.
- `fakeTSNode` in tests: `+ listenFunc` field, mirroring the existing `dialFunc` field pattern exactly.
- `process` struct: generalize the single `listener net.Listener` field to also track a slice of
  port-forward listeners (or reuse a `[]net.Listener` for everything and special-case index 0 as the
  SOCKS5 listener, or cleaner: keep SOCKS5's field as-is and add `pfListeners []net.Listener` alongside),
  updating `setListener`-equivalent helpers and `close()` to close all of them.
- `Runner.Start()`: after `node.Up` + `configureExitNode` succeed, before `commitStart`, loop over
  `p.TailscaleConfig.PortForwards`, call `node.Listen("tcp", fmt.Sprintf(":%d", fw.ListenPort))` for each,
  collecting listeners into `proc`/failing fast (or per-forward, a policy decision) on error, then spawn
  one goroutine per successfully-opened listener running a small `runPortForward(ctx, ln, target)` accept
  loop doing `io.Copy` both directions per accepted connection with `net.Dial("tcp", target)`.
- New small helper, e.g. `internal/tailscale/portforward.go`, ~60-100 lines: accept loop + a
  `relay(a, b net.Conn)` using two goroutines and `io.Copy`, with `sync.WaitGroup`/context-cancel-driven
  connection close on either side finishing or the process context canceling.

**Testing approach**, directly modeled on `TestRunSocks5RoutesUDPAssociateThroughNode`
(runner_test.go:814+):
- Same shape: build a `fakeTSNode` with a fake `listenFunc` returning a real local
  `net.Listen("tcp","127.0.0.1:0")` listener standing in for the tailnet-side listener (since `fakeTSNode`
  already fakes `Dial` similarly by delegating to a real local dialer).
- Start a real local TCP "target" listener (`net.Listen`) representing the LAN destination, just like the
  test's `targetUDP` stands in for the eventual real destination.
- Drive a real client connection into the fake tailnet-side listener, write bytes, assert they arrive on
  the target listener (and the reverse direction), confirming the relay correctly wires accepted
  connections to `net.Dial(target)` — not `node.Dial`, which is the one correctness property most worth
  a regression test given how easy it'd be to accidentally reuse `node.Dial` (tailnet dial) instead of
  `net.Dial` (LAN dial) by copy-pasting from `runSocks5`.
- Additional test: two simultaneous port-forwards, confirming they don't share/leak listeners
  (`process`'s slice-of-listeners bookkeeping) and both get cleaned up together on `proc.close()`.

**Ballpark effort**: small-to-medium — roughly comparable to (perhaps somewhat larger than) the existing
SOCKS5 UDP-ASSOCIATE fix referenced in the task (a self-contained runner.go change plus one focused test),
because of the added multi-listener lifecycle bookkeeping and new config/validation/UI surface. A rough
guess: 1-2 focused days for a developer already familiar with this codebase, most of it in lifecycle edge
cases (partial-forward-start-failure semantics, shutdown ordering, port-collision validation) rather than
in the core `Listen`+`io.Copy` relay itself, which is genuinely simple.

### Features 2 and 3
Not recommended for implementation as described. If the constrained TCP-only fallback-relay variant of
(2) is ever wanted, its shape is a straightforward generalization of (1)'s relay code (one
`RegisterFallbackTCPHandler` callback instead of N explicit `Listen` calls) — similar order of magnitude
of code, but it inherits/compounds all of (1)'s lifecycle concerns plus needs explicit user-facing
"TCP-only, best-effort" framing to avoid being mistaken for real subnet routing.

---

## What's actually buildable, ranked

1. **Port forwarding / DNAT (feature 1) — build it.** Real, bounded, uses tsnet's documented `Listen`
   API exactly as intended, fits the existing runner.go/`tsNode`/`process`/testing patterns cleanly.
   No architectural blocker. ~1-2 days of focused work per the estimate above.

2. **Constrained TCP-only "fallback relay to a known CIDR" (a weaker cousin of feature 2) — buildable
   but not recommended to call "subnet routing."** Uses `RegisterFallbackTCPHandler`, which is real and
   documented, but only relays TCP, no ICMP/UDP, and is an app-layer relay, not real IP forwarding. If
   built, it must be named and documented for what it actually is (a generalized port-forward catch-all),
   not marketed as Tailscale-style subnet routing.

3. **Real, transparent, protocol-agnostic LAN subnet router (full feature 2 reading) — fundamentally
   blocked.** Requires a kernel TUN device and OS-level `ip_forward`/iptables NAT that tsnet's default
   userspace-networking mode does not have and cannot get without becoming a full `tailscaled`-style
   privileged install (a different, much larger architecture, not an incremental change to this app).

4. **Exit-node provision (feature 3) — do not build as literally requested.** The control-plane half is
   trivial (same `EditPrefs`/`AdvertiseRoutes` mechanism already in this file), but tsnet cannot actually
   forward general (TCP+UDP+ICMP, arbitrary destination) internet traffic the way advertising as an exit
   node implies. Shipping the advertisement without matching forwarding capability actively misleads
   other tailnet peers who select this node as their exit node. If ever attempted, it must be scoped and
   disclaimed as TCP-only best-effort, which likely isn't worth building given how narrow and confusing
   that would be relative to what "exit node" normally means.
