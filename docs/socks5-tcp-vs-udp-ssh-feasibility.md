# Follow-up feasibility: SSH via direct IP vs. SOCKS5, and UDP tailnet streaming

This is a follow-up to `docs/portable-tailscale-server-client-feasibility.md`, which already established
that this app embeds `tailscale.com/tsnet` and reaches peers via a local SOCKS5 proxy whose dialer calls
`node.Dial` → `tsnet.Server.Dial`. This doc answers a more specific question: can the user SSH to a tailnet
peer *without* SOCKS5 (using "IP directly"), and can UDP streams (game/media) pass through this app.

Short answer up front: **for SSH the premise is a false dichotomy** — SOCKS5 *is* the IP-direct TCP
mechanism this app offers, there is no separate one. **For UDP the gap is real** — it's a concrete
implementation gap in this app's `go-socks5` wiring, not a SOCKS5 protocol limitation.

---

## 1. SSH use case

**Code path:** `internal/tailscale/runner.go`

```go
type tsNode interface {
	Up(context.Context) (*ipnstate.Status, error)
	LocalClient() (localClient, error)
	Dial(context.Context, string, string) (net.Conn, error)   // line 100
	Close() error
}
```
(`runner.go:97-102`). There is **no `Listen` method** on `tsNode`, and `realNode` (the concrete
implementation backed by `*tsnet.Server`, `runner.go:109-131`) only wraps `Up`, `LocalClient`, `Dial`,
and `Close`. The only network-reachability primitive this app exposes to the outside world is:

```go
func (r *Runner) runSocks5(_ context.Context, node tsNode, listener net.Listener) error {
	server := socks5.NewServer(
		socks5.WithResolver(noResolve{}),
		socks5.WithDialAndRequest(func(ctx context.Context, network, _ string, request *socks5.Request) (net.Conn, error) {
			conn, err := node.Dial(ctx, network, request.RawDestAddr.String())   // runner.go:741
			...
		}),
		socks5.WithBufferPool(bufferpool.NewPool(256*1024)),
	)
	return server.Serve(listener)
}
```
(`runner.go:737-749`). This is a local, `127.0.0.1`-bound SOCKS5 CONNECT proxy. There is no HTTP CONNECT
proxy, no raw TCP forwarder, no "app-native" tunnel — SOCKS5 is the *entire* interaction surface for
reaching a peer's tailnet IP (confirmed already in the prior doc, §"No relay-only / lightweight mode").

**So the correct mechanism today is exactly what the user was trying to avoid**, because there is nothing
else to use instead: point an SSH client at the peer's tailnet IP with a `ProxyCommand`/`ProxyJump` that
speaks SOCKS5 to the app's local listener. Concretely, any of:

- OpenSSH ≥ 7.3's native support for a `socks5h://` proxy in `-J`/`ProxyJump`, or the classic form:
  ```
  ssh -o ProxyCommand="nc -X 5 -x 127.0.0.1:1080 %h %p" user@100.x.y.z
  ```
- `socat` with its SOCKS5 address type:
  ```
  ssh -o ProxyCommand="socat - SOCKS5:127.0.0.1:1080:%h:%p,socksport=1080" user@100.x.y.z
  ```
- OpenSSH ≥ 8.4's built-in `ProxyCommand="ssh -o ProxyJump=..." ` isn't relevant here, but
  `ssh -o ProxyCommand="ncat --proxy 127.0.0.1:1080 --proxy-type socks5 %h %p"` is another equivalent form.

**Is there any other, more direct IP-based mechanism instead of SOCKS5?** No. Confirmed by the interface
surface above: `tsNode` has no `Listen`, and `runSocks5` is the only caller of `node.Dial` in the entire
codebase (verified: `Dial(` only appears at `runner.go:100` (interface decl), `runner.go:122-124`
(`realNode.Dial` impl), and `runner.go:741` (the one call site inside `runSocks5`)). There is no second,
parallel "direct-connect" code path.

**Resolving the user's premise:** SSH is itself carried entirely over one TCP stream, so "connect to a
remote IP over TCP" and "go through the app's SOCKS5 CONNECT" are *the same operation*, not two
alternatives. SOCKS5 CONNECT in this app does nothing more than take a destination host:port and call
`node.Dial(ctx, "tcp", dest)` on the attacker's... err, the user's behalf (`runner.go:741`, with `network`
defaulting to `"tcp"` for CONNECT per go-socks5's `handleConnect`, see §3 below). There is no lighter-weight
"just give me the raw IP" path that skips this — SOCKS5-over-TCP through this app's listener genuinely is
the direct IP-based mechanism. The user's framing ("SOCKS5 vs. IP directly") is a false dichotomy for the
SSH case specifically.

---

## 2. Does this app expose any inbound listener on the tailnet (to *host* SSH, not just reach it)?

No. Grep across `internal/tailscale/runner.go` for `.Listen(`, `ListenSSH`, `ListenPacket`, `ListenTLS`
returns zero matches — the only `net.Listen` usage in the package is the *local* SOCKS5 listener
(`r.listen = net.Listen` at `runner.go:145`, bound to `127.0.0.1`, not the tailnet interface).

`tsNode` (`runner.go:97-102`) exposes `Up`, `LocalClient`, `Dial`, `Close` — no `Listen`. `realNode` wraps
only those four. So today this app can **originate** outbound connections into the tailnet (via `Dial`)
but cannot **accept** inbound connections addressed to its own tailnet IP.

For comparison, the underlying `tsnet.Server` (from `tailscale.com@v1.102.4/tsnet/tsnet.go`) *does* support
this:
```go
func (s *Server) Listen(network, addr string) (net.Listener, error)      // tsnet.go:1274
func (s *Server) ListenSSH(addr string) (net.Listener, error)             // tsnet.go:1292
func (s *Server) ListenPacket(network, addr string) (net.PacketConn, error) // tsnet.go:1312
```
`ListenSSH` in particular is purpose-built for exactly the "host an SSH server reachable at this node's own
tailnet IP" use case the user is describing, and it even yields `*tailssh.Session` connections carrying the
peer's Tailscale identity. The library capability is there; the app simply never calls it.

**What would be needed to add it (conceptually):**
1. Add `Listen(network, addr string) (net.Listener, error)` to the `tsNode` interface and to `realNode`
   (thin wrapper over `s.server.Listen`, same pattern as the existing `Dial` wrapper at `runner.go:122-124`).
2. Decide what "hosting" means for this GUI app — e.g. either (a) spawn a real SSH server (`golang.org/x/crypto/ssh`
   server-side) behind `node.Listen("tcp", ":22")`, or (b) use `tsnet.Server.ListenSSH` directly, which
   requires blank-importing `tailscale.com/feature/ssh` into the binary (per the tsnet.go doc comment at
   line ~1287) and wiring the resulting listener to a session handler.
3. Surface it as a new Runner method/UI feature (a toggle to "host SSH on this tailnet IP"), plus lifecycle
   management (start/stop alongside the existing `process` struct at `runner.go:134-142`) and its own event
   plumbing through `r.emit`.
This is a nontrivial new feature, not a config flag — nothing in the current code paths toward it.

---

## 3. UDP use case: why it cannot pass through today, precisely

**This is an implementation gap in how this app wires `go-socks5`, not a SOCKS5 protocol limitation.**
UDP ASSOCIATE is part of the SOCKS5 spec (RFC 1928 §4, command code `0x03`), and the vendored library
(`github.com/things-go/go-socks5 v0.1.3`, confirmed in `go.mod`) fully implements it — the gap is entirely
on this app's side of the wiring.

Evidence, tracing the exact code:

1. **The library supports UDP ASSOCIATE.** In
   `$GOMODCACHE/github.com/things-go/go-socks5@v0.1.3/ruleset.go`, `PermitCommand` has an
   `EnableAssociate` field, and `NewPermitAll()` returns `&PermitCommand{true, true, true}` — i.e.
   Connect, Bind, *and* Associate are all allowed by default. In `handle.go`, `handleRequest`'s dispatch
   switch (`handle.go:86-114`) explicitly has a `case statute.CommandAssociate: last = sf.handleAssociate`
   branch, and `handleAssociate` (`handle.go:181` onward) is a fully working UDP relay: it opens a
   `net.ListenUDP`, reads/parses SOCKS5 UDP datagrams via `statute.ParseDatagram`, and relays them via
   `dial(ctx, "udp", pk.DstAddr.String())`.

2. **`option.go` exposes `WithAssociateHandle`** (`option.go:121-123`) to let a caller override/customize
   UDP-associate behavior, exactly parallel to `WithConnectHandle`/`WithBindHandle`.

3. **This app never calls it.** `runSocks5` (`runner.go:737-749`) passes only three options to
   `socks5.NewServer`: `WithResolver(noResolve{})`, `WithDialAndRequest(...)`, `WithBufferPool(...)`. No
   `WithRule(...)` is passed, so the library's default (`rules: NewPermitAll()`, set in `NewServer` at
   `server.go:70-76`) applies — meaning UDP ASSOCIATE is *not blocked by policy*. No
   `WithAssociateHandle(...)` is passed either, meaning the library falls back to its own default
   `handleAssociate` implementation.

4. **The critical gap: `WithDialAndRequest`'s dialer is CONNECT-only.** In `handleConnect`
   (`handle.go:120-124`):
   ```go
   if sf.dialWithRequest != nil {
       target, err = sf.dialWithRequest(ctx, "tcp", request.DestAddr.String(), request)
   ```
   This is only reached from the `CommandConnect` branch of the dispatch switch. `handleAssociate`
   (`handle.go:183-186`) uses `sf.dial` (the plain `WithDial` callback), **not** `sf.dialWithRequest`:
   ```go
   dial := sf.dial
   if dial == nil {
       dial = func(_ context.Context, net_, addr string) (net.Conn, error) {
           return net.Dial(net_, addr) // nolint: noctx
       }
   }
   ```
   Since this app only calls `WithDialAndRequest` and never `WithDial`, `sf.dial` is `nil` inside the
   `Server`, so `handleAssociate`'s UDP relay falls back to plain `net.Dial(ctx, "udp", pk.DstAddr.String())`
   — a **raw OS-level UDP dial that never routes through `tsnet`/the tailnet at all**. It would try to
   reach `pk.DstAddr` over the regular network, not via `node.Dial`, so it would simply fail (or worse,
   silently misbehave) for a tailnet-only peer address, since `net.Dial` has no route to a `100.x.x.x`
   tailnet IP outside of `tsnet`'s userspace netstack.

So to summarize the exact gap: **UDP ASSOCIATE is technically reachable and "enabled" by the library's
defaults (no rule blocks it), but the UDP relay's actual outbound dial never goes through
`node.Dial`/`tsnet`, because this app only supplied a CONNECT-scoped dialer (`WithDialAndRequest`) and
never a general-purpose `WithDial` or `WithAssociateHandle`.** Today, if a client sent a SOCKS5 UDP
ASSOCIATE request to this app's listener, the app would (a) accept the ASSOCIATE per default rules, then
(b) attempt to relay UDP packets to the tailnet peer using the host's plain OS networking stack instead of
the tsnet WireGuard mesh — which cannot reach a tailnet IP, so the relay is nonfunctional for the intended
use case in practice, even though the protocol negotiation "succeeds."

**Is this a fundamental SOCKS5 limitation?** No — that would be false. UDP ASSOCIATE is standard SOCKS5
(RFC 1928), and the vendored library fully implements it (`handleAssociate`, `WithAssociateHandle`,
`PermitCommand.EnableAssociate`). **It is squarely an implementation gap specific to how this app wires
go-socks5**: it never passes a tsnet-aware `WithDial` (or a custom `WithAssociateHandle`) so that UDP
ASSOCIATE's data plane also routes through `node.Dial`.

---

## 4. Two ways to get UDP tailnet reachability without SOCKS5 at all

First, confirming the premise: `tsnet.Server.Dial` does accept `network="udp"`. From
`$GOMODCACHE/tailscale.com@v1.102.4/tsnet/tsnet.go:356`:
```go
func (s *Server) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	...
	return s.dialer.UserDial(ctx, network, address)
}
```
and `net/tsdial/tsdial.go`'s `UserDial` explicitly branches on UDP:
```go
if len(ipps) == 1 || strings.HasPrefix(network, "udp") {
	return d.dialOneUser(ctx, network, ipps[0])
}
```
So yes — the underlying tsnet library **can** dial UDP over the tailnet today; this app's own `node.Dial`
wrapper (`realNode.Dial`, `runner.go:122-124`) already forwards *any* network string transparently to
`s.server.Dial`, so `node.Dial(ctx, "udp", "100.x.x.x:5000")` would already work correctly if something
called it with `"udp"` — the SOCKS5 CONNECT path in `runSocks5` just never sends `"udp"` as the network
(CONNECT is TCP-only by protocol design, and the broken ASSOCIATE path doesn't route through `node.Dial`
at all, per §3).

### (a) Add a raw UDP port-forwarder/relay inside this app

Add a dedicated UDP listener on `127.0.0.1:<port>` whose datagrams are relayed via
`node.Dial(ctx, "udp", peerAddr)`. Rough shape, following the existing `process`/`Runner` patterns in
`runner.go`:

```go
func (r *Runner) runUDPForward(ctx context.Context, node tsNode, pc net.PacketConn, peerAddr string) error {
	conn, err := node.Dial(ctx, "udp", peerAddr) // e.g. "100.x.x.x:5000"
	if err != nil {
		return err
	}
	defer conn.Close()

	buf := make([]byte, 65535)
	var lastClient net.Addr
	go func() { // tailnet -> local client
		b := make([]byte, 65535)
		for {
			n, err := conn.Read(b)
			if err != nil { return }
			if lastClient != nil {
				pc.WriteTo(b[:n], lastClient)
			}
		}
	}()
	for { // local client -> tailnet
		n, addr, err := pc.ReadFrom(buf)
		if err != nil { return err }
		lastClient = addr
		if _, err := conn.Write(buf[:n]); err != nil { return err }
	}
}
```
This would need its own listener lifecycle wired into `Runner` (parallel to `r.listen`/`serveSocks5`), a
UI control to configure the target peer's tailnet `ip:port`, and per-client session tracking if more than
one local UDP client needs to be supported concurrently (the naive single-`lastClient` version above only
handles one local peer at a time, similar in spirit to a simple `socat UDP-LISTEN:.. UDP:..` relay but
routed through `node.Dial` instead of the OS network stack). This directly reuses the existing
`tsNode.Dial` abstraction that's already proven in `runSocks5`.

### (b) Use this app only to bootstrap tsnet auth/state, then use a separate tool for UDP

Since `tsnet` persists its node state under a state directory (`defaultStateDir()`, `runner.go:151-157`,
under the OS user config dir), a user could run this app once to authenticate/join the tailnet, then run a
*separate*, purpose-built program against the **same state directory** for the UDP need:

- The real `tailscale` CLI (if `tailscaled` is also installed) pointed at the same tailnet identity — not
  really applicable here since this app deliberately avoids `tailscaled`/CLI dependency (per the prior
  feasibility doc), so this sub-option is weaker unless the user is willing to install full Tailscale
  separately.
- A small custom Go program using `tsnet` directly, pointed at the same `Dir` (state directory) so it
  reuses the already-authenticated node identity without a fresh device registration:
  ```go
  srv := &tsnet.Server{
      Dir:      sameStateDirAsWireproxyGUI, // reuse existing authenticated node
      Hostname: "udp-relay",
  }
  defer srv.Close()
  conn, err := srv.Dial(ctx, "udp", "100.x.x.x:5000")
  // relay to/from a local UDP socket, same pattern as (a) but as a standalone binary
  ```
  This mirrors exactly what wireproxy-gui's `realNode.Dial` already does, just in an independently
  compiled/run program.

**Which is more realistic?** Given this is described as a closed-source GUI app the user can run but not
necessarily patch: **(b) is more realistic for someone without source-modification rights** — they only
need the state directory, not to touch the app's code, and a ~30-line standalone Go program using `tsnet`
directly is very achievable. **(a) is the better long-term fix** if the user (or someone) can actually fork
or contribute to the app, since it's a natural, contained extension of code that already exists
(`node.Dial` + a new listener, following the exact pattern `runSocks5` already establishes) and gives all
users of the app UDP support without needing a second tool or Go toolchain. If the user has no way to
modify wireproxy-gui's source, (b) is the only currently-actionable path.

---

## 5. Corrected mental model

The user's framing — "SOCKS5 vs. IP directly" — is not the right axis. Every reachability path this app
offers (today or hypothetically) still targets a tailnet **IP address**; SOCKS5 is just the *transport
mechanism* by which a local TCP client hands the app a destination IP:port to dial via `tsnet`. There is no
alternate "more direct" IP path hiding behind SOCKS5 — SOCKS5-CONNECT-through-this-app *is* the IP-direct
TCP mechanism, per §1.

The real, precise distinction is:

| | Works today via this app | Why |
|---|---|---|
| **TCP** (SSH, HTTP, etc.) via this app's SOCKS5 listener | ✅ Yes | `runSocks5` → `WithDialAndRequest` → `node.Dial(ctx, "tcp", dest)` → `tsnet.Server.Dial` → `UserDial`. Fully wired end to end (`runner.go:737-749`). |
| **UDP** via this app's SOCKS5 listener | ❌ No | SOCKS5 ASSOCIATE is accepted (default `NewPermitAll` rules allow it), but its data-plane dial falls back to raw `net.Dial("udp", ...)` — because this app never supplied `WithDial`/`WithAssociateHandle` — so it never reaches the tailnet (`go-socks5@v0.1.3/handle.go:183-186` vs. `runner.go:737-749`). |
| **Inbound listen on this app's own tailnet IP** (host SSH/other server) | ❌ No | `tsNode` has no `Listen` method at all; only `Up`/`LocalClient`/`Dial`/`Close` are wired (`runner.go:97-102`, `109-131`). The underlying `tsnet.Server.Listen`/`ListenSSH` exist but are unused. |
| **UDP dial capability at the tsnet library layer** (not exposed by this app) | N/A (library-level, not app-level) | `tsnet.Server.Dial` → `tsdial.Dialer.UserDial` explicitly supports `network` strings starting with `"udp"` (`tsdial.go`). The capability exists in the dependency; the app simply never invokes it with `"udp"`. |

So: **state it precisely as TCP-via-this-app's-SOCKS5 (works today) vs. UDP-via-this-app (does not work
today, needs a new feature)** — not SOCKS5-vs-IP-direct. SSH already gets the best available mechanism
(SOCKS5 CONNECT = direct IP TCP dial via `tsnet`). UDP streaming needs either a new feature in this app
(§4a) or a separate small `tsnet`-based tool reusing the same authenticated state directory (§4b).

---

## Verdict

**SSH question:** There is no non-SOCKS5, more-direct IP mechanism to reach a tailnet peer through this
app — SOCKS5 CONNECT (`runSocks5`, `runner.go:737-749`, backed by `node.Dial(ctx, "tcp", …)` →
`tsnet.Server.Dial`) *is* the direct-IP-over-TCP mechanism. The correct, currently-working way to SSH is an
SSH client using `ProxyCommand`/native SOCKS5-proxy support (`nc -X 5`, `socat SOCKS5:...`, or OpenSSH's
`socks5h://` ProxyJump support) pointed at the app's local SOCKS5 listener, targeting the peer's tailnet
IP:22. The user's "not SOCKS5, IP directly" framing is a false dichotomy for SSH specifically, since SSH is
inherently TCP and SOCKS5-through-this-app is how TCP-to-a-tailnet-IP is achieved here. Separately, this app
also does **not** expose a way to *host* an inbound SSH (or any) server reachable at its own tailnet IP —
`tsNode`/`runner.go` never call `tsnet.Server.Listen`/`ListenSSH`, even though the underlying `tsnet`
library supports both; adding that would be a genuinely new feature (§2).

**UDP question:** UDP media/game streaming cannot currently pass through this app. This is **not** a SOCKS5
protocol limitation (UDP ASSOCIATE is standard SOCKS5 and is fully implemented in the vendored
`go-socks5@v0.1.3` library, including default-allowed rules and a working relay in `handleAssociate`). It
**is** an implementation gap specific to this app: `runSocks5` only supplies `WithDialAndRequest` (a
CONNECT-only dialer per `handle.go`'s dispatch), never `WithDial` or `WithAssociateHandle`, so any UDP
ASSOCIATE session falls back to the library's default plain `net.Dial("udp", …)`, which never routes
through `tsnet` and cannot reach a tailnet peer. Achieving UDP tailnet reachability requires either adding
a dedicated UDP relay to this app wired to `node.Dial(ctx, "udp", …)` (§4a, a real but contained code
change, since `tsnet.Server.Dial` already supports UDP via `UserDial`), or bypassing this app entirely for
the UDP need by using its authenticated state directory with a separate small `tsnet`-based program (§4b,
more realistic without source access to the app).
