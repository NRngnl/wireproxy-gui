# Action Plan: Terminal UI Frontend + Headless Daemon Mode

Status: approved plan, ready for implementation.
Synthesized from two independent planning passes (Claude Opus, Devin SWE)
that used the `tui-design` skill and both read `internal/application`,
`internal/ui`, `docs/architecture.md`, and `cmd/wireproxy-gui/main.go`
before proposing designs. This document reconciles both into one plan.
Source drafts: `.pi/plan-opus.md`, `.pi/plan-swe.md` (kept for reference).

## 1. Goals & Non-Goals

### Goals

1. Add a second, fully-featured frontend — a terminal UI in `internal/tui`
   plus a `cmd/wireproxy-tui` composition root — that depends **only** on
   `internal/application.Service` (via a narrow local interface, the same
   pattern `internal/ui/app.go` already uses), following the exact
   clean-architecture boundary rules in `docs/architecture.md`.
2. Add a headless daemon/service mode — `internal/daemon` +
   `cmd/wireproxy-daemon` — that loads profiles, honors auto-connect,
   manages profile lifecycles with no UI, and shuts down gracefully on
   `SIGTERM`/`SIGINT` via `Service.Shutdown(ctx)`. Ship a systemd unit
   (Linux) and a launchd plist (macOS); document a Windows service option
   as a follow-up, not v1.
3. Both new entrypoints reuse existing `internal/profilejson`,
   `internal/runner`, `internal/wireproxy`, `internal/tailscale` exactly as
   `cmd/wireproxy-gui/main.go` composes them today — no new persistence or
   runtime code paths, no changes to `internal/application`'s public API.
4. Identify and mitigate the shared-`profiles.json` concurrency risk that
   arises once GUI, TUI, and daemon can all point at the same store.

### Non-Goals (v1)

- No client/server split: the TUI does not attach to a remote daemon over
  RPC/socket. It embeds its own `Service`, exactly like the Fyne GUI.
- No full cross-process write-safety protocol. v1 ships an **advisory file
  lock** (hard-blocks daemon-vs-daemon, warns-but-allows for interactive
  GUI/TUI) plus a documented single-writer policy — not a merge/conflict
  system. Full client/server redesign is recorded as a v2 direction.
- No Windows service implementation (design note + doc only).
- No new persistence schema or changes to `internal/profile`,
  `internal/connection`, or `internal/application`'s public method set.
- No macOS notarized daemon package; launchd plist is a template.

## 2. Architecture & Package Layout

```text
cmd/wireproxy-gui/main.go        existing Fyne composition root (unchanged)
cmd/wireproxy-tui/main.go        NEW: TUI composition root
cmd/wireproxy-daemon/main.go     NEW: daemon composition root

internal/tui/                    NEW, sibling inbound adapter to internal/ui
  app.go                         tui.Application interface + Run(core, loadErr) error
  model.go                       root tea.Model, message types, Update/View
  screens/                       list.go, detail.go, editform.go, exitnode.go,
                                  importexport.go, confirm.go, help.go
  keymap.go                      key.Binding sets per screen (bubbles/help)
  styles.go                      Lipgloss styles/theme, NO_COLOR handling
  app_test.go / screens/*_test.go  teatest-based interaction tests

internal/daemon/                 NEW, thin headless inbound adapter
  daemon.go                      Application interface + Run(ctx, core, loadErr, opts) error
  logging.go                     log/slog setup (stdout, optional --json-logs)
  daemon_test.go

internal/lock/                   NEW, advisory cross-process file lock
  lock.go                        TryLock(path) / Probe(path), platform-neutral API
  lock_unix.go                   flock(2)-based impl (build tag unix)
  lock_windows.go                LockFileEx-based impl (build tag windows)
  lock_test.go

packaging/
  systemd/wireproxy-daemon.service       NEW
  launchd/com.github.nrngnl.wireproxy-daemon.plist   NEW
  windows/README.md                      NEW (design note only, v1)

docs/architecture.md    UPDATED: add tui + daemon to the layering diagram/text
docs/tui.md             NEW: keybindings, screens reference
docs/daemon.md          NEW: install/run instructions, concurrency policy
README.md               UPDATED: "Frontends" section (GUI/TUI/daemon) +
                         "Running multiple frontends" concurrency policy
```

**Import allow-list** (enforced by `internal/architecture/architecture_test.go`):
`internal/tui` and `internal/daemon` may import only `internal/application`,
`internal/buildinfo`, `internal/connection`, `internal/profile` — the same
allow-list `internal/ui` already has. They must **never** import
`internal/profilejson`, `internal/wireproxy`, `internal/tailscale`,
`internal/runner`, or `internal/ui`. Only `cmd/*` composition roots wire
concrete adapters. `internal/lock` is a leaf utility package with no
dependency on `internal/application` at all (usable from any `cmd/*`).

`internal/application.Service`'s existing public surface (`Load`, `Profiles`,
`Profile`, `Status`, `Logs`, `RuntimeLocked`, `Add`, `Save`, `Delete`,
`Import`, `Export`, `Connect`, `Login`, `ConnectAll`, `AutoConnect`,
`Disconnect`, `DisconnectAll`, `ExitNodes`, `Logout`, `Shutdown`,
`Changes() <-chan Change`) is sufficient for both new frontends without
modification.

## 3. Framework Choice

**Bubble Tea + Lipgloss + Bubbles + Huh** (the Charm stack). New `go.mod`
deps: `github.com/charmbracelet/bubbletea`, `lipgloss`, `bubbles`, `huh`.

Justification (both independent plans converged here):

- `Service.Changes() <-chan Change` plus explicit `Status`/`Operation` enums
  are already an event-driven state machine. Bubble Tea's Elm-architecture
  `Update(msg) (Model, Cmd)` maps directly onto it: a goroutine bridges
  `Changes()` into `tea.Msg`s via `p.Send(...)`; the model is a pure,
  testable reducer. tview/gocui would need manual `QueueUpdateDraw`-style
  glue from goroutines into the render loop — more error-prone.
- `bubbles/list` (profile list + filtering), `bubbles/viewport` (log
  tail/follow, direct analog of Fyne's `logTail`/scroll-to-bottom logic),
  `bubbles/textinput`/`textarea`, and `bubbles/help` cover this app's needs
  out of the box. `huh` gives validated multi-field forms for the
  add/edit-profile screen.
- Pure Go, no cgo — unlike the Fyne GUI's `libgl1-mesa-dev`/`xorg-dev` CI
  deps. TUI/daemon binaries build and test without GUI system packages.
- `teatest` gives a first-party interaction-testing harness; tview/gocui
  have no equivalent, and this repo's testing convention
  ("behavioral tests live beside the layer that owns the behavior") extends
  cleanly to it.

## 4. TUI UX Design

### Screens

1. **Profile List (home)** — sidebar/list + detail pane split, default screen.
2. **Detail / Log pane** — selected profile's status, bind info, live log
   viewport (follows new lines, pauses on manual scroll-up, resumes on `G`).
3. **Add/Edit Profile Form** (huh-based) — name, backend kind toggle
   (WireGuard/Tailscale), SOCKS bind, WireGuard config textarea or Tailscale
   fields (hostname, auth key, control URL, exit-node mode, allow-LAN,
   ephemeral), port-forward sub-list add/remove.
4. **Exit Node Picker** (modal list, Tailscale only) — `ExitNodes(ctx, id)`
   with a loading/spinner state while in flight.
5. **Import/Export** — path-input prompt (`textinput`) wrapping
   `os.ReadFile`/`os.WriteFile` around `Import(fileName, data)`/`Export(ids)`.
6. **Confirm modal** (delete, logout, quit-while-connected) — reusable.
7. **Help overlay** — `bubbles/help` full keybinding list, toggled by `?`.
8. **Error/toast banner** — transient one-line message for save/validation
   errors and async failures (`Change.Err`), non-blocking.

### States

Loading (startup) · Load error (persistent banner, app continues, same as
Fyne's `loadErr` handling) · Empty (`len(Profiles())==0` → CTA screen) ·
Per-profile: Stopped (gray `○`) · Starting/Stopping (amber `◐`, keys
no-op+toast while `RuntimeLocked(id)`) · Running (green `●`) · Error (red
`!`, last log line inline).

### 80x24 layout sketch

```
┌ wireproxy-gui (tui) ─────────────────────────────── v0.2.3 ─┐
│ Profiles (3)               │ home-wg                          │
│ ● home-wg     127.0.0.1:1080│ WireGuard · 127.0.0.1:1080       │
│ ○ work-ts     127.0.0.1:1081│ Status: ● connected              │
│ ! broken-vpn  127.0.0.1:1082│                                   │
│                             │ ── log (following) ──────────────│
│                             │ 12:04:01 tunnel up                │
│                             │ 12:04:02 socks5 listening :1080   │
├─────────────────────────────┴───────────────────────────────────┤
│ ↑↓ select  c connect  d disconnect  a add  e edit  x delete  ? │
└──────────────────────────────────────────────────────────────┘
```
Degrades below 80 cols by collapsing to single-pane (drop log pane first).
Status glyph colors reuse the Fyne tray icon vocabulary
(`connectedTrayIcon`, `disconnectingTrayIcon`, `errorTrayIcon`).

### Keybindings (canonical — resolves a naming conflict between the two source drafts)

| Key | Action |
|---|---|
| `↑/k` `↓/j` | move selection |
| `c` / `enter` | connect selected |
| `C` | connect all |
| `d` | disconnect selected |
| `D` (shift) | disconnect all |
| `a` | add profile |
| `e` | edit selected |
| `x` / `delete` | delete selected (confirm modal) |
| `n` | exit-node picker (Tailscale only, disabled otherwise) |
| `L` / `O` | Tailscale login / logout |
| `i` / `E` (shift-e) | import / export |
| `g` / `G` | log scroll to top / bottom (resume follow) |
| `/` | filter list |
| `tab` | cycle panes |
| `?` | help overlay |
| `q` / `ctrl+c` | quit TUI process (does **not** stop running connections — TUI has no tray; state this explicitly in a quit confirmation and in `docs/tui.md`) |
| `esc` | cancel/back from modal or form |

Keymaps defined once via `bubbles/key.Binding` so `help` auto-generates the
cheatsheet from the same source of truth as the actual bindings.

### Scriptable / non-interactive mode (`cli-basics.md`)

`cmd/wireproxy-tui` detects a non-TTY stdout (`golang.org/x/term.IsTerminal`)
and refuses to launch the full-screen program, instead supporting:
- `--list` → tab-separated profile/status table to stdout.
- `--connect <name>` / `--disconnect <name>` → one-shot action, exit code
  reflects result.
- `--version` (reuse `internal/buildinfo`).
- `--no-color` forces `lipgloss.Ascii` profile; otherwise respect `NO_COLOR`.
- Exit codes: 0 success, 1 generic error, 2 usage error.

## 5. Daemon Design

### Entry point (`cmd/wireproxy-daemon/main.go`)

Mirrors `cmd/wireproxy-gui/main.go`'s wiring exactly (`profilejson.DefaultStorePath`
→ `runner.New(wireproxy.NewRunner(), tailscale.NewRunner())` →
`application.New(...)` → `core.Load()`), replacing `ui.Run(core, loadErr)`
with `daemon.Run(ctx, core, loadErr, opts)`. Flags: `--store` (override
profile path), `--log-file` / default stdout, `--json-logs`.

```go
func Run(ctx context.Context, core Application, loadErr error, opts Options) error {
    if loadErr != nil { logger.Warn("profile store load", "err", loadErr) }
    if err := core.AutoConnect(); err != nil { logger.Warn("auto-connect", "err", err) }

    ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
    defer stop()

    for {
        select {
        case ch, ok := <-core.Changes():
            if !ok { continue }
            logChange(logger, ch)
        case <-ctx.Done():
            shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            defer cancel()
            return core.Shutdown(shutdownCtx)
        }
        // SIGHUP handling: on signal.NotifyContext delivery of SIGHUP specifically,
        // call core.Load() again + core.AutoConnect() for newly-added auto-connect
        // profiles, without exiting the select loop. (Implementer: use a dedicated
        // signal.Notify channel for SIGHUP separately from the SIGTERM/SIGINT
        // NotifyContext, since NotifyContext only fires once then stops listening.)
    }
}
```

`daemon.Application` is a narrow local interface (`AutoConnect`, `Changes`,
`Shutdown`, `Profiles`, `Status`, `Logs`) — same pattern as `ui.Application`.

- **SIGTERM/SIGINT**: cancel context → `core.Shutdown(shutdownCtx)` (10s
  bound, reuse/align with the existing `shutdownWaitTimeout` pattern in
  `internal/ui/app.go`) → exit 0 on success, non-zero on error/timeout.
- **SIGHUP**: reload profile store (`core.Load()`) + re-run `AutoConnect()`
  for newly-added auto-connect profiles — this is the daemon's designated
  way to pick up out-of-band store edits (see concurrency policy, §6).
- **Logging**: `log/slog` to stdout by default (systemd/launchd capture
  stdout/stderr into journal/log files declared in the unit — no in-process
  file rotation needed), one line per `Change` (profile ID, operation,
  status transition, error if any). `--json-logs` selects
  `slog.NewJSONHandler` for log aggregation; default is plain text matching
  `LogEntry`'s existing convention.

### systemd unit — `packaging/systemd/wireproxy-daemon.service`

```ini
[Unit]
Description=wireproxy-gui headless daemon (profile manager)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/wireproxy-daemon
Restart=on-failure
RestartSec=2
KillSignal=SIGTERM
TimeoutStopSec=15
# Run as a per-user unit (systemctl --user) by default so it shares
# $XDG_CONFIG_HOME/wireproxy-gui/profiles.json with the desktop GUI for the
# same user. For unattended/server hosts needing root-level tun access,
# install as a system unit instead with an explicit --store path; document
# both in docs/daemon.md. Verify actual tun/capability requirements
# empirically before finalizing (Task 9).
# User=wireproxy-gui
# AmbientCapabilities=CAP_NET_ADMIN

[Install]
WantedBy=default.target
```
(`Type=notify`/`sdnotify` integration is an optional nice-to-have inside
Task 9, not required for v1 — `Type=simple` is an accepted fallback.)

### launchd plist — `packaging/launchd/com.github.nrngnl.wireproxy-daemon.plist`

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.github.nrngnl.wireproxy-daemon</string>
  <key>ProgramArguments</key>
  <array><string>/usr/local/bin/wireproxy-daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>~/Library/Logs/wireproxy-daemon/stdout.log</string>
  <key>StandardErrorPath</key><string>~/Library/Logs/wireproxy-daemon/stderr.log</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
```
Install under `~/Library/LaunchAgents/` (per-user) by default for the same
profile-sharing reason as the systemd user unit; document
`/Library/LaunchDaemons` (root, system-wide) as the alternative. launchd
sends SIGTERM on unload by default — no extra plist config needed.

### Windows (design note only, v1)

`golang.org/x/sys/windows/svc` (already indirect via wireguard-windows deps)
wraps the same `daemon.Run` loop; SCM stop/shutdown control requests
translate to the same `context.Cancel` path. Documented in
`packaging/windows/README.md`, not implemented in v1.

## 6. Concurrency / Shared-State Risk & Mitigation

**Confirmed finding**: repo-wide grep for `flock|lockfile|LOCK|advisory`
returns zero matches. `internal/profilejson.Repository` has no cross-process
locking — only an in-process mutex that protects one process's own state.
If GUI + TUI + daemon run concurrently against the same `profiles.json`:

1. **Silent last-writer-wins data loss** on `Save`/`Delete`/`Import` — each
   process serializes its whole in-memory profile list; a save from process
   A after process B's independent edit clobbers B's changes with no
   conflict detection.
2. **Possible torn writes** if `profilejson.Repository.Save` does not
   already write via temp-file + atomic rename (verify in Task 3 — not
   confirmed during planning).
3. **Duplicate bind-port runtime conflicts** are only guarded *within* one
   process's in-memory state (`runningBindConflictLocked`); two processes
   can independently attempt the same SOCKS port and fail at the OS
   bind-syscall level with a generic error.
4. **Stale status views** — each process only knows about connections *it*
   started; the GUI can show "stopped" for a profile the daemon is actively
   running.

**v1 mitigation: advisory file lock + documented single-writer policy**
(not a full client/server redesign — that is recorded as a v2 direction):

- New `internal/lock` package: `TryLock(path) (Lock, bool, error)`
  (non-blocking exclusive attempt) and `Probe(path) (held bool, err error)`,
  with `flock(2)` on Unix and `LockFileEx` on Windows.
- **Daemon**: takes an exclusive lock on `<store-dir>/.lock` for its whole
  lifetime. A second daemon process against the same store **hard-fails**
  at startup with a clear "daemon already running" message and non-zero
  exit — daemon-vs-daemon is the one case worth a hard block.
- **GUI/TUI**: probe the same lock at startup; if held (daemon running),
  show a non-blocking warning ("a wireproxy-gui daemon is managing this
  profile store; edits made here may be overwritten — stop the daemon
  first") but still launch. This is advisory, not enforced, to avoid
  regressing today's ability to run the GUI more than once.
- **Atomic writes regardless of locking**: `profilejson.Repository.Save`
  must write via temp-file-in-same-dir + `os.Rename` (Task 3 audits and
  fixes this if not already the case) — eliminates torn writes for the
  single-writer-at-a-time case independent of the lock story.
- **Documented policy** (`docs/daemon.md` + README "Running multiple
  frontends"): run at most one of {GUI, TUI, daemon} against a given store
  at a time for *editing*; the daemon is intended to replace GUI/TUI for
  unattended/headless use, not run alongside them against the same store.

## 7. Ordered Implementation Tasks

Dependency graph: **Task 0** is the sole hard prerequisite for everything.
After Task 0, three independent tracks can run in parallel:
- **TUI track**: 1 → 2 → {3, 4} → 5
- **Daemon track**: 6 → 7
- **Lock/persistence track**: 8 (needs Task 0 only), then Task 7/2 integrate it in Task 9
Tasks 10 (CI) and 11 (docs) are integration/polish, depend on the tracks above.

### Task 0 — Architecture scaffolding & guardrails
Files: `internal/architecture/architecture_test.go`, `go.mod`/`go.sum`,
`docs/architecture.md`.
Work: add `tui` and `daemon` entries to the import allow-list rules
(`{"application", "buildinfo", "connection", "profile"}`); add stub
`internal/tui/doc.go` and `internal/daemon/doc.go` packages so the test has
something to check; `go get` the Charm deps
(`bubbletea`, `lipgloss`, `bubbles`, `huh`); update the architecture diagram
and prose for the two new adapters and `cmd/wireproxy-tui`/`cmd/wireproxy-daemon`.
Acceptance: `go build ./...` succeeds; `go test ./internal/architecture/...`
passes with the new rules, and fails if a forbidden import is temporarily
added (verify, then remove); docs updated and internally consistent.

### Task 1 — `internal/tui` core: root model, Service wiring, profile list
Depends on: Task 0.
Files: `internal/tui/app.go`, `internal/tui/model.go`,
`internal/tui/screens/list.go`, `internal/tui/keymap.go`, `internal/tui/styles.go`.
Work: define `tui.Application` interface (mirrors `ui.Application`); root
`tea.Model` with `Init`/`Update`/`View`; goroutine bridging
`core.Changes()` into `tea.Msg`s; sidebar profile list (`bubbles/list`) with
status glyph/color per `application.Status`; empty-state screen.
Acceptance: renders against a fixture store; `↑/↓` navigation and `q` quit
work; `internal/tui/model_test.go` (fake `Application` double or `teatest`)
asserts `Update` transitions on `Change` messages.

### Task 2 — `cmd/wireproxy-tui` composition root + scripting flags
Depends on: Task 1 (interface signature; can stub-first in parallel).
Files: `cmd/wireproxy-tui/main.go`.
Work: copy `cmd/wireproxy-gui/main.go`'s wiring, swap in `tui.Run(core,
loadErr)`; add `--store`, TTY-detection guard, `--list`/`--connect <name>`/
`--disconnect <name>`/`--version`/`--no-color` scripting flags per §4.
Acceptance: `go build -o bin/wireproxy-tui ./cmd/wireproxy-tui` succeeds;
`go vet ./...` clean; `--list < /dev/null` prints a table and exits 0
without launching the full-screen program; manual smoke run against empty
and populated stores renders without panic.

### Task 3 — `internal/profilejson` atomic-write audit + fix
Depends on: Task 0 only (independent of TUI/daemon code).
Files: `internal/profilejson/profilejson.go`, `internal/profilejson/profilejson_test.go`.
Work: read `Repository.Save` in full; if it does not already write via
temp-file-in-same-dir + `os.Rename`, change it to do so; add a test
simulating a crash-mid-write that asserts the original file is untouched.
Acceptance: `go test ./internal/profilejson/...` passes; forced
partial-write scenario leaves `profiles.json` byte-identical to its
pre-write state.

### Task 4 — TUI: connect/disconnect + detail pane + live log viewport
Depends on: Task 1.
Files: `internal/tui/screens/detail.go` (uses `bubbles/viewport`), edits to
`internal/tui/model.go`.
Work: detail pane (kind/bind/status); log viewport consuming `core.Logs(id)`
on selection plus live-appended `LogEntry`s via `Change`; follow/pause-on-
scroll matching Fyne's `logTail` behavior; wire `c`/`C`/`d`/`D` to
`Connect`/`ConnectAll`/`Disconnect`/`DisconnectAll`, no-op+toast when
`RuntimeLocked(id)`.
Acceptance: status glyph transitions starting→running in test harness ticks
against a fake `Application`; log viewport appends and auto-scrolls unless
user has scrolled up (covered by a test simulating scroll-then-append).

### Task 5 — TUI: add/edit/delete forms, exit-node picker, import/export, help
Depends on: Task 1 (can start after Task 4 lands for shared confirm/toast
plumbing, but is not blocked by it).
Files: `internal/tui/screens/editform.go` (huh-based), `internal/tui/screens/confirm.go`,
`internal/tui/screens/exitnode.go`, `internal/tui/screens/importexport.go`,
`internal/tui/screens/help.go`.
Work: `a`/`e` open a `huh.Form` (empty for add-then-edit matching
`Service.Add(name)` semantics, pre-filled for edit) submitting via
`Save(updated)`, inline validation-error display for domain errors
(`ErrDuplicateBindAddress`, etc.); `x` opens confirm modal → `Delete(id)`;
`n` opens exit-node picker with `ExitNodes(ctx, id)` async + spinner state,
selecting calls `Save`; `L`/`O` call `Login`/`Logout`; `i`/`E` open
path-input prompts wrapping `os.ReadFile`/`os.WriteFile` around
`Import`/`Export`; `?` toggles `bubbles/help` overlay.
Acceptance: validation errors render as inline field errors, not raw Go
error dumps; delete requires confirm keypress; exit-node picker doesn't
freeze the UI while `ExitNodes` is in flight (runs in a `tea.Cmd`); import/
export round-trips a fixture JSON bundle identically to existing
`profilejson_test.go` fixtures.

### Task 6 — `internal/daemon` core: run loop, AutoConnect, signal-driven shutdown
Depends on: Task 0.
Files: `internal/daemon/daemon.go`, `internal/daemon/logging.go`.
Work: `daemon.Application` narrow interface; `Run(ctx, core, loadErr, opts)`
per §5 (AutoConnect on start, Changes()-driven slog logging, SIGTERM/SIGINT
→ bounded `Shutdown`, SIGHUP → reload + re-AutoConnect).
Acceptance: unit test with a fake `Application` double asserts `AutoConnect`
called once at startup; canceled `ctx` triggers exactly one `Shutdown(ctx)`
call with a bounded timeout; `Changes()` messages produce logger output
(assert on logger sink, not stdout); a simulated SIGHUP triggers a second
`Load`+`AutoConnect` pair.

### Task 7 — `cmd/wireproxy-daemon` composition root
Depends on: Task 6.
Files: `cmd/wireproxy-daemon/main.go`.
Work: flags (`--store`, `--log-file`, `--json-logs`); `signal.NotifyContext`
wiring (SIGTERM/SIGINT) plus separate `signal.Notify` for SIGHUP; calls
`daemon.Run`.
Acceptance: `go build -o bin/wireproxy-daemon ./cmd/wireproxy-daemon`
succeeds; manual run responds to `kill -TERM <pid>` with a clean shutdown
log line and exit 0 within the timeout; `kill -HUP <pid>` triggers a logged
reload without exiting.

### Task 8 — `internal/lock` advisory file lock package
Depends on: Task 0 only (independent of TUI/daemon tracks — can run fully
in parallel with 1–7).
Files: `internal/lock/lock.go`, `internal/lock/lock_unix.go`,
`internal/lock/lock_windows.go`, `internal/lock/lock_test.go`.
Work: `TryLock(path string) (Lock, bool, error)` (non-blocking exclusive),
`Probe(path string) (held bool, err error)`; `flock`/`LockFileEx` impls
behind build tags.
Acceptance: test acquires a lock in-process, a second attempt (simulated
second holder — separate fd/goroutine) observes `Probe`'s `held == true`;
releasing frees it for a subsequent `TryLock`.

### Task 9 — Lock integration + packaging (systemd/launchd) + install docs
Depends on: Task 2, Task 7, Task 8 (needs all three binaries + the lock
package to exist).
Files: `cmd/wireproxy-daemon/main.go`, `cmd/wireproxy-gui/main.go`,
`cmd/wireproxy-tui/main.go`, `internal/ui/app.go` (startup warning),
`internal/tui/app.go` (startup warning banner),
`packaging/systemd/wireproxy-daemon.service`,
`packaging/launchd/com.github.nrngnl.wireproxy-daemon.plist`,
`packaging/windows/README.md`, `docs/daemon.md`.
Work: daemon takes `lock.TryLock` at startup, hard-fails if already held;
GUI/TUI call `lock.Probe` at startup, show non-blocking warning if held;
write the unit/plist templates from §5 verbatim; `docs/daemon.md` covers
install, SIGHUP reload, the single-frontend-at-a-time policy, and
permission/tun-device troubleshooting notes (verify actual capability
requirements empirically before finalizing wording).
Acceptance: two `wireproxy-daemon` processes against the same store — the
second exits non-zero with a clear message; GUI/TUI launch successfully
(with a warning) while a daemon holds the lock; `systemd-analyze verify` (or
manual review) passes on the unit file; `plutil -lint` passes on the plist.

### Task 10 — CI/build/release integration
Depends on: Task 2, Task 7, Task 9.
Files: `.github/workflows/ci.yml`, `.github/workflows/release.yml`,
`scripts/build-release.sh`.
Work: add `go build ./cmd/wireproxy-tui/...` and `./cmd/wireproxy-daemon/...`
to the CI build matrix (pure-Go, no cgo/X11 deps needed for these two);
extend `scripts/build-release.sh` to package both new binaries plus the
Task 9 packaging templates into release artifacts per target OS/arch.
Acceptance: CI green on a PR touching only `internal/tui`/`internal/daemon`/
`internal/lock`; `go vet ./...` and `go test -count=1 ./...` pass including
new packages; a manual `scripts/build-release.sh` run produces
`wireproxy-tui` and `wireproxy-daemon` binaries alongside the existing
`wireproxy-gui` package layout.

### Task 11 — Docs: README, docs/tui.md, docs/daemon.md finalization
Depends on: Tasks 1–10 (final pass once everything lands; can draft in
parallel earlier and reconcile at the end).
Files: `README.md`, `docs/tui.md`, `docs/daemon.md`, `docs/architecture.md`.
Work: README "Frontends" section (GUI/TUI/daemon) + "Running multiple
frontends" concurrency policy (§6); `docs/tui.md` full keybinding table +
ASCII layout; `docs/daemon.md` install + concurrency policy finalized
against actual implementation.
Acceptance: docs reviewed for accuracy against the merged implementation;
no dangling references to unimplemented flags/files; `ripwire_doc_drift` (or
equivalent) shows no new drift introduced by this doc set.

## 8. Testing Strategy

1. **Domain/application layer** — unchanged, already covered by existing
   `internal/profile`, `internal/application` test suites; both new
   frontends inherit this safety net for free.
2. **Adapter unit tests (bulk of new investment)** — `internal/tui` and
   `internal/daemon` `Update`/`Run` logic tested against hand-written fake
   `Application` doubles (not the real `Service`), validating state-machine
   correctness without a real terminal or real backends.
3. **Golden/snapshot view tests (narrow)** — `teatest` or manual `View()`
   string assertions for a small number of key screens (dashboard,
   empty-state, add-form, error banner) at 80x24. Not the primary
   correctness mechanism — used sparingly to catch layout regressions.
4. **Integration smoke tests (very narrow)** — one or two tests wiring a
   real `application.Service` + real `profilejson.Repository` against a
   temp dir, driving add→connect→disconnect→delete via simulated `tea.Msg`
   key sequences.
5. **Lock package tests** — real filesystem lock contention, in-process
   (two goroutines/fds).
6. **Daemon signal tests** — fake `Application` double; a small number of
   true subprocess `kill -TERM`/`kill -HUP` tests optionally guarded by a
   `//go:build integration` tag (slower/flakier in CI sandboxes).
7. **Architecture boundary test** — extended in Task 0, runs on every CI
   build; the cheapest, highest-leverage guard against either new package
   reaching into `profilejson`/`runner` directly.
8. **Manual/exploratory pass** — real 80x24 terminal, wider/narrower resize,
   and over SSH, once after Task 5 and once after Task 9; also a manual
   multi-process run (GUI + TUI + daemon together) to confirm Task 9's lock
   warnings behave as documented.

## 9. Open Risks / Questions (carried forward for implementers)

1. **`AutoConnect()`'s exact call site in the Fyne path** (`ui.Run` itself
   vs `cmd/wireproxy-gui/main.go` before `ui.Run`) needs confirming before
   Task 6 so the daemon calls it at the behaviorally-equivalent point.
2. **`huh` vs hand-rolled `bubbles/textinput` forms** — if `huh`'s
   validation-error UX doesn't map cleanly onto `profile` package's typed
   domain errors, Task 5 may need a hand-rolled fallback. Decide
   empirically during Task 5, not upfront.
3. **Windows TUI/daemon demand** — confirm whether Windows binaries for TUI
   are wanted now (the project ships Windows GUI builds) or should be
   Unix-first for v1 with Windows added once demand is confirmed.
4. **Headless TUN/capability requirements per OS**, especially macOS
   without an interactive session (no keychain/permission-dialog UI
   available to a launchd daemon) — needs empirical verification before
   finalizing the launchd unit design in Task 9; a daemon that needs an
   interactive permission prompt on first TUN creation is a blocking
   discovery, not a footnote.
5. **`profile.Profile`'s exact auto-connect field name/shape** — confirm
   against `internal/profile/profile.go` before wiring the TUI's
   auto-connect toggle in Task 5.
6. **v1's advisory-lock approach does not prevent concurrent writes, only
   warns about them.** If judged insufficient by review, the real fix is a
   full client/server split — scope that as an explicit v2 epic, not folded
   into this task list.
7. **No control surface on the daemon in v1** (no socket/RPC) — "restart a
   single profile without restarting the whole daemon" is a natural v2 ask;
   named here as a known gap, not silently absent.
8. **`Type=notify`/`sdnotify` for systemd** is optional in Task 9;
   `Type=simple` is an accepted fallback if the extra protocol work is
   deferred.
</content>
