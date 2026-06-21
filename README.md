# Wireproxy GUI

Wireproxy GUI is a small Go/Fyne desktop app for running WireGuard-backed
SOCKS5 proxies from a native GUI and system tray. It manages multiple saved
profiles, starts each connection in-process through the `github.com/windtf/wireproxy`
library, and does not shell out to the `wireproxy` command-line tool.

The app is intended for users who already have WireGuard profile files and want
each profile exposed as a local SOCKS5 listener, such as `127.0.0.1:1080`.

## Features

- Add profiles manually or import WireGuard `.conf` files.
- Import and export JSON profile bundles.
- Run multiple profiles at the same time with separate SOCKS5 bind addresses.
- Connect or disconnect one profile, or connect/disconnect every saved profile.
- Show profile status in the sidebar and tray menu.
- Show the SOCKS5 bind address and WireGuard interface address for each profile.
- Follow each profile log by default, with scrollback preserved when you scroll up.
- Auto-connect selected profiles when the app opens.
- Use native file dialogs for import and export.
- Hide to tray on window close when tray support is available.
- Stop active profiles during app shutdown and wait briefly for listeners to close.

## Requirements

- Go 1.26.4.
- macOS, Linux, or Windows with GUI support from Fyne.
- `mise` is optional, but the repo includes `.mise.toml` for tool pinning.
- No separate `wireproxy` binary is required.

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

## Import And Export

Import accepts:

- WireGuard `.conf` files.
- JSON files previously exported by Wireproxy GUI.
- JSON arrays or single JSON profile objects with compatible fields.

Export writes a JSON bundle:

```json
{
  "version": 1,
  "profiles": []
}
```

Exported files are written with `0600` permissions where supported because they
may contain WireGuard private keys.

## Runtime Behavior

Each connected profile owns an embedded WireGuard engine and SOCKS5 listener.
The app tracks profile states as disconnected, connecting, connected,
disconnecting, or error. Profile configuration and SOCKS5 bind address are
locked while a profile is active; disconnect the profile before changing runtime
connection settings.

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
