# Implementation Plan: Inbound Port Forwarding for Tailscale Profiles

Status: planning only — no code changes have been made. This plan is derived
from `docs/portable-tailscale-server-client-feasibility.md` (§12–13) and
`docs/nat-like-features-feasibility.md` (§C), which established feasibility.
This document does not re-litigate feasibility; it only decides the concrete
shape of the implementation and splits it into independently assignable work
packages.

## Feature summary and scope boundary

The app listens on this node's own tailnet IP (via `tsnet.Server.Listen` /
`ListenPacket`) at a user-configured port, and relays each accepted
connection/datagram to a fixed local LAN target address using a plain
`net.Dial`/`net.Dialer` on the host's real network stack (not `node.Dial`,
since the LAN target is not itself a tailnet peer). This lets a tailnet peer
dial `<this-node-tailnet-ip>:8443` and reach `192.168.1.50:80` on the local
LAN, per profile, for as long as that profile's Tailscale connection is
running.

**In scope for v1:**
- TCP port forwarding (accept loop + bidirectional `io.Copy` relay), modeled
  directly on the existing SOCKS5 CONNECT relay path.
- UDP port forwarding (`tsnet.Server.ListenPacket` + a per-source-address
  NAT-style session map), modeled on the existing SOCKS5 UDP ASSOCIATE relay
  (`runSocks5`'s `WithDial` wiring) since both need connectionless,
  session-tracked relaying. UDP is included because the existing codebase
  already solved the harder "session tracking over a connectionless
  protocol" problem once (§11 of the feasibility doc) and the pattern is
  directly reusable; excluding it would not meaningfully reduce work-package
  count or risk.
- Per-profile list of forward rules: `{ListenPort int, Protocol string,
  TargetAddr string}`.
- Persistence (profile JSON) and validation (port range, duplicate listen
  ports within a profile, target address format).
- A minimal (not polished) GUI surface to add/edit/remove forward rules,
  because the plan text explicitly requires *some* UI and the feature is
  unusable without one — a fully bounded "table" widget with add/remove
  buttons and three text entries per row is small enough to fit in one work
  package without blocking the runner/persistence work.

**Explicitly deferred (not in v1):**
- Editing/reloading forward rules on a running profile without a full
  restart (v1 requires restart — forward rule changes flow through the
  existing `RuntimeConfigChanged` restart-required path, not the live-update
  `ExitNodeConfigChanged` path).
- Rate limiting, connection count caps, or per-rule enable/disable toggles
  beyond deleting the rule.
- IPv6-specific target address handling beyond what `net.Dial`/`net.JoinHostPort`
  already provide.
- Any UI polish (drag-to-reorder, inline validation styling, etc.) beyond
  functional add/remove/edit and error surfacing consistent with existing
  form validation.
- Advertising forwarded ports anywhere in Tailscale ACLs/services (`tailcfg`
  `ServiceName` / `ListenService`) — v1 uses plain `tsnet.Server.Listen`/
  `ListenPacket` bound to the node's own tailnet IP, not the newer
  `ListenService` service-registration mechanism.

## Resolved constraint: TailscaleConfig comparability

**Directive (binding on every work package):** `profile.FieldsChanged`,
`profile.RuntimeConfigChanged`/`tailscaleRestartConfigChanged`, and
`profile.ExitNodeConfigChanged` currently rely on Go struct `==`/`!=`
equality over `TailscaleConfig` (a `Profile` value) and over `TailscaleConfig`
directly. Adding a `[]PortForward` slice field makes `TailscaleConfig`
non-comparable, which breaks compilation of `!=` on both `Profile` and
`TailscaleConfig` wherever it appears (`profile.go` lines ~227/262/314/363
region — see exact call sites below).

**Decision: option (b) — refactor away from struct `==`/`!=` and use
explicit field-by-field comparison plus `slices.Equal` for the new slice
field.** This is preferred over a fixed-size array (inflexible, silently
truncates or wastes memory for a user-facing "add as many rules as you like"
list) and is a small, mechanical, low-risk change confined to
`internal/profile/profile.go` and its non-domain callers
(`internal/profilejson/profilejson.go`, `internal/ui/app.go`) that currently
use `TailscaleConfig{}`/`Profile{}` zero-value comparison.

Concretely:
- `FieldsChanged` stops doing `before.TailscaleConfig != after.TailscaleConfig`
  and instead calls a new unexported helper `tailscaleConfigEqual(before,
  after TailscaleConfig) bool` that compares every field explicitly,
  including `slices.Equal(before.PortForwards, after.PortForwards)` (using
  `"slices"` from the standard library; `PortForward` is a small comparable
  struct so `slices.Equal` works directly with `==` per element — no custom
  `Equal` method needed).
- `tailscaleRestartConfigChanged` is extended to also compare
  `PortForwards` via the same helper's building blocks (port-forward rule
  changes require a listener restart, so they belong in the *restart*
  bucket, not the live-update `ExitNodeConfigChanged` bucket).
- Every other site that currently does `x.TailscaleConfig != (profile.TailscaleConfig{})`
  or `x.TailscaleConfig == profile.TailscaleConfig{}` for a *zero-value
  check* (not a `before`/`after` diff) is replaced with a new exported method
  `func (c TailscaleConfig) IsZero() bool` that also field-compares
  (`len(c.PortForwards) == 0` plus the same explicit field checks). Call
  sites to update, found by inspection:
  - `internal/profilejson/profilejson.go:227` — `if item.IsTailscale() || item.TailscaleConfig != (profile.TailscaleConfig{})`
  - `internal/profilejson/profilejson.go:314` — `item.IsTailscale() && (item.TailscaleConfig != profile.TailscaleConfig{})`
  - `internal/ui/app.go:1428` — `existing.TailscaleConfig = profile.TailscaleConfig{}` (assignment, not comparison — unaffected, left as-is)
- No other package compares `Profile` values with `==`/`!=` (verified by
  repo-wide grep); `Profile` itself does not need an `Equal`/`IsZero` helper
  for this feature.

This directive must be implemented as part of Work Package 1 (below) before
any other package's tests can pass, since it is a compile-breaking change
shared by every package that touches `profile.TailscaleConfig`.

## New types (decided once; all work packages must use these exact names/shapes)

In `internal/profile/profile.go`:

```go
// PortForwardProtocol identifies the transport protocol of a forward rule.
type PortForwardProtocol string

const (
	PortForwardTCP PortForwardProtocol = "tcp"
	PortForwardUDP PortForwardProtocol = "udp"
)

// PortForward describes one inbound-tailnet-IP-to-LAN relay rule: a
// Tailscale peer dialing this node's tailnet IP at ListenPort is relayed to
// TargetAddr (host:port) on the local network, using a plain net.Dial (not
// node.Dial, since the target is not itself a tailnet peer).
type PortForward struct {
	ListenPort int
	Protocol   PortForwardProtocol
	TargetAddr string
}
```

New errors in the existing `var (...)` block in `internal/profile/profile.go`:

```go
ErrPortForwardPortOutOfRange  = errors.New("port forward listen port must be between 1 and 65535")
ErrPortForwardProtocolInvalid = errors.New("port forward protocol must be tcp or udp")
ErrPortForwardTargetRequired  = errors.New("port forward target address is required")
ErrPortForwardTargetInvalid   = errors.New("port forward target address must be host:port")
ErrPortForwardDuplicatePort   = errors.New("duplicate port forward listen port")
```

`TailscaleConfig` gains one new field (append at the end of the struct to
minimize diff noise):

```go
type TailscaleConfig struct {
	Hostname               string
	AuthKey                string
	Authenticated          bool
	ControlURL             string
	ExitNode               string
	AutoExitNode           bool
	ExitNodeAllowLANAccess bool
	Ephemeral              bool
	PortForwards           []PortForward
}
```

In `internal/tailscale/runner.go`, the `tsNode` interface gains:

```go
type tsNode interface {
	Up(context.Context) (*ipnstate.Status, error)
	LocalClient() (localClient, error)
	Dial(context.Context, string, string) (net.Conn, error)
	Listen(network, addr string) (net.Listener, error)
	ListenPacket(network, addr string) (net.PacketConn, error)
	Close() error
}
```

`realNode` gets thin wrappers:

```go
func (n *realNode) Listen(network, addr string) (net.Listener, error) {
	return n.server.Listen(network, addr)
}

func (n *realNode) ListenPacket(network, addr string) (net.PacketConn, error) {
	return n.server.ListenPacket(network, addr)
}
```

`fakeTSNode` in `internal/tailscale/runner_test.go` gets matching optional
fields so existing/new tests can stub them without every existing test
needing to set them (nil func fields fall back to returning
`errUnexpectedDial`-style sentinel errors, following the existing pattern for
`dialFunc`):

```go
type fakeTSNode struct {
	// ... existing fields unchanged ...
	listenFunc       func(network, addr string) (net.Listener, error)
	listenPacketFunc func(network, addr string) (net.PacketConn, error)
}

func (n *fakeTSNode) Listen(network, addr string) (net.Listener, error) {
	if n.listenFunc != nil {
		return n.listenFunc(network, addr)
	}
	return nil, errUnexpectedListen
}

func (n *fakeTSNode) ListenPacket(network, addr string) (net.PacketConn, error) {
	if n.listenPacketFunc != nil {
		return n.listenPacketFunc(network, addr)
	}
	return nil, errUnexpectedListenPacket
}
```

with two new sentinel errors alongside the existing `errUnexpectedDial`:
`errUnexpectedListen`, `errUnexpectedListenPacket`.

---

## Work Package 1 — `profile` domain model, validation, and comparability refactor

**Files:** `internal/profile/profile.go`, `internal/profile/profile_test.go`.

**Must land first** — every other package depends on the new
`PortForward`/`PortForwardProtocol` types and the `TailscaleConfig` field
compiling and behaving correctly. No other package may edit these two files.

### Changes

1. Add `PortForwardProtocol`, its two constants, `PortForward`, and the five
   new `Err*` sentinels exactly as specified above.
2. Add `PortForwards []PortForward` field to `TailscaleConfig`.
3. Add `func (c *TailscaleConfig) Normalize()` extension: trim
   `TargetAddr` whitespace, lower-case `Protocol`, and default an empty
   `Protocol` to `PortForwardTCP` for each rule in `PortForwards` (old
   behavior: `Normalize` does not touch `PortForwards` at all since the field
   doesn't exist yet; new behavior: iterates and normalizes each rule
   in-place, matching the existing trim-and-default style already used for
   `Hostname`/`AuthKey`/etc. in the same method).
4. Add `func (c TailscaleConfig) Validate() error` extension: old behavior
   only checks the exit-node exclusivity rule; new behavior additionally
   validates each `PortForwards` entry (port in `1..65535` via
   `ErrPortForwardPortOutOfRange`, protocol is `tcp` or `udp` via
   `ErrPortForwardProtocolInvalid`, `TargetAddr` non-empty via
   `ErrPortForwardTargetRequired` and parses with `net.SplitHostPort` via
   `ErrPortForwardTargetInvalid`), and checks for duplicate `ListenPort`
   values within the same profile's `PortForwards` slice via
   `ErrPortForwardDuplicatePort` (compare by port number only, regardless of
   protocol — a TCP and UDP rule cannot share a listen port either, since
   `tsnet.Server.Listen`/`ListenPacket` bind independently but the UI/config
   model treats "listen port" as the single user-facing key; if you want the
   plan's tests to allow same port with different protocol, do not — keep it
   simple: one port per profile is used by at most one forward rule).
   Aggregate all `PortForward` validation errors with the same
   `errors.Join` pattern already used in `Profile.Validate`.
5. Add `func (c TailscaleConfig) IsZero() bool` performing an explicit
   field-by-field comparison (including `len(c.PortForwards) == 0`) that
   replaces every current bare `== (TailscaleConfig{})` / `!=
   (TailscaleConfig{})` zero-value check. This is a new exported method;
   nothing in `profile.go` itself currently does a zero-value check on
   `TailscaleConfig`, so this method exists purely for
   `profilejson`/`ui` (Work Packages 3/4) to call — do not wire those call
   sites in this package, just provide the method.
6. Add unexported `func tailscaleConfigEqual(before, after TailscaleConfig) bool`
   comparing every field explicitly (`Hostname`, `AuthKey`, `Authenticated`,
   `ControlURL`, `ExitNode`, `AutoExitNode`, `ExitNodeAllowLANAccess`,
   `Ephemeral`, and `slices.Equal(before.PortForwards, after.PortForwards)`
   using the `"slices"` stdlib package — add it to imports).
7. Modify `FieldsChanged` (line ~357-368): old behavior
   `before.TailscaleConfig != after.TailscaleConfig`; new behavior
   `!tailscaleConfigEqual(before.TailscaleConfig, after.TailscaleConfig)`.
   Every other line in `FieldsChanged` is unchanged.
8. Modify `tailscaleRestartConfigChanged` (line ~334-339): old behavior
   compares `Hostname`, `AuthKey`, `ControlURL`, `Ephemeral` only; new
   behavior additionally returns true when
   `!slices.Equal(before.PortForwards, after.PortForwards)` (port-forward
   rule changes require a restart — the accept-loop goroutines are spun up
   once in `Runner.Start`, matching WP2's design). `ExitNodeConfigChanged`
   is NOT touched — port forwards are not a "live exit node preference".

### Acceptance criteria / tests (add to `internal/profile/profile_test.go`)

- `TestTailscaleConfigNormalizeDefaultsPortForwardProtocol`: a
  `PortForward{ListenPort: 8443, TargetAddr: " 192.168.1.50:80 "}` with empty
  `Protocol` normalizes to `Protocol: PortForwardTCP` and trimmed
  `TargetAddr`.
- `TestTailscaleConfigValidateRejectsInvalidPortForward`: table-driven test
  (mirroring the existing `TestRuntimeConfigChangedDetectsRestartFields`
  table style) covering: out-of-range port → `ErrPortForwardPortOutOfRange`;
  bad protocol string → `ErrPortForwardProtocolInvalid`; empty target →
  `ErrPortForwardTargetRequired`; unparsable target (no port) →
  `ErrPortForwardTargetInvalid`; two rules with the same `ListenPort` →
  `ErrPortForwardDuplicatePort` (use `errors.Is` against each, matching how
  `TestFieldsChangedIgnoresTimestamps` and friends use `t.Fatal` assertions
  elsewhere in the file — use `errors.Is(err, profile.ErrX)` idiom).
- `TestTailscaleConfigValidateAcceptsValidPortForward`: one TCP and one UDP
  rule on distinct ports validates with no error.
- `TestFieldsChangedDetectsPortForwardChanges`: extend/add alongside
  `TestFieldsChangedIgnoresTimestamps` — adding a `PortForward` entry to
  `after.TailscaleConfig.PortForwards` on a Tailscale profile makes
  `FieldsChanged` return true.
- `TestRuntimeConfigChangedDetectsPortForwardChanges`: add a new subtest
  case (or a new sibling test) alongside
  `TestRuntimeConfigChangedDetectsRestartFields` proving a `PortForwards`
  diff requires restart via `RuntimeConfigChanged`.
- `TestRuntimeConfigChangedIgnoresLiveTailscaleFields` and
  `TestExitNodeConfigChanged*`-style behavior: add an assertion (or a small
  new test) that changing only `PortForwards` does NOT affect
  `ExitNodeConfigChanged`'s result (keep that boundary explicit since it's
  easy to blur).
- `TestTailscaleConfigIsZero`: zero-value `TailscaleConfig{}` reports
  `IsZero() == true`; any non-zero field including a non-empty
  `PortForwards` reports `false`.

### Risk to existing tests

- `TestRuntimeConfigChangedIgnoresNonRuntimeFields`,
  `TestRuntimeConfigChangedDetectsRestartFields`,
  `TestRuntimeConfigChangedIgnoresLiveTailscaleFields`,
  `TestFieldsChangedIgnoresTimestamps` (all in `profile_test.go`) must
  continue to pass unmodified in behavior for the non-port-forward fields —
  the refactor from `!=` to `tailscaleConfigEqual`/`slices.Equal` must be
  behavior-preserving for every pre-existing field comparison, verified by
  running the full existing suite in this file after the change, before
  adding any new tests.

---

## Work Package 2 — `tailscale` runner: `tsNode.Listen`/`ListenPacket` plus per-profile forward-rule lifecycle

**Files:** `internal/tailscale/runner.go`, `internal/tailscale/runner_test.go`.

**Depends on:** Work Package 1 (needs `profile.PortForward`,
`profile.PortForwardProtocol`, `profile.PortForwardTCP/UDP`, and the
compiling `TailscaleConfig.PortForwards` field).

### Changes

1. Extend the `tsNode` interface and add `Listen`/`ListenPacket` to
   `realNode`, exactly as specified in "New types" above.
2. Extend `process` struct (currently `cancel`, `done`, `mu`, `doneOnce`,
   `closed`, `listener net.Listener`, `node tsNode`) with a new field to
   track forward-rule listeners/packet-conns so they close together with the
   SOCKS5 listener and node on `process.close()`:
   ```go
   type process struct {
       cancel context.CancelFunc
       done   chan struct{}

       mu           sync.Mutex
       doneOnce     sync.Once
       closed       bool
       listener     net.Listener
       node         tsNode
       forwardConns []io.Closer // new: TCP listeners and UDP PacketConns for active port forwards
   }
   ```
   Add `func (p *process) addForwardCloser(c io.Closer) bool` following the
   exact same closed-check pattern as `setListener`/`setNode` (returns false
   and immediately closes `c` if `p.closed` is already true; otherwise
   appends and returns true). Modify `process.close()` (old behavior: closes
   `cancel`, `listener`, `node`; new behavior: also iterates
   `p.forwardConns` and closes each) — add the import `"io"` to
   `runner.go`.
3. Add a new unexported function
   `func (r *Runner) startPortForwards(ctx context.Context, p profile.Profile, node tsNode, proc *process) error`
   called from `Start()` immediately after the existing `if
   !proc.commitStart() { ... }` block and before `started = true` (old
   behavior: `Start()` goes straight from `commitStart()` to emitting
   `EventStarted` and spawning the SOCKS5 goroutine; new behavior: it also
   calls `startPortForwards` for every rule in
   `p.TailscaleConfig.PortForwards`, and if any rule's `Listen`/
   `ListenPacket` call errors, `Start()` returns that error and the existing
   `defer` cleanup path runs — meaning any port-forward bind failure aborts
   the whole profile start, matching the existing SOCKS5-listener-bind
   failure behavior). Each successfully bound listener/packet-conn is
   registered via `proc.addForwardCloser` and its accept/relay loop is
   launched in its own goroutine (do not block `Start()` waiting for these
   loops — same fire-and-forget goroutine pattern as the existing
   `serveSocks5` goroutine, but do NOT emit `EventStopped`/`EventError` from
   these goroutines on ordinary cancellation, to avoid duplicate/confusing
   stop events competing with the SOCKS5 goroutine's own
   `EventStopped`/`EventError`; do emit `EventError` for forward-loop
   failures that are not due to context cancellation or `net.ErrClosed`, so
   operational forward failures are visible in the log, but do not call
   `r.removeProcess` from these goroutines — only the SOCKS5 goroutine owns
   process teardown).
4. Add `func (r *Runner) relayTCPForward(ctx context.Context, node tsNode, listener net.Listener, rule profile.PortForward)`:
   an accept loop that, per accepted `net.Conn`, dials
   `net.Dial("tcp", rule.TargetAddr)` (plain host dialer, NOT `node.Dial` —
   per the feasibility doc's explicit call-out that the LAN target is not on
   the tailnet) and relays with two `io.Copy` goroutines + `sync.WaitGroup`,
   closing both sides when either direction finishes or `ctx` is canceled —
   mirror the connection-close-on-context-done pattern already used in
   `fakeTSNode.dialFunc` test helpers and in `realNode`/`socksReplyConn`
   conventions (wrap in a small named function, not inlined, so it is
   testable independently if useful, but a single non-exported function is
   sufficient; do not over-engineer this into a public type).
5. Add `func (r *Runner) relayUDPForward(ctx context.Context, node tsNode, packetConn net.PacketConn, rule profile.PortForward)`:
   a NAT-style relay: read datagrams from `packetConn`, key an
   in-memory session map by source `net.Addr.String()`, and for each new
   source open one UDP connection to `rule.TargetAddr` via
   `net.Dial("udp", rule.TargetAddr)` (again plain, not `node.Dial`),
   spawning a goroutine that copies replies from that dial back out through
   `packetConn.WriteTo(buf, sourceAddr)`. Mirror the existing SOCKS5 UDP
   ASSOCIATE relay's session/lifetime pattern as closely as practical (idle
   sessions should be cleaned up either via a read deadline + prune loop or
   torn down when `ctx` is canceled — do not leak goroutines on profile
   stop; all such session goroutines must exit within one relay-loop
   `ctx.Done()`).
6. Do NOT modify `runSocks5`, `NewRunner`, `newTSNetNode`, or
   `configureExitNode` — this package's other functions are unaffected.

### Acceptance criteria / tests (add to `internal/tailscale/runner_test.go`)

- Add `listenFunc`/`listenPacketFunc` fields and methods to `fakeTSNode` and
  the two sentinel errors, exactly as specified in "New types" above.
- `TestStartPortForwardsBindsEachConfiguredRule`: a `Runner` with a stubbed
  `newNode` returning a `fakeTSNode` whose `listenFunc` records every
  `(network, addr)` call; start a profile with two `PortForward` rules (one
  tcp, one udp) and assert both `Listen`/`ListenPacket` were called with the
  expected addr (`":<port>"` form, matching how `tsnet.Server.Listen`
  expects addrs per its doc comment — confirm exact addr string format used
  by `startPortForwards` here).
- `TestPortForwardBindFailureAbortsStart`: `listenFunc` returns an error for
  one rule; assert `Runner.Start` returns a non-nil error and the profile is
  not left running (`Runner.Running(profileID)` is false afterward, matching
  the existing failure-cleanup pattern already exercised by other `Start`
  failure tests in this file — find and follow the closest existing
  precedent, e.g. a test asserting cleanup after `upErr`).
- `TestRelayTCPForwardRoundTripsPayload`: following the real-listener
  pattern from `TestRunSocks5RoutesUDPAssociateThroughNode` (use
  `net.Listen("tcp", "127.0.0.1:0")` as the stand-in for the `tsNode.Listen`
  result, a `fakeTSNode` is not needed here since `relayTCPForward` takes a
  `net.Listener` directly), plus a real local TCP echo/target listener;
  dial the forward-facing listener, write a payload, assert the real target
  received it and the client received the reply.
- `TestRelayUDPForwardRoundTripsPayload`: following the same real-listener
  pattern as `TestRunSocks5RoutesUDPAssociateThroughNode` (real
  `net.ListenUDP` standing in for `tsnet.Server.ListenPacket`'s result, and
  a real `net.ListenUDP` target), send one datagram in, assert it's relayed
  to the target and the reply flows back to the original source address.
- `TestProcessCloseClosesForwardListeners`: unit test on `process` directly
  (not through `Runner.Start`) — register a fake `io.Closer` via
  `addForwardCloser`, call `process.close()`, assert the closer's `Close()`
  was invoked (add a tiny `closeRecorder` test helper implementing
  `io.Closer`, following the existing `fakeListener` helper style already in
  this file).

### Risk to existing tests

- `TestRunSocks5PreservesHostnameDestination` and
  `TestRunSocks5RoutesUDPAssociateThroughNode` are untouched (this package
  does not modify `runSocks5`) but **must still pass** since `fakeTSNode`
  gains new fields/methods — verify no existing test relies on `fakeTSNode`
  having exactly its current method set (e.g. via reflection or an interface
  assertion) — a quick `grep -n "fakeTSNode{" internal/tailscale/runner_test.go`
  confirms all existing construction sites use named fields, so adding new
  optional fields is additive and safe.
- Any test constructing a `Runner` via `NewRunner()` and calling `Start`
  with a Tailscale profile that has an empty `PortForwards` slice must
  behave identically to today (zero forward rules ⇒ `startPortForwards` is
  effectively a no-op loop) — explicitly verify this by running the full
  existing `TestRunner*`/`TestStart*` suite in this file after the change.

---

## Work Package 3 — `profilejson` persistence

**Files:** `internal/profilejson/profilejson.go`, `internal/profilejson/profilejson_test.go`.

**Depends on:** Work Package 1 (needs `profile.PortForward`,
`profile.TailscaleConfig.IsZero()`).

Does not depend on Work Package 2 (runner) or Work Package 4 (UI) and can
proceed in parallel with them once WP1 lands.

### Changes

1. Add a new stored type mirroring the domain `PortForward` field-by-field
   (no reflection/tags-only marshaling, consistent with the file's existing
   style):
   ```go
   type storedPortForward struct {
       ListenPort int    `json:"listen_port"`
       Protocol   string `json:"protocol"`
       TargetAddr string `json:"target_addr"`
   }
   ```
2. Add `PortForwards []storedPortForward` `json:"port_forwards,omitempty"`
   field to `storedTailscaleConfig`.
3. Modify `fromDomainProfile` (line ~207-231): old behavior builds
   `stored.TailscaleConfig` with 8 explicit fields when
   `item.IsTailscale() || item.TailscaleConfig != (profile.TailscaleConfig{})`;
   new behavior changes the zero-check to
   `item.IsTailscale() || !item.TailscaleConfig.IsZero()` (per WP1's
   `IsZero()` method) and additionally maps
   `item.TailscaleConfig.PortForwards` into `stored.TailscaleConfig.PortForwards`
   via a small new helper `func fromDomainPortForwards(rules
   []profile.PortForward) []storedPortForward` (returns `nil` for a `nil`/
   empty input, matching the `omitempty` JSON tag and the file's existing
   nil-vs-empty conventions — check `fromDomainProfiles`'s
   `make([]storedProfile, 0, len(profiles))` pattern for the preferred
   idiom of always allocating with `make(..., 0, len(rules))` then
   appending, so re-marshal round trips are stable).
4. Modify `toDomain` (line ~254-273, the `storedProfile.toDomain` method):
   old behavior maps 8 fields when `item.TailscaleConfig != nil`; new
   behavior additionally maps `item.TailscaleConfig.PortForwards` into
   `domain.TailscaleConfig.PortForwards` via a symmetric
   `func toDomainPortForwards(rules []storedPortForward) []profile.PortForward`.
5. Modify `hasImportProfileContent` (line ~305-315): old behavior returns
   `item.IsTailscale() && (item.TailscaleConfig != profile.TailscaleConfig{})`;
   new behavior returns `item.IsTailscale() && !item.TailscaleConfig.IsZero()`
   (this call site is purely a mechanical swap to the new `IsZero()` method
   from WP1 — do not change any other logic in this function).

### Acceptance criteria / tests (add to `internal/profilejson/profilejson_test.go`)

- `TestRepositoryRoundTripPreservesPortForwards`: build on the exact
  pattern of `TestRepositoryRoundTripPreservesTailscaleDomainFields` (read
  it first) — create a Tailscale profile with 2 `PortForward` rules
  (one tcp, one udp), `Save`, `Load`, assert the loaded profile's
  `TailscaleConfig.PortForwards` matches exactly (order-preserving; do not
  sort, since the file has no established sorting convention for this kind
  of list — confirm by checking whether `TestRepositoryRoundTripPreservesTailscaleDomainFields`
  or any other test asserts on list order elsewhere in this file, and follow
  that precedent if one exists; otherwise preserve insertion order as the
  new `storedPortForward` mapping does).
- `TestFromDomainProfileOmitsEmptyPortForwards`: a Tailscale profile with no
  `PortForwards` marshals with `port_forwards` field absent from the JSON
  (via `omitempty`) — follow `TestBundleEncodingUsesVersionOne`'s style of
  asserting on the raw encoded bytes/structure if that's the existing
  convention for field-presence assertions in this file, otherwise assert
  via `stored.Profiles[0].TailscaleConfig.PortForwards == nil` after
  round-tripping through `EncodeBundle`/`json.Unmarshal`.
- `TestCodecEncodeExportClearsLocalTailscaleAuthentication` (existing test)
  must keep passing unmodified — it exercises the same `fromDomainProfile`
  path this package touches; add a quick manual check that a profile with
  both `Authenticated: true` and non-empty `PortForwards` still has
  `PortForwards` preserved through `EncodeExport` (auth-clearing must not
  accidentally clear or corrupt port forwards) — add this as an assertion
  extension to that existing test rather than a new test, since it is
  testing an interaction between two fields on the same struct.

### Risk to existing tests

- `TestRepositoryRoundTripPreservesTailscaleDomainFields`,
  `TestCodecDecodeImportRejectsAuthenticatedOnlyTailscaleProfile`,
  `TestCodecLegacyProfileDefaultsToWireGuard`, and
  `TestBundleEncodingUsesVersionOne` all touch `fromDomainProfile`/
  `toDomain`/`hasImportProfileContent` and must be re-run and kept green;
  none of them construct a profile with `PortForwards` set today, so the
  mechanical `IsZero()` swap must be verified to produce identical results
  to the old `!= (TailscaleConfig{})` comparison for every zero/non-zero
  `TailscaleConfig` value already exercised by these tests.
- Old JSON files (schema `version: 1`) with no `port_forwards` key must
  still load correctly (`json.Unmarshal` into a struct with a missing key
  simply leaves the new slice field `nil` — no explicit migration code is
  needed; call this out in a short code comment near
  `storedTailscaleConfig` since it's a forward-compatibility fact worth
  documenting, not because it needs any migration logic).

---

## Work Package 4 — Minimal GUI surface for managing forward rules

**Files:** `internal/ui/app.go` (and `internal/ui/app_test.go` if one
exists — check before writing tests; if none exists, add a new
`internal/ui/portforward_test.go` for any pure-function tests extracted per
below, to avoid growing `app.go`/its test file further than necessary).

**Depends on:** Work Package 1 (needs `profile.PortForward`,
`profile.PortForwardProtocol` types and `TailscaleConfig.Validate()`
producing the new `Err*` sentinels for surfacing in the UI's existing error
display path). Does not depend on Work Package 2 or 3, and can proceed in
parallel with them once WP1 lands. **Non-overlapping-edit note:** this is
the only work package touching `internal/ui/app.go`; no other package may
edit this file.

### Design decision: minimal but present, not deferred

The task brief requires *some* UI surface or an explicit justification to
defer it. Deferring would leave the feature configurable only by
hand-editing the JSON store, which is inconsistent with how every other
Tailscale field in this app is exposed (`setupTailscaleForm`). The minimal
surface below is intentionally simple: a fixed-layout add/remove list, not a
polished editable table, matching the "does not need to be polished"
allowance in the task brief.

### Changes

1. Add new `GUI` struct fields near the existing `tsHostname`,
   `tsAuthKey`, etc. fields (search for the struct definition holding those
   — likely near line ~90-100 per the earlier `tailscaleForm *widget.Form`
   sighting):
   ```go
   tsPortForwards       []profile.PortForward // in-memory working list for the form
   tsPortForwardList    *widget.List          // or *fyne.Container of rows — implementer's choice of Fyne widget, but must expose add/remove per-row
   tsPortForwardAdd     *widget.Button
   tsPortForwardPort    *widget.Entry
   tsPortForwardProto   *widget.Select
   tsPortForwardTarget  *widget.Entry
   ```
   (Exact widget types are an implementation detail left to the worker;
   what's fixed is: one form section, an add-row control with 3 inputs
   —listen port, protocol select of `{"tcp","udp"}`, target address— and a
   list of already-added rules each with a remove button.)
2. Extend `setupTailscaleForm()` (~line 367-420): old behavior builds 7
   `*widget.FormItem`s ending with `{Text: tr("Node lifetime"), Widget:
   g.tsEphemeral}`; new behavior appends one more `*widget.FormItem` whose
   `Widget` is a `*fyne.Container` combining the add-row controls and the
   rule list (`container.NewVBox(addRow, g.tsPortForwardList)` or
   equivalent), with `Text: tr("Port forwards")` and a `HintText` explaining
   the tailnet-IP-to-LAN relay behavior in one sentence.
3. Extend `setTailscaleForm(config profile.TailscaleConfig)` (~line
   454-470): old behavior sets each simple field's widget text/checked
   state from `config`; new behavior also sets `g.tsPortForwards =
   append([]profile.PortForward(nil), config.PortForwards...)` (defensive
   copy) and calls a new `func (g *GUI) refreshPortForwardList()` that
   rebuilds `g.tsPortForwardList`'s displayed rows from `g.tsPortForwards`.
4. Extend `profileFromForm(existing profile.Profile) (profile.Profile,
   error)` (~line 1394-1437): old behavior builds
   `existing.TailscaleConfig` with 7 fields inside the `if
   existing.IsTailscale()` branch; new behavior adds an 8th field,
   `PortForwards: append([]profile.PortForward(nil), g.tsPortForwards...)`,
   to that same struct literal. The `else` branch that clears
   `existing.TailscaleConfig = profile.TailscaleConfig{}` for non-Tailscale
   profiles needs no change (zero-value already has a nil `PortForwards`).
5. Add `func (g *GUI) addPortForwardFromInputs() error`: parses
   `g.tsPortForwardPort.Text` as an int, reads `g.tsPortForwardProto.Selected`,
   trims `g.tsPortForwardTarget.Text`, appends a `profile.PortForward` to
   `g.tsPortForwards`, calls `g.refreshPortForwardList()`, and clears the
   input widgets — wired as the `OnTapped`/`OnSelected` handler of
   `g.tsPortForwardAdd`. On parse failure, surface the error using whatever
   existing error-display convention this file already uses for form
   validation errors (find and reuse it — do not invent a new error-toast
   mechanism; check how `profileFromForm`'s returned errors are already
   shown to the user, e.g. via a dialog or inline label, and reuse that same
   call).
6. Add `func (g *GUI) removePortForward(index int)`: removes
   `g.tsPortForwards[index]` and calls `refreshPortForwardList()`.
7. Add `func (g *GUI) refreshPortForwardList()`: rebuilds the list widget's
   rows, each row showing `"{Protocol} {ListenPort} -> {TargetAddr}"` and a
   remove button bound to `removePortForward(i)`.

### Acceptance criteria / tests

- If `internal/ui` has an existing widget-level test harness (check for a
  `_test.go` file using `test.NewApp()`/`test.NewWindow()` from
  `fyne.io/fyne/v2/test` before writing new tests — follow that exact
  harness setup if present), add:
  - `TestProfileFromFormIncludesPortForwards`: seed `g.tsPortForwards` with
    two rules, call `profileFromForm`, assert the returned
    `profile.TailscaleConfig.PortForwards` matches.
  - `TestSetTailscaleFormPopulatesPortForwards`: call `setTailscaleForm`
    with a config containing rules, assert `g.tsPortForwards` is populated
    (defensive-copied, not aliasing the input slice — mutate the input
    after the call and assert `g.tsPortForwards` is unaffected).
  - `TestAddPortForwardFromInputsValidatesPort`: invalid port text produces
    an error and does not mutate `g.tsPortForwards`.
- If no such harness exists in this package today, do not introduce a new
  heavyweight Fyne test-app harness just for this feature; instead extract
  the three pure-logic pieces (`addPortForwardFromInputs`'s parsing,
  `profileFromForm`'s new field assembly, `refreshPortForwardList`'s label
  formatting) into small widget-independent helper functions/tests where
  feasible, and note in the PR description which parts remain
  manually-verified-only. State explicitly in the final PR/commit message
  which of these two paths was taken.

### Risk to existing tests

- Any existing test around `profileFromForm`/`setTailscaleForm`/
  `setupTailscaleForm` (search `internal/ui` test files for these names
  before editing) must keep passing — the new field additions are additive
  (new struct literal field, new form item, new widget refs) and must not
  change any existing widget's index/position assumptions if any test
  inspects `g.tailscaleForm.Items` by index.
- `showSelected()`'s reset branch (~line 1467) calls
  `g.setTailscaleForm(profile.TailscaleConfig{})` — verify this correctly
  clears `g.tsPortForwards` to empty via the same defensive-copy logic in
  step 3 above (an empty/nil `PortForwards` on a zero-value
  `TailscaleConfig` naturally produces an empty `g.tsPortForwards`).

---

## Cross-package integration notes (read by every worker)

- WP1 is a hard prerequisite for WP2, WP3, and WP4 — it must be merged (or
  at minimum, fully implemented and compiling standalone with its own tests
  green) before the other three start, since they all import the new
  `profile.PortForward`/`profile.PortForwardProtocol` types.
- WP2, WP3, and WP4 touch three disjoint files
  (`internal/tailscale/runner.go`, `internal/profilejson/profilejson.go`,
  `internal/ui/app.go` respectively) plus their own `_test.go` files, and
  have no edit overlap with each other — they can be implemented fully in
  parallel once WP1 lands.
- No work package should modify `docs/portable-tailscale-server-client-feasibility.md`
  or `docs/nat-like-features-feasibility.md` — those are historical
  investigation records, not living design docs; if this plan's decisions
  diverge from something stated there, this plan wins for implementation
  purposes.
- Naming discipline: use `profile.PortForward`, `profile.PortForwardProtocol`,
  `profile.PortForwardTCP`, `profile.PortForwardUDP` verbatim in every
  package — do not rename or alias these types locally.

## Final integration / verification checklist

Run after all four work packages are merged, from the repo root:

```sh
go build ./...
go test -race ./...
golangci-lint run --build-tags=ci
```

Additionally:
- Manually verify (or add an end-to-end test if the repo has an existing
  e2e harness for Tailscale profiles — check for one before assuming none
  exists) that starting a Tailscale profile with one TCP and one UDP
  `PortForward` rule against two real `tsnet.Server` instances (or the
  existing test double pattern, if a full two-node integration test is
  judged too slow/flaky for CI) results in a successful relay from a
  simulated tailnet peer to a local target, and that `Runner.Stop`/
  `StopAllAndWait` cleanly tears down the forward listeners alongside the
  SOCKS5 listener with no leaked goroutines (`go test -race` will catch
  most leaks that manifest as data races; consider a `goleak`-style check
  only if the repo already uses one elsewhere — do not introduce a new test
  dependency for this alone).
- Confirm `internal/profile/profile_test.go`,
  `internal/tailscale/runner_test.go`,
  `internal/profilejson/profilejson_test.go`, and any `internal/ui` tests
  all pass together, not just individually per-package, in case of any
  cross-package fixture assumptions (none are currently known, but this is
  the first feature to touch all three layers plus the UI in one change).
