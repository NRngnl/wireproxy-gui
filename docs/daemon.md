# wireproxy-daemon

`wireproxy-daemon` is a headless (no UI) frontend for the same
`internal/application.Service` core used by `wireproxy-gui` and
`wireproxy-tui`. It is intended for unattended/server use where no
interactive GUI or terminal session is available.

## What it does

- Loads the profile store on startup and calls `AutoConnect()` for any
  profiles configured to connect automatically.
- Runs until it receives `SIGTERM` or `SIGINT`, at which point it performs
  a graceful shutdown (`core.Shutdown` with a bounded timeout) and exits.
- Handles `SIGHUP` as a best-effort "reload" signal: it re-reads the
  profile store (`core.Load()`) and re-runs `AutoConnect()` for
  newly-added auto-connect profiles, without restarting the process
  (`internal/daemon/daemon.go`). This is the daemon's designated way to
  pick up out-of-band edits to `profiles.json` (for example a change made
  via the GUI or TUI, or by hand) without a full restart. Each step is
  logged independently (`reload requested (SIGHUP)`, then the outcome of
  the reload and of auto-connect), so a partial failure in one step
  (e.g. a malformed store file) does not silently mask success or failure
  of the other.
- Logs one line per profile state change (`log/slog`) to stdout by
  default, or to a file via `--log-file`. Pass `--json-logs` to emit JSON
  lines instead of plain text, which is convenient for log aggregation.

## Flags

| Flag          | Default                             | Meaning                              |
|---------------|--------------------------------------|---------------------------------------|
| `--store`     | `profilejson.DefaultStorePath()`     | Path to the profile store JSON file.  |
| `--log-file`  | *(empty, meaning stdout)*            | Path to write logs to.                |
| `--json-logs` | `false`                              | Emit logs as JSON lines instead of text. |

## Building / installing the binary

```sh
go build -o wireproxy-daemon ./cmd/wireproxy-daemon
sudo install -m 0755 wireproxy-daemon /usr/local/bin/wireproxy-daemon
```

## Installing as a service (automated, from the GUI or TUI)

As of this feature, `wireproxy-gui` and `wireproxy-tui` can install and
uninstall `wireproxy-daemon` as a **per-user** login-time service
themselves — no manual file copying, no `systemctl`/`launchctl` commands.
Both frontends call the same shared `internal/svcinstall` package, so
their behavior is identical:

- **GUI**: open the tray menu (or the main window if tray support isn't
  available) and choose **"Enable Start on Login"**. The menu item
  toggles to **"Disable Start on Login"** once installed, reflecting live
  status. Disabling asks for confirmation first, since it stops a service
  you may depend on for unattended operation; enabling does not, matching
  the existing "Connect All" precedent.
- **TUI**: press `S` to open the Start on Login panel. It shows current
  status (Installed/Enabled/Running) and offers a single `y`/`N`-confirmed
  action (install if not installed, uninstall if installed). Press `esc`
  to close without acting.

**This automated path is per-user only** — it writes a `systemctl --user`
unit on Linux or a `~/Library/LaunchAgents` plist on macOS, using
whichever `wireproxy-daemon` binary it finds next to the calling GUI/TUI
binary or on `$PATH`. It never writes a system-wide unit, never touches
`/etc/systemd/system` or `/Library/LaunchDaemons`, and never prompts for
elevated privileges. If the profile store's advisory lock (see
[Concurrency / single-writer policy](#concurrency--single-writer-policy)
below) is currently held by another process, installing warns first and
offers to proceed anyway rather than silently racing it. If you need a
system-wide/unattended-server install instead, use the fully manual
instructions below — the automated GUI/TUI path does not support that
case and does not replace it.

Uninstalling via the GUI/TUI stops the service (if running), disables it,
and removes the unit/plist file. It does **not** delete
`~/Library/Logs/wireproxy-daemon/` on macOS — uninstalling the service is
not "delete my logs."

## Installing as a systemd user service manually (Linux)

The unit template lives at `packaging/systemd/wireproxy-daemon.service`.
Use this manual path for a system-wide/unattended-server install, for a
host without a GUI/TUI binary available, or if you simply prefer to
manage the unit file yourself.

```sh
mkdir -p ~/.config/systemd/user
cp packaging/systemd/wireproxy-daemon.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now wireproxy-daemon
```

This runs the daemon as a per-user unit, which means it shares
`$XDG_CONFIG_HOME/wireproxy-gui/profiles.json` (or the platform-equivalent
default store path) with the desktop GUI for the same user account — no
`--store` override needed for the common case.

For unattended/server hosts that need root-level `tun` device access
instead, install the same unit file as a **system** unit
(`/etc/systemd/system/wireproxy-daemon.service`) with an explicit
`--store` path in `ExecStart` (a per-user profile store under a service
account's home directory, or a shared system path), and consider
uncommenting the `User=`/`AmbientCapabilities=CAP_NET_ADMIN` lines in the
unit rather than running the service as root. This repository has not
empirically verified the exact capability set required for headless `tun`
creation on every distribution — see Troubleshooting below.

Check status/logs with:

```sh
systemctl --user status wireproxy-daemon
journalctl --user -u wireproxy-daemon -f
```

## Installing as a launchd agent manually (macOS)

The plist template lives at
`packaging/launchd/com.github.nrngnl.wireproxy-daemon.plist`. Use this
manual path for the same reasons noted in the systemd section above — for
the common per-user case, prefer the automated GUI/TUI install.

```sh
mkdir -p ~/Library/LaunchAgents
sed "s|__HOME__|$HOME|g" packaging/launchd/com.github.nrngnl.wireproxy-daemon.plist > ~/Library/LaunchAgents/com.github.nrngnl.wireproxy-daemon.plist
mkdir -p ~/Library/Logs/wireproxy-daemon
launchctl load ~/Library/LaunchAgents/com.github.nrngnl.wireproxy-daemon.plist
```

`launchd` does not expand `~` or environment variables in plist string
values at load time, so the packaged plist template uses a `__HOME__`
placeholder in `StandardOutPath`/`StandardErrorPath` instead of a literal
`~`. The `sed` substitution above replaces `__HOME__` with your actual
absolute home directory *before* the plist is copied into place, so the
log paths `launchd` sees are real absolute paths. Run the substitution
first, then create the log directory, then load the agent — loading the
plist before the log directory exists is harmless (`launchd` creates the
log files on first write), but the log directory must exist before the
daemon actually starts writing to it.

This installs a per-user `LaunchAgent`, sharing the same per-user profile
store as the desktop GUI, for the same reason as the systemd user unit
above. `/Library/LaunchDaemons` (system-wide, root) is the alternative for
unattended/server hosts; the tradeoffs are the same as the systemd
system-unit case.

`launchd` sends `SIGTERM` on `launchctl unload` by default, so no extra
plist configuration is needed for graceful shutdown.

To stop and remove:

```sh
launchctl unload ~/Library/LaunchAgents/com.github.nrngnl.wireproxy-daemon.plist
```

## Windows

Neither the daemon's Windows Service Control Manager integration nor the
automated GUI/TUI service install (`internal/svcinstall`) is implemented
for Windows in v1 — see `packaging/windows/README.md` for the intended
design (an `x/sys/windows/svc` wrapper around the same `daemon.Run` loop).
On Windows, the GUI/TUI's "Start on Login" action is disabled/unavailable
(`internal/svcinstall.New()` returns an unsupported stub on every OS other
than Linux and macOS). The `wireproxy-daemon` binary can still be built
and run in the foreground on Windows for manual use.

## Concurrency / single-writer policy

`profiles.json` has no cross-process transaction protocol — each frontend
(`wireproxy-gui`, `wireproxy-tui`, `wireproxy-daemon`) loads the whole
store into memory and writes the whole store back out. Running more than
one of them against the same store at the same time risks last-writer-wins
data loss on concurrent edits (see plan §6 for the full risk writeup).

v1 mitigation is an **advisory file lock**, not a full client/server
redesign:

- The daemon takes an exclusive advisory lock on `<store-path>.lock` for
  its entire lifetime. A **second daemon** process started against the
  same store **hard-fails** at startup with a clear
  `"another daemon instance already holds the lock"` message and a
  non-zero exit code — daemon-vs-daemon is the one case that is actively
  blocked.
- The **GUI and TUI** probe the same lock at startup. If it is held (a
  daemon is running against that store), they print a **non-blocking
  warning** to stderr and continue launching normally — this is advisory
  only, so as not to regress today's ability to run the GUI more than
  once, or to run the GUI/TUI alongside a daemon for read-only inspection.

**Policy**: run at most one of {GUI, TUI, daemon} against a given profile
store at a time for *editing*. The daemon is meant to replace the GUI/TUI
for unattended/headless use of a given store, not to run alongside them
against the same store. If you need to inspect status from a TUI or GUI
while a daemon manages connections, be aware that any edits you make in
that second frontend may be silently overwritten once the daemon's
in-memory state is next saved.

The same advisory lock backs the automated service **install** action
(`internal/svcinstall`) described above: installing while another process
holds the lock warns and asks for confirmation before proceeding, rather
than silently installing a service that will contend for the same lock at
next login. **Uninstall never checks the lock** — stopping and disabling a
service is always safe to attempt regardless of who currently holds it.

## Troubleshooting: TUN / permission requirements for headless operation

**This has not been empirically verified.** The exact capabilities or
permissions required to create a `tun`/`utun` device without an
interactive session vary by OS and are not confirmed by this project as of
this writing:

- On Linux, creating a `tun` device typically requires `CAP_NET_ADMIN` (or
  root). The commented-out `User=`/`AmbientCapabilities=CAP_NET_ADMIN`
  lines in `packaging/systemd/wireproxy-daemon.service` are a starting
  point, not a verified-working configuration — test on your target
  distribution before relying on them in production.
- On macOS, `utun` creation from a `launchd` agent/daemon running without
  an interactive user session (no keychain/permission-dialog UI available)
  is **unverified**. If wireproxy or the underlying WireGuard userspace
  implementation requires any prompt-driven permission grant on first use,
  a headless `launchd` daemon may not be able to satisfy it, which would
  be a blocking discovery for unattended macOS use — not just a footnote.
  Confirm this empirically for your macOS version before deploying
  `wireproxy-daemon` as an unattended LaunchDaemon.
- If the daemon exits immediately or fails to bring up a tunnel with a
  permission-denied-style error, check the systemd unit's/launchd plist's
  captured stdout/stderr log (`journalctl --user -u wireproxy-daemon` or
  `~/Library/Logs/wireproxy-daemon/stderr.log`) for the underlying
  wireproxy/WireGuard error before assuming it is a `wireproxy-gui`-level
  bug.
