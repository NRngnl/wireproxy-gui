# Tailscale Integration Research

## Goal

Add a Tailscale-backed connection type that behaves like the current
WireGuard-backed profiles: connecting a profile starts a local proxy listener,
and applications can send traffic through that listener. For Tailscale profiles,
the proxy should be able to route internet traffic through a selected Tailscale
exit node.

## Current App Shape

- `internal/profile.Profile` stores one connection profile, one local SOCKS5 bind
  address, and one WireGuard config blob.
- `internal/wireproxy.Runner` owns the runtime lifecycle for each profile:
  validate config, bind the SOCKS5 listener, start the embedded WireGuard engine,
  serve SOCKS5 through that engine, then close the listener and tunnel on stop.
- The UI treats the backend as a generic profile runner through `profileRunner`.
  Shared runtime events live in `internal/connection`, with backend-specific
  runners behind `internal/runner`.

That shape maps well to a second backend: a profile can still own one local
proxy address and one runtime process/state object.

## Tailscale Facts Checked

- Tailscale exit nodes route public internet traffic by using default routes
  through a selected tailnet device. Tailscale requires opt-in: the exit node has
  to advertise exit-node capability, an admin has to allow it, and the client has
  to select it.
- `tailscaled --tun=userspace-networking` runs without a kernel TUN device and
  can expose SOCKS5 and HTTP proxy listeners.
- The stable `tailscaled` proxy flags are:
  - `--socks5-server=[host]:port`
  - `--outbound-http-proxy-listen=[host]:port`
  - The SOCKS5 and HTTP proxy can share one address.
- `tailscale up` / `tailscale set` accepts `--exit-node` and
  `--exit-node-allow-lan-access`.
- `tailscale exit-node list` and `tailscale exit-node suggest` expose exit-node
  discovery flows from the CLI.
- `tailscale.com/tsnet` embeds a Tailscale node directly in a Go process, stores
  node state in an app-controlled directory, does not require root privileges,
  and exposes `Server.Dial`, `Server.LocalClient`, and `Server.Loopback`.
- `tsnet.Server.Loopback()` creates a loopback SOCKS5 proxy, but it chooses its
  own port and requires generated username/password credentials. That is useful
  for a proof of concept, but does not match this app's current user-selected
  no-auth SOCKS5 bind address.
- Tailscale prefs support selecting exit nodes with `ExitNodeID`,
  `ExitNodeIP`, or `AutoExitNode`; `AutoExitNode` can ask the client to choose
  an exit node automatically.

Primary sources:

- Tailscale exit-node docs: https://tailscale.com/docs/features/exit-nodes
- Tailscale userspace networking docs: https://tailscale.com/docs/concepts/userspace-networking
- `tailscaled` daemon flags: https://tailscale.com/docs/reference/tailscaled
- Tailscale CLI docs: https://tailscale.com/docs/reference/tailscale-cli
- `tsnet` Go package docs: https://pkg.go.dev/tailscale.com/tsnet
- Tailscale `ipn` prefs docs: https://pkg.go.dev/tailscale.com/ipn

## Recommended Direction

Use `tsnet` as the only accepted Tailscale integration path.

This keeps the app's current embedded-runtime model: no separate `tailscaled`
binary, no system daemon management, no root requirement, and one independent
Tailscale node state directory per profile. It also lets the existing runner
pattern remain mostly intact.

Do not use `tsnet.Server.Loopback()` as the final user-facing proxy because it
does not let the profile own the configured bind address. Instead, start a
SOCKS5 server on `Profile.BindAddress()` and make its dialer call
`tsnet.Server.Dial`. The implementation reuses the existing
`github.com/things-go/go-socks5` dependency with a no-resolve resolver so domain
name SOCKS requests are passed to `tsnet.Dial` as hostnames.

Do not implement an external `tailscaled` runner. The app should not require,
launch, bundle, or manage the `tailscale` or `tailscaled` command-line tools for
Tailscale profile runtime behavior.

## Profile Model Changes

Add a backend kind while preserving old JSON profiles as WireGuard profiles:

```go
type BackendKind string

const (
	BackendWireGuard BackendKind = "wireguard"
	BackendTailscale BackendKind = "tailscale"
)

type Profile struct {
	ID              string      `json:"id"`
	Kind            BackendKind `json:"kind,omitempty"`
	Name            string      `json:"name"`
	WireGuardConfig string      `json:"wireguard_config,omitempty"`
	TailscaleConfig Tailscale   `json:"tailscale_config,omitempty"`
	SocksHost       string      `json:"socks_host"`
	SocksPort       int         `json:"socks_port"`
	AutoStart       bool        `json:"auto_start"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

type Tailscale struct {
	Hostname               string `json:"hostname,omitempty"`
	AuthKey                string `json:"auth_key,omitempty"`
	Authenticated          bool   `json:"authenticated,omitempty"`
	ControlURL             string `json:"control_url,omitempty"`
	ExitNode               string `json:"exit_node,omitempty"`
	AutoExitNode           bool   `json:"auto_exit_node,omitempty"`
	ExitNodeAllowLANAccess bool   `json:"exit_node_allow_lan_access,omitempty"`
	Ephemeral              bool   `json:"ephemeral,omitempty"`
}
```

Security note: `AuthKey` is sensitive. The existing app already stores WireGuard
private keys in a `0600` profile file, so temporarily saving auth keys before
first start is consistent with the current security model. After Tailscale
authentication succeeds, the app clears `AuthKey`, stores `Authenticated`, and
keeps the auth field locked until Logout removes the profile's tsnet state.

## Runner Design

Create a Tailscale runner beside `internal/wireproxy`:

```text
internal/tailscale/
  runner.go
  runner_test.go
```

Runtime flow:

1. Normalize and validate the Tailscale profile.
2. Bind `Profile.BindAddress()` before starting Tailscale so bind conflicts fail
   early, matching the current WireGuard runner.
3. Start `tsnet.Server` with a per-profile state dir:

   ```go
   stateDir, err := profileStateDir(configDir, profile.ID)
   srv := &tsnet.Server{
    Dir:        stateDir,
    Hostname:   cfg.Hostname,
    AuthKey:    cfg.AuthKey,
    ControlURL: cfg.ControlURL,
    Ephemeral:  cfg.Ephemeral,
    UserLogf:   emitProfileLog,
   }
   ```

   The state-dir helper rejects empty IDs, `.`/`..`, and path separators before
   joining paths.

4. Call `srv.Up(ctx)` so the runner waits until the node is usable. If the node
   needs browser login, `UserLogf` should emit the auth URL into the profile log.
5. Configure exit-node prefs through `srv.LocalClient()`:

   ```go
   lc, err := srv.LocalClient()
   status, err := lc.Status(ctx)
   prefs, err := lc.GetPrefs(ctx)
   prefs.ExitNodeAllowLANAccess = cfg.ExitNodeAllowLANAccess
   if cfg.AutoExitNode {
    prefs.AutoExitNode = ipn.AnyExitNode
   } else if cfg.ExitNode != "" {
    err = prefs.SetExitNodeIP(cfg.ExitNode, status)
   } else {
    prefs.ClearExitNode()
   }
   _, err = lc.EditPrefs(ctx, &ipn.MaskedPrefs{
    Prefs:                     *prefs,
    ExitNodeIDSet:             true,
    ExitNodeIPSet:             true,
    AutoExitNodeSet:           true,
    ExitNodeAllowLANAccessSet: true,
   })
   ```

   The mask explicitly covers clearing `AutoExitNode`, `ExitNodeIP`, and
   `ExitNodeID`.

6. Serve SOCKS5 on the already-bound listener with a dialer that calls
   `srv.Dial(ctx, network, address)`.
7. On stop, close the SOCKS5 listener and call `srv.Close()`.

The existing UI runner interface stays close to its original shape. A dispatcher
in `internal/runner` routes WireGuard profiles to `internal/wireproxy.Runner`
and Tailscale profiles to `internal/tailscale.Runner`.

## Rejected Alternative: External `tailscaled`

An external process runner is intentionally out of scope. It would require each
profile to manage a private socket and state directory:

```sh
tailscaled \
  --tun=userspace-networking \
  --socket=/path/to/profile.sock \
  --statedir=/path/to/profile-state \
  --socks5-server=127.0.0.1:1080
```

Then configure the node:

```sh
tailscale --socket=/path/to/profile.sock up \
  --hostname=wireproxy-gui-demo \
  --auth-key="$TAILSCALE_AUTH_KEY" \
  --exit-node=<exit-node-name-or-ip> \
  --exit-node-allow-lan-access=false
```

This path is rejected because the app would have to find or bundle `tailscale`
and `tailscaled`, manage child processes, parse CLI output, and handle platform
differences. It also breaks the app's current embedded-runtime model.

## UI Scope

Minimal Tailscale UI fields:

- Backend selector: WireGuard or Tailscale.
- SOCKS5 host and port: keep existing fields.
- Tailscale hostname: optional, defaults from profile name.
- Auth key: optional. A Tailscale profile can be started with Login or Connect:
  when the auth key is set, `tsnet` uses it while creating new profile state;
  when it is empty, the app logs the browser sign-in URL from `tsnet`.
  If Tailscale requires device approval, the app logs that the device is
  waiting for approval in the admin console and continues automatically once
  the backend reaches `Running`. Once the backend reaches `Running`, the UI
  clears the saved auth key, marks the profile authenticated, and switches Login
  to Logout. Logout removes that profile's tsnet state and unlocks auth entry.
  Export omits the local authenticated marker. Imported profiles are assigned
  new profile IDs and do not include tsnet state, so import clears authenticated
  state and requires Login again.
- Control URL: optional, for Headscale or non-default control servers.
- Exit node:
  - empty means no exit node,
  - automatic mode asks Tailscale to pick an exit node,
  - hostname or IP means a specific exit node.
- Allow LAN access while using exit node: checkbox.
- Register as ephemeral node: checkbox.

The "Refresh" action next to the exit-node field loads current exit-node
options from `tsnet.Server.LocalClient()` status data while the Tailscale profile
is connected. The list is manual, not live-updating; refresh again after tailnet
device or approval changes. Exit-node mode, selected exit node, and LAN access
can be changed while the profile is connected; Save applies those prefs to the
running `tsnet` node without reconnecting.

## Proof Of Concept Checks

This machine does not currently have `tailscale` or `tailscaled` on `PATH`, but
the accepted implementation should not depend on those tools. Runtime
verification needs an embedded `tsnet` prototype plus a real tailnet, auth flow,
and usable exit node.

For the `tsnet` implementation, verify:

```sh
curl --socks5-hostname 127.0.0.1:1080 https://ifconfig.me
```

Expected result: the observed public IP belongs to the selected exit node, not
the local network.

Also verify:

- connecting to a tailnet-only hostname through the proxy succeeds,
- connecting to a public hostname through the proxy uses the exit node,
- disconnect closes the listener,
- duplicate bind addresses fail before Tailscale login,
- a missing/unapproved exit node does not silently leak traffic outside the
  selected route,
- old WireGuard profile JSON loads as `BackendWireGuard`.

## Implementation Status

- Done: shared runner events moved to `internal/connection`.
- Done: profile backend kind, Tailscale config, import/export migration, and
  validation coverage.
- Done: `internal/tailscale.Runner` embeds `tsnet.Server`, binds the configured
  SOCKS5 address, configures exit-node prefs, and serves SOCKS5 through
  `tsnet.Dial`.
- Done: UI backend selector, conditional WireGuard/Tailscale forms, and manual
  exit-node refresh.
- Done: connected Tailscale profiles can update exit-node prefs dynamically.
- Done: successful Tailscale auth clears saved auth keys and Logout removes
  stored profile auth state.
- Remaining: manual integration verification with a real tailnet, auth flow,
  and exit node.
- Do not add an external `tailscaled` process runner.
