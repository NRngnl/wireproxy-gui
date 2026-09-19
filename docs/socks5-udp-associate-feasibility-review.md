# Review: SSH-by-IP and UDP-over-Tailnet via this App's SOCKS5 Proxy

**Question under review:** *"I mean not using socks5 to connect to remote ts machine ssh using this app, using ip directly, or udp to connect to remote pc's stream ... since using socks5 most likely will be using tcp"*

This is an independent code-grounded review. It supersedes the prior doc (`docs/portable-tailscale-server-client-feasibility.md`) only on the narrower UDP/SSH-transport question; that doc's IP-reachability-via-SOCKS5-CONNECT conclusion is not disputed.

---

## 1. Is "SSH by IP, not via SOCKS5" even a coherent alternative?

**Blunt answer: No, not with this app. Not a third option — a false dichotomy.**

SSH is TCP-only (RFC 4253, transport layer runs over a reliable, ordered byte stream). There is no "raw UDP" or "direct IP, no proxy" SSH mode. Given that this app **creates no OS-level network interface at all** — no TUN/TAP device, no routing table entries, no `100.x.x.x` address bound to any OS network adapter — there is no way for the OS-level `ssh` binary, or any other unmodified TCP tool, to "just" address a peer's tailnet IP directly through this app. The tsnet node lives entirely in-process as a userspace netstack; the *only* egress this app exposes to the outside world is the local SOCKS5 listener (`internal/tailscale/runner.go:737-749`, `runSocks5`).

So the real options are exactly two, and only two:

- **(a) Go through this app's SOCKS5 proxy as a `ProxyCommand`** — e.g. `ssh -o ProxyCommand="nc -X 5 -x 127.0.0.1:<port> %h %p" user@100.x.x.x`, or `ssh -o ProxyCommand="connect -S 127.0.0.1:<port> %h %p"`. This works today because SOCKS5 CONNECT is TCP, SSH is TCP, and `runSocks5`'s `WithDialAndRequest` callback routes the CONNECT through `node.Dial(ctx, "tcp", ...)` into the tsnet netstack (confirmed at `runner.go:740-746`). This is the only path this GUI app supports for SSH.
- **(b) Install the real Tailscale client** and use MagicDNS + the OS-level routed tailnet interface it creates (a genuine TUN/utun device with kernel routes to `100.64.0.0/10` and peer subnets). Then `ssh user@100.x.x.x` (or `ssh user@hostname` via MagicDNS) works completely unmodified, because the OS itself now has a route to the tailnet — no proxy, no `ProxyCommand`, nothing app-specific. This is explicitly the mechanism the "no-install, portable" design of this app was built to avoid (see `docs/tailscale-integration.md`'s "Rejected Alternative: External `tailscaled`" section) and it is unrelated to this GUI app's own capabilities.

There is no third "direct IP, no proxy, no install" path with this app as built. Any claim that IP-only SSH connectivity is possible without SOCKS5 and without installing real Tailscale is incorrect for this app's current architecture.

---

## 2. Would UDP ASSOCIATE work through this app's SOCKS5 server if a client tried it?

Walking the go-socks5 v0.1.3 library internals (`$GOMODCACHE/github.com/things-go/go-socks5@v0.1.3/`) against exactly what `runSocks5` wires up:

**Step 1 — protocol negotiation layer (RuleSet).** `runSocks5` never calls `socks5.WithRule(...)`. `server.go`'s `NewServer` defaults `rules: NewPermitAll()` when no rule option is passed (`server.go`, struct literal in `NewServer`). `ruleset.go`'s `NewPermitAll()` returns `&PermitCommand{true, true, true}` — i.e. `EnableConnect: true, EnableBind: true, EnableAssociate: true`. `PermitCommand.Allow` switches on `req.Command` and for `statute.CommandAssociate` returns `p.EnableAssociate` → `true`.

**Conclusion for step 1: yes, the default RuleSet permits `CommandAssociate`.** A client sending a UDP ASSOCIATE request to this app's SOCKS5 port will *not* be rejected at the rule-check stage (`handle.go`'s `handleRequest`, the `sf.rules.Allow(ctx, req)` check, passes for Associate).

**Step 2 — actual relay dial path.** `handle.go`'s `handleAssociate` does:

```go
dial := sf.dial
if dial == nil {
    dial = func(_ context.Context, net_, addr string) (net.Conn, error) {
        return net.Dial(net_, addr) // nolint: noctx
    }
}
```

`sf.dial` is populated **only** by `socks5.WithDial(...)`. `sf.dialWithRequest` (populated by `WithDialAndRequest`) is a *separate field* consulted only by `handleConnect` (used for `CommandConnect` / TCP), never by `handleAssociate`. This app's `runSocks5` calls `WithDialAndRequest` exclusively and never calls `WithDial`, so `sf.dial` is `nil` at runtime.

**Conclusion for step 2: `handleAssociate` falls back to raw `net.Dial(net_, addr)` — the plain OS-level dialer, completely bypassing `node.Dial` / the tsnet netstack.** Any UDP datagram associate-relayed through this SOCKS5 server would attempt to leave via the machine's normal OS network stack, not the tailnet. It would try to reach `pk.DstAddr` (the target the client specified) using ordinary UDP sockets on the host's real interfaces — which for a tailnet peer's `100.x.x.x` address would simply fail (that address isn't routable outside the tailnet's own userspace netstack, and there's no OS route to it since no TUN device exists).

**Plain statement of the conclusion: No — UDP relayed through this app's SOCKS5 server would never actually traverse the tailnet.** Even though the protocol-negotiation layer accepts the ASSOCIATE command (so the client wouldn't get an immediate "command not supported" rejection), the data-plane dial silently uses `net.Dial`, not `node.Dial`, so datagrams destined for a `100.x.x.x` peer would fail to route (most likely `net.Dial` returns a "no route to host" / unreachable style error the first time a target address is dialed, since that address only exists inside tsnet's private netstack). This is a genuine, exploitable-looking gap: the server *appears* to support UDP ASSOCIATE (it won't SOCKS5-reject it), but it is functionally broken for tailnet destinations because it's wired to the wrong dial function.

---

## 3. Root cause — which of (a)/(b)/(c) is correct?

- **(a) "SOCKS5 protocol itself lacks UDP support" — FALSE.** UDP ASSOCIATE is a first-class SOCKS5 command (RFC 1928 §4, command code `0x03`), and `go-socks5` fully implements it (`handle.go`'s `handleAssociate`, NAT-style UDP relay with per-flow `net.Conn` tracking via `sync.Map`).
- **(c) "fundamental tsnet/WireGuard limitation" — FALSE.** WireGuard's own transport is UDP, and `tsnet.Server.Dial` genuinely supports `network="udp"`: it forwards to `dialer.UserDial(ctx, network, address)` (`tsnet.go:356-363`), which for `strings.HasPrefix(network, "udp")` calls `d.dialOneUser` → `d.NetstackDialUDP(ctx, ipp)` when the destination is a Tailscale-routed IP (`tsdial.go:567-596`). tsnet's own dialer is fully UDP-capable over the tailnet's userspace netstack.
- **(b) "go-socks5's default config/wiring in this specific app not connecting UDP ASSOCIATE to the tsnet dialer" — TRUE, this is the actual root cause.** Confirmed directly in code: `runner.go`'s `runSocks5` only ever calls `WithDialAndRequest` (consumed by `handleConnect`/TCP path only) and never `WithDial` or `WithAssociateHandle`. `handleAssociate`'s fallback (`sf.dial == nil` → plain `net.Dial`) is therefore always the live code path for any UDP ASSOCIATE request this server receives.

**Single correct root cause:** This app's `runSocks5` wiring of `go-socks5` never connects the library's UDP-ASSOCIATE relay path to the tsnet-backed dialer (`node.Dial`). It's a wiring/configuration gap in this specific app, not a protocol limitation and not a tsnet capability limitation.

---

## 4. Realistic engineering path to add UDP relay support to this app

Two viable shapes, in `internal/tailscale/runner.go`'s `runSocks5`:

**Option A — minimal, use the library's built-in relay, just fix the dial function.**
Add `socks5.WithDial(func(ctx context.Context, network, addr string) (net.Conn, error) { return node.Dial(ctx, network, addr) })` alongside the existing `WithDialAndRequest`. Since `handleConnect` prefers `dialWithRequest` when set and only falls back to `dial` otherwise, and `handleAssociate` uses `dial` exclusively, adding `WithDial` fixes the ASSOCIATE path without disturbing the existing CONNECT/TCP path. This is the smallest possible change — likely a 3-5 line diff.

- Pitfall: `handleAssociate`'s relay binds its own UDP listen socket via `net.ListenUDP` on the *local* machine (for the client to send datagrams to) — that part is unaffected and fine. Only the *outbound* per-flow dial (`dial(ctx, "udp", pk.DstAddr.String())`) needs to go through `node.Dial`. With `WithDial` set, it will.
- Pitfall: `tsnet.Server.Dial(ctx, "udp", addr)` requires `addr` to already be resolvable/reachable inside the tailnet (an IP, since `WithResolver(noResolve{})` disables the app's own DNS resolution — same constraint as the existing TCP CONNECT path, not a new problem).
- Pitfall: no additional address-family plumbing needed; `dialOneUser`'s netstack UDP path (`NetstackDialUDP`) already handles this internally in tsnet.

**Option B — full custom relay via `WithAssociateHandle`, for finer control** (e.g. custom session limits, logging, or reusing the app's `socksReplyConn` wrapper).
Replace the library's default `handleAssociate` entirely with a bespoke handler passed to `socks5.WithAssociateHandle(...)`. This requires re-implementing: (1) binding a local UDP listen socket for the client, (2) parsing/building SOCKS5 UDP datagram headers (`statute.ParseDatagram` / `pk.Header()`), (3) per-flow NAT-style session tracking keyed by `(srcAddr, dstAddr)` since UDP is connectionless and there's no "one relay conn per client" model, (4) idle-session expiry/cleanup, and (5) closing everything when the parent TCP control connection (the ASSOCIATE request's `Reader`) closes or errors — this signals the client is done. This is considerably more code and duplicates library logic; only worth it if Option A's dial-fn swap isn't flexible enough.

**Recommendation: Option A.** It is the smallest, lowest-risk change, reuses all of go-socks5's already-tested NAT/session-tracking code, and directly closes the root-cause gap identified in §3.

Either option still leaves real complexity inherent to UDP-over-tailnet relaying, independent of this app:
- UDP is connectionless — "session" lifetime and cleanup are heuristic (the library already does idle cleanup via per-flow goroutines that exit on read error/EOF).
- A misbehaving/malicious local SOCKS5 client could open many concurrent UDP "sessions" (one `net.Conn`-like tsnet dial per unique dest) with no cap in the library — worth adding a session limit if exposing this beyond localhost-trusted use.
- SOCKS5 UDP ASSOCIATE is bound to a parent TCP control connection per the RFC; go-socks5's implementation already ties relay lifetime to that TCP conn's `Reader`, which is correct, but this constrains which client libraries can drive it well (not all HTTP/SOCKS5 UDP client wrappers implement the control-connection lifetime handshake correctly — this is a client-side compatibility risk, not something fixable in this app).

---

## 5. Realistic alternatives today, for a user who cannot modify this app's source

Ranked by realism/effort:

1. **SSH via `ProxyCommand` through this app's existing SOCKS5 proxy — works today, zero app changes needed.** Realistic and immediate for the *SSH* half of the question specifically, since SSH is TCP and CONNECT already works. Does not address UDP streaming at all.
2. **Install the real Tailscale client.** Highest realism for full functionality (MagicDNS, UDP-over-tailnet "just works" since the OS gets a real routed interface), but it directly defeats the stated no-install/portable goal of using this GUI app in the first place. If the user's actual priority is "UDP streaming to a tailnet peer must work, portability is secondary," this is the pragmatic answer today.
3. **Run a separate small Go program that directly imports `tsnet` and calls `Dial(ctx, "udp", addr)` (or a purpose-built relay/proxy) outside this GUI app.** Medium effort (need to write and maintain a second small Go binary/tool), but stays "no official Tailscale client install" and would genuinely work — confirmed above that `tsnet.Server.Dial` supports `network="udp"` end-to-end through the userspace netstack. This is essentially "build Option A yourself, standalone, without touching this GUI app's codebase." Most realistic non-source-modifying path that preserves the no-install goal.
4. **Wait for/request an upstream fix in this app (Option A above) and use the app once patched.** Not "today," but the cheapest actual fix (~5 lines) — worth flagging to the app maintainer given how small the change is relative to impact.

For "cannot modify this app's source" specifically, ranked purely by effort-to-get-working-today: **(1) for SSH now, (2) for full-fidelity UDP now if install is acceptable, (3) for a no-install UDP path with moderate build effort.**

---

## Verdict

- **SSH by IP "directly" without SOCKS5 and without installing real Tailscale is not a coherent option with this app.** It creates no OS-level network interface; the *only* way this app can carry SSH traffic is TCP CONNECT through its SOCKS5 proxy (works today via `ProxyCommand`). The only alternative that avoids SOCKS5 entirely is installing the real Tailscale client, which is a different tool with a different (non-portable) trust model.
- **UDP ASSOCIATE through this app's SOCKS5 proxy does not work today, and it's a clean, single-cause bug/gap, not a fundamental limitation.** The default `NewPermitAll()` RuleSet lets a UDP ASSOCIATE request past the protocol-negotiation check, creating a false impression of support — but `handleAssociate`'s actual relay dial falls back to plain `net.Dial` because this app only wires `WithDialAndRequest` (TCP-CONNECT-only) and never `WithDial`/`WithAssociateHandle`. Any UDP relayed this way would attempt to leave via the host's ordinary network stack and fail to reach tailnet-only addresses. **It would never traverse the tailnet as currently built.**
- **Neither SOCKS5-the-protocol nor tsnet-the-library nor WireGuard-the-transport is the blocker.** SOCKS5 UDP ASSOCIATE is real and implemented in the vendored library; `tsnet.Server.Dial` genuinely supports `network="udp"` over the tailnet netstack. The gap is entirely in how this app's `runSocks5` wires the two together — it simply never connects them for the UDP path.
- **The fix is small** (add `socks5.WithDial` backed by `node.Dial`, ~5 lines) but not present today, so as of the current code, UDP streaming to a remote tailnet peer via this app does not work, full stop.
- **For users today without modifying source:** SSH-over-SOCKS5-ProxyCommand works now; full UDP support requires either installing the real Tailscale client (defeats portability) or standing up a separate small `tsnet`-based relay program outside this GUI app.
