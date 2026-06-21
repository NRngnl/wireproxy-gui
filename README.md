# Wireproxy GUI

Wireproxy GUI is a small Go/Fyne desktop app for running WireGuard- or
Tailscale-backed SOCKS5 proxies from a native GUI and system tray. It manages
multiple saved profiles, starts each connection in-process through the embedded
WireGuard engine or an embedded Tailscale `tsnet` node, and does not shell out to
the `wireproxy`, `tailscale`, or `tailscaled` command-line tools.

The app is intended for users who already have WireGuard profile files or a
Tailscale tailnet and want each profile exposed as a local SOCKS5 listener, such
as `127.0.0.1:1080`.

## Features

- Add WireGuard or Tailscale profiles manually, or import WireGuard `.conf`
  files.
- Import and export JSON profile bundles.
- Run multiple profiles at the same time with separate SOCKS5 bind addresses.
- Connect or disconnect one profile, or connect/disconnect every saved profile.
- Show profile status in the sidebar and tray menu.
- Show the SOCKS5 bind address and backend detail for each profile.
- Register an embedded Tailscale device with an auth key or browser sign-in.
- Select a Tailscale exit node manually, by node ID, MagicDNS base name, or IP,
  or with automatic mode.
- Follow each profile log by default, with scrollback preserved when you scroll up.
- Auto-connect selected profiles when the app opens.
- Use native file dialogs for import and export.
- Hide to tray on window close when tray support is available.
- Stop active profiles during app shutdown and wait briefly for listeners to close.

## Requirements

- Go 1.26.4.
- macOS, Linux, or Windows with GUI support from Fyne.
- `mise` is optional, but the repo includes `.mise.toml` for tool pinning.
- No separate `wireproxy`, `tailscale`, or `tailscaled` binary is required.

For native file dialogs, macOS and Windows work through the platform dialog
support used by `github.com/ncruces/zenity`. On Unix-like desktops, install one
of `zenity`, `matedialog`, or `qarma` if file pickers do not open.

## Install Tools

With `mise`:

```sh
mise install
```

Without `mise`, install a compatible Go toolchain and `golangci-lint` yourself.

## Run From Source

```sh
go run ./cmd/wireproxy-gui
```

Or with `mise`:

```sh
mise run build
./bin/wireproxy-gui
```

## Build

Build the executable:

```sh
go build -o bin/wireproxy-gui ./cmd/wireproxy-gui
```

The local macOS app bundle used during development is:

```text
dist/Wireproxy GUI.app
```

That bundle is generated outside Git and is ignored by the repository. To install
it on macOS, copy the `.app` bundle into `/Applications` or run it in place.

## Release Builds

The project version is stored in [VERSION](VERSION). Release builds inject the
version, commit, and UTC build time into the binary.

Build a release artifact for the native OS and architecture:

```sh
scripts/build-release.sh
```

Build a specific target when the host has the required native/cross CGO toolchain:

```sh
RELEASE_VERSION=0.1.0 GOOS=darwin GOARCH=arm64 scripts/build-release.sh
```

Artifacts are written to:

```text
dist/release/artifacts/
```

The macOS release artifact is a zipped `.app` bundle. Linux releases are
`.tar.gz` archives. Windows releases are `.zip` archives. Each artifact gets a
neighboring `.sha256` checksum file.

GitHub Actions builds releases automatically when a version tag such as `0.1.0`
or `v0.1.0` is pushed. The release workflow runs on Ubuntu only and uses
`fyne-cross` containers to cross-compile Linux and Windows for amd64 and
aarch64/arm64.

macOS release artifacts are intentionally not built in CI. Build them locally on
macOS with `scripts/build-release.sh`, or pass `GOOS=darwin GOARCH=arm64` or
`GOOS=darwin GOARCH=amd64` when using a host with the required macOS CGO
toolchain.

## Test And Lint

```sh
go test ./...
go vet ./...
golangci-lint run
```

The repo also exposes the common commands through `mise`:

```sh
mise run test
mise run lint
```

## Profile Storage

Profiles are stored in the current user's OS config directory:

```text
<user-config-dir>/wireproxy-gui/profiles.json
```

On macOS this is normally:

```text
~/Library/Application Support/wireproxy-gui/profiles.json
```

The profile directory is created with owner-only permissions, and profile JSON is
written with `0600` permissions where the platform supports it.

## WireGuard Profiles

Imported WireGuard files must include the usual required sections and keys:

```ini
[Interface]
Address = ...
PrivateKey = ...

[Peer]
PublicKey = ...
AllowedIPs = ...
Endpoint = ...
```

The GUI stores the WireGuard configuration text you provide. When a profile is
started, it removes any existing `[Socks5]` section from that text and injects
the profile's selected bind address:

```ini
[Socks5]
BindAddress = 127.0.0.1:1080
```

Use a unique SOCKS5 bind address for every profile you run at the same time.

## Tailscale Profiles

Tailscale profiles run an embedded `tsnet` node with one state directory per
profile. Paste an auth key and click Login or Connect to register this app as a
Tailscale device. Leave the auth key empty to use the browser sign-in URL written
to the profile log.

If Tailscale requires device approval, the profile log asks you to approve the
device in the Tailscale admin console and the app continues automatically after
approval. After authentication succeeds, the saved auth key is removed from the
profile and the auth field shows Authenticated. Click Logout to remove that
profile's stored Tailscale state and unlock auth again.

Exit-node choices are loaded manually. Connect the Tailscale profile, click
Refresh next to the exit-node field, and refresh again after tailnet device or
approval changes. You can also type a node ID, MagicDNS base name, or Tailscale
IP address manually. Automatic exit asks Tailscale to choose an available exit
node. While connected, change the exit-node mode, selected node, or LAN access
setting and click Save to apply it without reconnecting.

## Import And Export

Import accepts:

- WireGuard `.conf` files.
- JSON files previously exported by Wireproxy GUI.
- JSON arrays or single JSON profile objects with compatible fields.

Tailscale node state and the local authenticated marker are not exported.
Imported Tailscale profiles are treated as not authenticated and require Login
again.

Export writes a JSON bundle:

```json
{
  "version": 1,
  "profiles": []
}
```

Exported files are written with `0600` permissions where supported because they
may contain WireGuard private keys or saved Tailscale auth keys.

## Runtime Behavior

Each connected profile owns an embedded backend and SOCKS5 listener. WireGuard
profiles run the embedded WireGuard engine; Tailscale profiles run an embedded
tsnet node. The app tracks profile states as disconnected, connecting,
connected, disconnecting, or error. Runtime fields are locked while a profile is
active; disconnect the profile before changing backend, SOCKS5 bind, WireGuard
config, or Tailscale auth settings. Tailscale exit-node settings can be changed
while connected; click Save to apply the new exit-node preference to the running
embedded node.

For Tailscale profiles, paste an auth key and click Login or Connect to register
the embedded `tsnet` node. Leave the auth key empty to use the browser sign-in
URL written to the profile log. If Tailscale requires device approval, the log
asks you to approve the device in the Tailscale admin console and the app
continues automatically after approval. Successful auth clears the saved auth key
and marks the profile authenticated until Logout removes the stored `tsnet`
state.

When quitting from the tray or closing the app, Wireproxy GUI calls graceful
shutdown for all active profiles and waits up to five seconds for listeners to
stop before process exit continues.

## Security Notes

- This app stores WireGuard configuration text locally, including private keys.
- Keep exported profile bundles private.
- Prefer binding SOCKS5 listeners to `127.0.0.1` unless you intentionally need a
  listener reachable from another host.
- Review imported profiles before connecting them.

## Project Layout

```text
cmd/wireproxy-gui/      Application entry point
internal/profile/       Profile model, validation, storage, import/export
internal/ui/            Fyne GUI, tray integration, localization
internal/wireproxy/     Embedded wireproxy runner and shutdown handling
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
