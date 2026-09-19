# Feasibility: "NAT-like" Features (Subnet Router, Exit Node Advertise, Port Forwarding, Route Advertisement)

**Question investigated (verbatim):** *"I mean I want to implement nat like things in this app, is this possible?"*

**Scope of this doc:** four concrete interpretations of "NAT-like" against this app's actual architecture — `internal/tailscale/runner.go` (the `tsNode`/`localClient` interfaces, `Runner`, `configureExitNode`) and the embedded `tailscale.com/tsnet` library at `v1.102.4` (module path confirmed via `go env GOMODCACHE`; also present at `v1.102.3` — this app pins one, verify with `go list -m tailscale.com` if it matters). Prior findings (`docs/portable-tailscale-server-client-feasibility.md`) already established: tsnet runs a **userspace gVisor netstack with no kernel TUN device**, SOCKS5 UDP ASSOCIATE now routes through `node.Dial`, and SSH works as a SOCKS5 `ProxyCommand`. This doc does not repeat that reasoning — it extends it to NAT/routing-style features.

The single most important fact governing all four options is in `tsnet.go`:

```go
// tsnet.go, Server.start()
if s.Tun == nil {
    // Only process packets in netstack when using the default fake TUN.
    // When a TUN is provided, let packets flow through it instead.
    ns.ProcessLocalIPs = true
    ns.ProcessSubnets = true   // <-- set true even in userspace mode!
} else {
    ns.CheckLocalTransportEndpoints = true
}
```

and in `wgengine/netstack/netstack.go`:

```go
// ProcessSubnets is whether netstack should handle incoming
// traffic destined to non-local IPs (i.e. whether it should
// be a subnet router).
// It can only be set before calling Start.
ProcessSubnets bool
```

`tsnet.Server` always uses the **fake/null `Tun`** (`s.Tun` is nil unless you explicitly wire a real one — nothing in this app or tsnet's public API sets it), so `ProcessSubnets = true` is unconditionally set for this app's use of tsnet. This means tsnet's *inbound* packet-processing path is architecturally already capable of accepting subnet-routed traffic addressed to non-local IPs, entirely inside the gVisor netstack, without any kernel TUN device or OS routing table entry. That single fact reshapes the verdicts below — it is *not* uniformly "fundamentally blocked," but it is also not free of caveats, because "receiving subnet packets" and "getting other peers' traffic routed to you at the control-plane level and delivering it back out to a real LAN" are different problems.

---

## (A) Subnet Router / Gateway — LAN devices without Tailscale reach the tailnet through this app's host

**Verdict: (2) moderate source change, with one structural caveat that keeps it short of Tailscale's real subnet-router UX — not (3) fundamentally blocked.**

What "real" Tailscale subnet routing does on a normal (non-userspace) node: the node advertises `Prefs.AdvertiseRoutes` (a CIDR, e.g. `192.168.1.0/24`), the admin approves it in the tailnet admin console, and the OS kernel — via the real TUN device and iptables/pf rules that `tailscaled` installs — forwards packets from LAN-facing NICs into the TUN and vice versa. That kernel-level NAT/forwarding-table integration is what `tailscaled` normally does and what tsnet explicitly does **not** do (no OS integration at all — see `tsnet.go` package doc: "userspace TCP/IP stack (gVisor)").

However, `wgengine/netstack.go`'s `ProcessSubnets` mechanism shows that netstack itself can be the forwarder, entirely inside the Go process, no kernel routing needed:

```go
if ns.ProcessSubnets && !isLocal {
    return true   // shouldProcessInbound(): accept non-local-IP packets for processing
}
```

and further down, `injectInbound` hands such packets to gVisor's TCP/UDP stack (`ns.linkEP.gro(p, gro)`), which eventually reaches `ns.forwardTCP`/UDP-forwarding code that dials out (`getTCPHandlerForFlow`, `ns.dialer.SystemDial`-equivalent paths) — i.e. netstack answers the TCP handshake and then relays bytes to whatever `dialIP:port` the packet targeted. This is exactly the subnet-router data plane, implemented in userspace instead of the kernel.

**The caveat that keeps this at (2) not (4):** for a *third-party LAN device that runs no Tailscale software at all* to reach this mechanism, its packets first have to physically arrive at the host machine addressed to a tailnet-routable CIDR, and get onto the WireGuard-over-UDP transport this app's tsnet node maintains. tsnet's netstack can *process* subnet-destined packets once they exist inside its own inbound WireGuard stream, but tsnet gives you **no way to inject a foreign LAN device's raw IP packets into that stream** — there is no `s.Tun` wired to a real network interface, and no public tsnet API for accepting raw packets from an OS-level interface. So the "how do I get LAN traffic that isn't already Tailscale WireGuard traffic into netstack" problem is unsolved by tsnet's public surface. In practice, for LAN device X to use this app's host as a gateway to tailnet peers, X's traffic must be steered to the host via something outside tsnet's scope: an OS default-route change on X pointing to the host + this app's Go process running its own kernel-adjacent NAT/iptables (which contradicts "no root, no TUN"), or X running a SOCKS5/HTTP proxy client pointed at this app's *existing* per-profile SOCKS5 listener (`runSocks5`) — which is not really "gateway/subnet-router" semantics, it's proxy semantics, and only works for proxy-aware clients, not arbitrary LAN devices/protocols.

**Bottom line for (A):** tsnet's own netstack has the *receiving* half of subnet-router logic built in (`ProcessSubnets`), so *tailnet peers* reaching this app's advertised subnets works at the tsnet layer once routes are advertised and approved (this is really case (D), see below). But making **non-Tailscale LAN devices** use this app as their gateway needs OS-level routing/NAT on the host that tsnet does not and cannot provide in userspace mode — that part genuinely requires privileged host networking work outside this app's and tsnet's current architecture (root, iptables/pf, or an OS default gateway change), which is out of scope for "no TUN, no root" as this app is built. If the ask is narrowed to "can LAN devices proxy through this app to reach tailnet peers" (not full transparent gateway semantics), that already works today via the existing SOCKS5 listener with no new code, just binding it to `0.0.0.0` instead of loopback (see `runSocks5`, `net.Listener` bind address) plus a caution that this exposes the proxy without Tailscale-grade ACLs.

---

## (B) Exit Node Provision — this app's tsnet node advertises itself as an exit node for OTHER peers

**Verdict: (3) fundamentally blocked for meaningful/general internet-egress traffic, in a way distinct from (A)/(D) — because being a *client's* selected exit node requires being the endpoint of ALL of that client's non-tailnet IP traffic, which requires the same "receive arbitrary foreign IP traffic and NAT it out to the general internet" capability tsnet's userspace mode structurally cannot provide without a kernel TUN + OS default route.**

The reverse direction the app already implements is `configureExitNode` in `runner.go`: it sets `prefs.ExitNodeID`/`prefs.AutoExitNode` so **this node's own traffic** routes through a peer-selected exit node. That is a client-side `Prefs` field consumed by the *other* node's tsnet/tailscaled — it says nothing about advertising this node as an exit node for others.

To advertise as an exit node, `ipn/prefs.go` shows the mechanism is `SetAdvertiseExitNode`:

```go
// SetAdvertiseExitNode mutates p (if non-nil) to add or remove the two
// /0 exit node routes.
func (p *Prefs) SetAdvertiseExitNode(runExit bool) {
    ...
    p.AdvertiseRoutes = append(p.AdvertiseRoutes,
        netip.PrefixFrom(netaddr.IPv4(0, 0, 0, 0), 0),
        netip.PrefixFrom(netip.IPv6Unspecified(), 0))
}
```

Advertising as an exit node is literally `AdvertiseRoutes = [0.0.0.0/0, ::/0]` — the *global* default routes, i.e. "route ALL of the peer's internet traffic through me." This is a strict superset of subnet-router advertisement (case D) with the same "Linux-only… requires additional manual configuration" caveats documented directly on `NoSNAT`:

```go
// NoSNAT specifies whether to source NAT traffic going to
// destinations in AdvertiseRoutes... Disabling SNAT requires
// additional manual configuration in your network to route
// Tailscale traffic back to the subnet relay machine.
//
// Linux-only.
NoSNAT bool
```

Setting `AdvertiseRoutes` via `client.EditPrefs` is trivially reachable from this app's existing `localClient.EditPrefs` (already used in `configureExitNode`), so *setting the preference bits* is a one-line change reusing the exact pattern already in `runner.go`. That part is (2)-level. But setting the preference is the easy 10% — the field's own doc comments say "The following block of options only have an effect on Linux" and NoSNAT/NoStatefulFiltering are explicitly "Linux-only," strongly implying the actual traffic-forwarding side of subnet/exit-node advertisement was designed and tested against `tailscaled`'s Linux kernel netfilter/routing integration, not tsnet's userspace netstack.

Practically: for another peer to actually use this node as their exit node, ALL of that peer's non-tailnet-destined packets get encapsulated and sent to this node over WireGuard, and this node must then egress them to the real internet and NAT the return traffic back. `wgengine/netstack.go`'s inbound path (`shouldProcessInbound` → `ProcessSubnets` branch → forward-and-dial) can technically receive and relay such packets the same way it would for subnet routes — tsnet's netstack doesn't distinguish "exit-node default route" packets from "subnet route" packets at the mechanics level, both just land as `!isLocal` destinations and get dialed out via the host's *userspace-visible* network (`s.dialer`, ultimately real OS sockets — so egress does actually work through the process's own outbound sockets, no TUN needed for outbound). This is a meaningful difference from (A): tsnet CAN in principle open outbound sockets to the real internet on behalf of relayed inbound flows, because doing so is just Go-level `net.Dial` from within the process — no kernel routing table entry is required for *outbound* dials, only for delivering *inbound* packets from a foreign device.

So the practical blocker is narrower than "impossible": the client side (`ExitNodeID` selection consuming a userspace exit node) is unverified/unsupported in tsnd/tailscaled's control-plane semantics, and DNS/route-table push-down assumptions Tailscale's coordination server and other peers' `tailscaled` clients make when they pick an exit node (e.g. expecting the exit node to also handle DNS resolution consistently, ICMP correctly, and sustain concurrent flow volume) were built assuming a full non-userspace node. There is no tsnet API guarantee or example anywhere in `tsnet.go` for "advertise as exit node," and the upstream Tailscale project documents subnet routers/exit nodes as Linux/router-appliance features, never as a userspace-tsnet capability. Given no first-party validation exists that a userspace-mode tsnet node correctly serves as a full exit node under real load (all of another device's TCP/UDP/ICMP traffic, sustained, for arbitrary destinations) — and given the doc comments repeatedly flag Linux-only kernel-level assumptions for the SNAT/stateful-filtering half of the feature — this is graded **(3) fundamentally blocked for reliable general-purpose exit-node service**, even though the preference-setting code path and even some forwarding mechanics exist. It could conceivably "sort of work" for light/best-effort use in a lab test, but is not something to build and ship as a supported app feature without upstream tsnet support that doesn't currently exist.

---

## (C) Port Forwarding — inbound tailnet-IP:port → local LAN service relay

**Verdict: (1) technically possible today via tsnet's existing public API, and (2) moderate, well-scoped app-side change to wire it into this app's architecture. This is the most concretely buildable of the four.**

`tsnet.go`'s `Server.Listen` is the exact primitive needed:

```go
// Listen announces only on the Tailscale network.
// It will start the server if it has not been started yet.
//
// Listeners which do not specify an IP address will match for traffic
// for the local node (that is, a destination address of the IPv4 or
// IPv6 address of this node) only. To listen for traffic on other addresses
// such as those routed inbound via subnet routes, explicitly specify
// the listening address or use RegisterFallbackTCPHandler.
func (s *Server) Listen(network, addr string) (net.Listener, error) {
    return s.listen(network, addr, listenOnTailnet)
}
```

This returns a plain `net.Listener` bound to *this node's own tailnet IP* on a chosen port — no kernel routing, no subnet advertisement, no exit-node semantics needed, because it's listening for traffic addressed directly to the node's own IP (which every tailnet peer can already reach by design). This is precisely the "listen locally, forward to a local LAN target" NAT/port-forward pattern: accept on `Listen("tcp", ":8443")`, then for each accepted `net.Conn`, `net.Dial("tcp", "192.168.1.50:80")` (or any LAN target reachable from the host) and pump bytes both directions (`io.Copy` both ways in goroutines) — a completely standard Go TCP relay, no tsnet magic required for the LAN-facing half.

Architecture fit in this app is strong:
- The `tsNode` interface (`runner.go`) already narrows `*tsnet.Server` down to `Up`, `LocalClient`, `Dial`, `Close`. Adding a `Listen(network, addr string) (net.Listener, error)` method to `tsNode` and `realNode` is a two-line addition, following exactly the same wrapper pattern already used for `Dial`.
- `runSocks5` in `runner.go` already establishes the pattern of "take a `net.Listener`, run an accept loop, dial out per-connection" (that's what the SOCKS5 server does, per-profile). A new `runPortForward(ctx, node, listener, targetAddr string)` function is a straightforward sibling to `runSocks5`, reusing the same `process`/`Runner` lifecycle (`process.listener`, `process.node`, `setNode`/`getNode`, `markDone`) with no changes to those mechanics.
- Per-profile config would need a small addition to `profile.TailscaleConfig` (e.g. a list of `{ListenPort int; TargetAddr string}` forward rules) plus a small settings-UI addition — moderate, additive, no architectural rework.
- UDP forwarding is also directly supported: `tsnet.Server.ListenPacket(network, addr string) (net.PacketConn, error)` exists for `"udp"/"udp4"/"udp6"` with the same "IP must be specified" contract, so UDP port-forwarding (not just TCP) is equally buildable with the same relay pattern already validated for SOCKS5 UDP ASSOCIATE per the prior feasibility doc.

No aspect of this requires a kernel TUN device, subnet-route advertisement, or exit-node semantics — it only requires the node's own tailnet IP (which every tsnet node already has) and a local dial target, which is exactly this app's existing threat model (profile-scoped SOCKS5 proxy) extended by one more listener type. This is a genuine "reverse" NAT (port-forward) feature: instead of a local SOCKS5 client dialing out through the tailnet, a tailnet peer dials in to this node's IP and gets relayed to a LAN service — the mirror image of the app's current SOCKS5 direction.

---

## (D) Advertise Routes (subnet advertisement) — does userspace tsnet support functioning as a routed-through subnet router?

**Verdict: (3) fundamentally blocked for full parity with Tailscale's kernel-based subnet router, but with a real nuance — the *receiving* half is implemented in tsnet's netstack (`ProcessSubnets`), while the missing half is host-side kernel NAT/forwarding for arbitrary non-tailnet destinations exactly as described in (A).**

`ipn/prefs.go` defines the exact fields:

```go
// AdvertiseRoutes specifies CIDR prefixes to advertise into the
// Tailscale network as reachable through the current node.
AdvertiseRoutes []netip.Prefix
```
```go
// NoSNAT specifies whether to source NAT traffic going to
// destinations in AdvertiseRoutes. The default is to apply source
// NAT, which makes the traffic appear to come from the router
// machine rather than the peer's Tailscale IP.
// ...
// Linux-only.
NoSNAT bool
```
```go
// NoStatefulFiltering specifies whether to apply stateful filtering when
// advertising routes in AdvertiseRoutes...
// To allow inbound connections from advertised routes, both NoSNAT and
// NoStatefulFiltering must be true.
// ...
// Linux-only.
NoStatefulFiltering opt.Bool `json:",omitempty"`
```

These three fields, plus the surrounding comment block ("The following block of options only have an effect on Linux"), are explicit and unambiguous: SNAT and stateful filtering for advertised routes are Linux-kernel-netfilter features, not tsnet features. tsnet on any OS (macOS, Windows, or Linux-without-root) cannot apply kernel NAT rules because it never touches the kernel network stack at all — there's no TUN device for `iptables`/`pf`/`nftables` to act on.

That said, `wgengine/netstack.go` shows tsnet's netstack itself does have subnet-router logic wired in and unconditionally enabled for the default (non-TUN) tsnet config: `ns.ProcessSubnets = true`, `shouldProcessInbound` returning true for `!isLocal` destinations when `ProcessSubnets` is set, and `injectInbound`'s ping-reply special case explicitly commented as "if this is an echo request and we're a subnet router, handle pings ourselves." So a tsnet-embedded node **can** receive traffic from tailnet peers addressed to routes it advertises, and netstack will attempt to forward/dial it out to the target — entirely without a kernel TUN. What tsnet's public API does *not* give you is:
1. Any way for `AdvertiseRoutes` acceptance to also feed OS-level routing on the host (irrelevant to userspace mode since there is no such requirement for tsnet-internal forwarding, but relevant if the goal is (A)'s "arbitrary LAN device" case);
2. The SNAT/stateful-filtering guarantees `NoSNAT`/`NoStatefulFiltering` describe, since those are documented as Linux-only kernel behaviors with no userspace-netstack equivalent implemented (nothing in `wgengine/netstack.go` implements SNAT or stateful filtering logic — the file has no NAT/conntrack table, it just forwards);
3. Confidence that Tailscale's own control-plane and peer clients treat a userspace-advertised route identically to a `tailscaled` kernel-mode one under all conditions — this is unverified and not documented as a supported configuration anywhere in tsnet's docs or comments.

So (D) is buildable as a **best-effort, SNAT-less, unfiltered subnet route** (set `AdvertiseRoutes` via `EditPrefs`, same one-line pattern as (B), and rely on `ProcessSubnets`'s built-in forwarding) purely for reaching **tailnet peers or other subnet destinations that only need TCP/UDP connectivity from other authenticated tailnet nodes** — which is a real and shippable capability, just without the SNAT/security knobs Tailscale's Linux implementation offers, and without any guarantee of being a supported/tested tsnet use case. It is *not* capable of the (A) "arbitrary non-Tailscale LAN device" gateway scenario, which needs OS-level ingress capture that tsnet fundamentally cannot do in userspace mode regardless of `AdvertiseRoutes` settings.

---

## What's Actually Buildable Here — Ranked

| Rank | Option | Verdict | Why |
|---|---|---|---|
| 1 | **(C) Port forwarding** | **(1) possible now / (2) moderate app wiring** | `tsnet.Server.Listen`/`ListenPacket` are documented, stable public APIs that need zero kernel/TUN support — they listen on the node's own already-reachable tailnet IP. Fits `tsNode` interface and `runSocks5`-style relay pattern in `runner.go` almost exactly. Highest confidence, lowest risk, most self-contained. |
| 2 | **(D) Advertise routes (subnet router, no-frills)** | **(2) moderate for a best-effort version / (3) blocked for full parity** | `ProcessSubnets` is unconditionally true in tsnet's default (non-TUN) mode, so tsnet's netstack already forwards traffic to non-local advertised destinations for tailnet peers. Setting `AdvertiseRoutes` via `EditPrefs` is a one-line change reusing `configureExitNode`'s pattern. But `NoSNAT`/`NoStatefulFiltering` are explicitly Linux-kernel-only, so a tsnet-based subnet router has no SNAT and no stateful filtering — usable for lab/limited scenarios, not a like-for-like replacement for a real Tailscale subnet router. |
| 3 | **(B) Exit node advertisement (this app becomes others' exit node)** | **(3) fundamentally blocked for reliable general use** | Mechanically it's `AdvertiseRoutes = [0.0.0.0/0, ::/0]` (per `Prefs.SetAdvertiseExitNode`) — a strict superset of (D)'s problems, now needing to relay potentially high-volume, all-destination traffic for another device's *entire* internet usage through a Go-level netstack forwarder with no SNAT support and no upstream validation that userspace tsnet was ever designed/tested for this role. Preference-setting is trivial; the forwarding correctness and scale guarantees are not there. |
| 4 | **(A) Full LAN gateway for non-Tailscale devices** | **(3) fundamentally blocked as stated, unless scope is narrowed to "proxy" semantics** | Requires capturing raw packets from devices that have zero Tailscale/tsnet software and getting them into tsnet's WireGuard/netstack pipe — tsnet's public API has no ingress mechanism for that; it would require host-level kernel routing/NAT (root, iptables/pf) which contradicts this app's no-root/no-TUN design. The already-existing per-profile SOCKS5 listener can approximate a narrower "proxy gateway for proxy-aware LAN clients" use case with just a bind-address change, but that is not transparent NAT/gateway semantics for arbitrary LAN traffic. |

**Practical recommendation:** if the user wants to build something concrete out of this, (C) port forwarding is the only option with no fundamental architectural or upstream-support risk — it should be the first thing built, using `tsnet.Server.Listen`/`ListenPacket` plus a new `runPortForward` sibling to `runSocks5`. (D) as a best-effort, unfiltered subnet-route advertisement could be offered as an experimental/advanced feature with an explicit "no SNAT, tailnet peers only, not a substitute for a real Tailscale subnet router" disclaimer. (B) and (A) should not be promised as app features without first validating (B) against actual upstream tsnet exit-node support (none currently documented) and, for (A), accepting that it requires stepping outside tsnet/userspace entirely into privileged host networking — a different, much larger project than "add a NAT feature to this app."
