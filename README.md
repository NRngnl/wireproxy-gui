# Wireproxy GUI

A small Go desktop app for managing multiple WireGuard-backed SOCKS5 profiles from a GUI and system tray.

## Requirements

- Go 1.26 through mise. This repo pins `go = "1.26.4"` in `.mise.toml`.
- `golangci-lint` through mise for `mise run lint`.
- No separate `wireproxy` binary is required; the app uses `github.com/windtf/wireproxy` as an embedded Go library.

## Run

```sh
mise install
mise run build
./bin/wireproxy-gui
```

For development:

```sh
go run ./cmd/wireproxy-gui
```

Run checks:

```sh
mise run lint
mise run test
```

## Features

- Multiple profiles, each with its own WireGuard config and SOCKS5 bind host/port.
- Connect or disconnect one selected profile, or connect/disconnect all profiles.
- Tray menu for showing the window, per-profile status/info submenus, connecting, disconnecting, and quitting after briefly waiting for active profiles to stop.
- Import WireGuard `.conf` files or exported JSON profile bundles.
- Export the selected profile or all profiles as a JSON bundle.
- Native operating-system file dialogs for import and export.
- Per-profile runtime log view that follows new lines until you scroll up; returning to the bottom resumes following.
- Optional per-profile auto-connect when the app opens.
- In-app usage guide from the Help button.

Profiles are stored in the OS config directory as `wireproxy-gui/profiles.json`.

## Notes

The GUI starts one embedded WireGuard engine per connected profile using the `github.com/windtf/wireproxy` library. It replaces any existing `[Socks5]` section with the selected profile's bind address before parsing and starting the in-process SOCKS5 listener.

```ini
[Socks5]
BindAddress = 127.0.0.1:1080
```

Use a different SOCKS5 bind address on each profile when running multiple connections at the same time.

Import and Export use native file dialogs through `github.com/ncruces/zenity`. macOS and Windows do not need an extra dialog dependency. On Unix-like desktops, install one of `zenity`, `matedialog`, or `qarma` if the native file picker is unavailable.
