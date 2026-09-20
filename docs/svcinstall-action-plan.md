# Action Plan: Service Install/Uninstall Automation (User-Level Only)

Status: approved plan, ready for implementation.
Synthesized from two independent planning passes (Claude Opus, Devin SWE),
both of which read `docs/tui-daemon-action-plan.md`, `docs/daemon.md`,
`internal/ui/app.go`, `internal/tui/model.go`, `internal/lock`, and the
`packaging/` templates before proposing designs. Source drafts:
`.pi/plan-svcinstall-opus.md`, `.pi/plan-svcinstall-swe.md`.

**Scope constraint (explicit operator decision): user-level (per-user)
service install/uninstall ONLY.** No system-wide systemd unit
(`/etc/systemd/system`), no `/Library/LaunchDaemons`, no elevation/`sudo`/
`pkexec`/UAC prompt anywhere in this feature. The existing manual
system-wide instructions in `docs/daemon.md` remain the only way to do
that; this feature does not touch, replace, or extend them.

## 1. Goals & Non-Goals

### Goals

1. Let a user click "Enable start on login" in the Fyne GUI tray menu, or
   press a key in the TUI, and have `wireproxy-daemon` installed as a
   **per-user** login-time OS service (`systemctl --user` on Linux,
   `launchd LaunchAgent` on macOS) — no shell, no manual file copying, no
   `systemctl`/`launchctl` invocation by the user.
2. Symmetric, idempotent **uninstall**: stop the running instance (if
   any), disable it, remove the unit/plist file.
3. GUI and TUI call **one shared package**, `internal/svcinstall`, for
   all OS-specific logic. Neither frontend re-implements systemd/launchd
   file generation or CLI invocation.
4. Linux and macOS are v1, fully implemented. Windows
   (`golang.org/x/sys/windows/svc`/`svc/mgr`, already an indirect
   dependency per `go.mod`) is a stretch goal (Task 10), structured so it
   can land later without reshaping the shared package's public API.
5. Unit/plist template content is embedded in the GUI/TUI binaries via
   `//go:embed`, so an installed binary works without `packaging/`
   existing on disk next to it.

### Non-Goals (v1)

1. **System-wide/elevated install is entirely out of scope** — not
   deferred-with-a-flag, not a "v2 idea to design later inside this
   package." No code path in `internal/svcinstall` ever targets
   `/etc/systemd/system`, `/Library/LaunchDaemons`, or a Windows service
   running as `LocalSystem`. If a user needs that, `docs/daemon.md`'s
   existing manual instructions are the only path, unchanged by this
   work.
2. No auto-building/downloading of the `wireproxy-daemon` binary itself —
   `Install` resolves an existing binary (§6) and fails with a clear
   error if none is found.
3. No background drift-reconciliation of a hand-edited installed unit.
   `Uninstall` then `Install` is the supported reset path.
4. No log-file cleanup on `Uninstall` in v1 (journald logs aren't a file
   this package could delete anyway; launchd's plain-file logs are
   deliberately left in place — uninstalling isn't "delete my history").
5. No multiple named service instances per profile store — one
   per-user-account service bound to the default store path, matching
   `wireproxy-daemon`'s own current default-store behavior.
6. Migrating away the manual `docs/daemon.md` instructions — they stay as
   the documented path for anyone without a GUI/TUI binary at all (a bare
   server checkout), and as the *only* path for system-wide install
   (Non-Goal 1).

## 2. Architecture & Package Layout

```text
internal/svcinstall/
  doc.go                  // package doc + platform support matrix
  svcinstall.go            // Installer interface, Status, Options, sentinel errors, New()
  svcinstall_linux.go       // systemd --user backend (build tag linux)
  svcinstall_darwin.go      // launchd LaunchAgent backend (build tag darwin)
  svcinstall_windows.go     // stub returning ErrUnsupported in v1 (Task 10 fills this in later)
  svcinstall_other.go       // !linux && !darwin && !windows stub
  binpath.go               // daemon-binary resolution (OS-agnostic)
  binpath_test.go
  templates.go              // //go:embed + text/template rendering
  templates_test.go         // golden-file tests
  templates/
    wireproxy-daemon.service.tmpl
    com.github.nrngnl.wireproxy-daemon.plist.tmpl
  testdata/
    *.golden
  svcinstall_linux_test.go   // fake-runner unit tests, no real systemctl
  svcinstall_darwin_test.go  // fake-runner unit tests, no real launchctl
  svcinstall_test.go         // platform-independent: Options, lock-interaction, New()
  sync_test.go                // packaging/ vs templates/ parity check (see §2.2)
```

`internal/svcinstall` depends only on `internal/lock`, `internal/profilejson`
(for `DefaultStorePath()`), and `internal/profile` — no dependency on
`internal/application`, `internal/ui`, `internal/tui`, `internal/daemon`,
`internal/wireproxy`, or `internal/tailscale`. Add to
`internal/architecture/architecture_test.go`'s rules map:

```go
"svcinstall": {"lock", "profilejson", "profile"},
"tui":        {"application", "buildinfo", "connection", "profile", "svcinstall", "tui/screens"},
"ui":         {"application", "buildinfo", "connection", "profile", "svcinstall"},
```

### 2.1 Import-boundary decision: leaf package, imported directly by both `ui` and `tui`

**Not** hidden behind an `application`-layer port wired only by `cmd/*`
(the `wireproxy`/`tailscale` pattern). Both independent plans converged
here; reasoning:

- `wireproxy`/`tailscale` implement `application.Runtime` and are kept
  out of `ui`/`tui` because they're runtime adapters for a domain use
  case (`application.Service`'s connection lifecycle) — letting UI code
  reach around `application.Service` to call them directly would bypass
  edit locks, status transitions, and log capture. Service
  install/uninstall has no such relationship: it doesn't touch
  `profile`/`connection` domain types, and its "runtime" is the host OS's
  service manager, not a backend `application.Service` orchestrates.
  Adding `InstallService()`-shaped methods to `application.Service` would
  be scope creep on a boundary whose whole vocabulary today is profiles
  and connections.
- `internal/lock` is the closer precedent (infrastructure leaf, no
  domain dependency) — but `lock` is only ever probed once in `main.go`
  before `Run()` is called, a one-shot startup check. `svcinstall`'s
  trigger is an **interactive, UI-presented action** (tray toggle
  showing live status, TUI modal with confirm) that belongs inside each
  frontend's existing interactive-action machinery (tray menu callbacks,
  the TUI's `mode` state machine), not in `main.go` before `Run()`.
- A narrow-interface-behind-`cmd/*` alternative was considered and
  rejected: it would force both `main.go`s to construct an
  `svcinstall.Installer` and thread it into `ui.Run`/`tui.Run` as a new
  parameter, for zero real decoupling benefit — `svcinstall.Installer`
  already *is* the abstraction boundary (small, stable, platform-neutral
  interface); a second wrapping interface centered on `cmd/*` adds
  indirection without the payoff that justifies it for `wireproxy`/
  `tailscale` (keeping their heavyweight backend dependency graphs fully
  out of `ui`/`tui`'s compiled closure — not applicable here, since
  `svcinstall`'s own dependency graph is trivial).

## 3. API Design

```go
package svcinstall

import "context"

var (
	ErrUnsupported         = errors.New("svcinstall: automated service install is not supported on this OS yet")
	ErrDaemonBinaryNotFound = errors.New("svcinstall: could not locate the wireproxy-daemon binary; build or install it first (see docs/daemon.md)")
	ErrLockHeld            = errors.New("svcinstall: a wireproxy-daemon (or another process) already holds the profile store lock")
)

// Status is the machine-checkable outcome of Status(); frontends render
// it, they do not parse shell output themselves.
type Status struct {
	Installed bool   // unit/plist file exists on disk
	Enabled   bool   // registered to start at login (systemctl is-enabled / launchctl equivalent)
	Running   bool   // service manager reports it currently running
	UnitPath  string // absolute path to the installed unit/plist; "" if not installed
	Detail    string // human-readable one-line extra context, e.g. "not supported on this OS"
}

// Options customizes Install. The zero value is the common case.
type Options struct {
	StorePath        string // overrides --store; defaults to profilejson.DefaultStorePath()
	DaemonBinaryPath string // overrides binary-path auto-resolution (§6); mainly for tests
	Force            bool   // proceed with Install even if ErrLockHeld would otherwise be returned
}

// Installer is the platform-agnostic surface both internal/ui and
// internal/tui call. Stateless; safe for concurrent use.
type Installer interface {
	// Supported reports whether this OS/build has a working backend, so
	// GUI/TUI can hide/disable the action instead of offering something
	// that always fails.
	Supported() bool

	// Status inspects current state without mutating anything. Safe to
	// call on every tray-menu-open / TUI-modal-open; implementations
	// must stay fast (one bounded, context-respecting subprocess call
	// plus a file stat, not a slow scan).
	Status(ctx context.Context) (Status, error)

	// Install writes the unit/plist (rendered from the embedded
	// template with the resolved binary + store path), then
	// enables+starts it. Idempotent: re-running after a successful
	// install re-writes the file (picking up e.g. a new binary path
	// after an app move) and re-enables/restarts. Returns ErrLockHeld
	// (without installing) if the profile-store lock is currently held
	// and Options.Force is false — see §8.
	Install(ctx context.Context, opts Options) error

	// Uninstall stops (if running), disables (if enabled), and removes
	// the unit/plist file. A no-op success (not an error) when nothing
	// is installed.
	Uninstall(ctx context.Context) error
}

// New returns the Installer for the current GOOS. On an unsupported OS
// it returns a stub whose Supported() is false and whose Install/
// Uninstall return ErrUnsupported — callers need no build tags of their
// own.
func New() Installer
```

Design notes:
- `context.Context` on `Install`/`Uninstall`/`Status` because both
  backends shell out; callers apply a short timeout (a few seconds) so a
  hung subprocess can't freeze the GUI event loop or the TUI's Bubble Tea
  update loop.
- Sentinel errors (`errors.Is`-checkable), not string-matched — `Status.
  Detail` and the error's own `.Error()` text carry the human-readable
  explanation for direct display.
- `Options.Force` resolves the "warn vs. block" question for the lock
  interaction (§8) explicitly rather than leaving frontends to silently
  double-call `Install`.

## 4. Embedded Templates (§2.2 in source plans)

Go's `//go:embed` cannot reach `../` paths, so `packaging/systemd/
wireproxy-daemon.service` and `packaging/launchd/com.github.nrngnl.
wireproxy-daemon.plist` cannot be embedded directly from their current
location. **Duplicate, don't move**: keep the `packaging/` copies as the
human-facing reference for `docs/daemon.md`'s still-supported manual path
(a sysadmin without Go tooling can read them directly), and add templated
copies under `internal/svcinstall/templates/` (`.tmpl` suffix, using
`text/template` fields for `{{.DaemonBinaryPath}}`, `{{.StorePath}}`,
`{{.LogDir}}`/`{{.HomeDir}}`) that the Go binary actually embeds and
renders. Enforce parity with `internal/svcinstall/sync_test.go`, which
reads both the `packaging/` file and the template's non-templated
structural lines from disk (following `architecture_test.go`'s
`repositoryRoot()` helper pattern) and fails if they diverge on anything
other than the templated fields — this is the real enforcement mechanism,
not just a "keep in sync" comment (though add that comment too, in both
files).

## 5. GUI UX (`internal/ui`)

- New tray submenu item in `trayMenu()` (`internal/ui/app.go`, alongside
  the existing `Connect All`/`Disconnect All` items), toggle-labeled from
  live `Installer.Status()`: **"Enable Start on Login"** when not
  installed, **"Disable Start on Login"** when installed.
- Also expose the same action from the main window (not tray-only), since
  Fyne apps without tray support (`g.tray == nil`) still need access —
  add under the existing settings/import-export area, gated the same way
  those actions already are.
- On unsupported OS (`!Installer.Supported()`), render via the existing
  `disabledMenuItem(label)` helper with "(not yet supported on this OS)"
  appended, rather than a clickable item that always errors.
- Enable: no confirmation dialog needed (low-risk, reversible, matches
  `Connect All`'s no-confirmation precedent). Dispatch `Install` on a
  goroutine (not the Fyne event-loop goroutine — match whatever existing
  pattern the codebase uses elsewhere for longer async actions, e.g.
  around Tailscale auth flows), with a transient "Enabling…" label state,
  results delivered back via the existing `runOnUI`/`fyne.Do` pattern.
- Disable: **does** get a lightweight `dialog.NewConfirm` (stops a
  background service the user may depend on for unattended operation —
  mirrors the existing "Quit while connected" confirmation precedent).
- On success: `dialog.ShowInformation` + refresh the tray label. On
  error (including `ErrLockHeld`): `dialog.ShowError` with the wrapped
  message intact; for `ErrLockHeld` specifically, offer a "proceed
  anyway" retry that re-calls `Install` with `Options.Force: true`.

## 6. TUI UX (`internal/tui`)

Follow the existing mode-based state machine in `internal/tui/model.go`
(`modeList`, `modeForm`, `modeConfirm`, `modeExitNode`, `modeImportExport`,
`modeHelp`) exactly:

- Add `modeService` to the `mode` enum.
- New keybinding `S` ("service") in `internal/tui/keymap.go`, added to
  `FullHelp()`'s existing grouping.
- New `internal/tui/screens/service.go` (+ `service_test.go`): a small,
  dependency-free panel following `screens.Confirm`'s shape — renders
  current `Status` (Installed/Enabled/Running/UnitPath as a formatted
  block) plus a single Install-or-Uninstall action line, confirmed with
  the existing `y`/`N` convention. Explicitly states the per-user-only
  scope in its copy ("Installs as a per-user login service. For
  system-wide/server deployment, see docs/daemon.md.").
- Because `Status`/`Install`/`Uninstall` shell out and must not block
  Bubble Tea's single-goroutine `Update`, dispatch as a `tea.Cmd`
  returning a `serviceResultMsg` — the same async-message pattern
  `model.go` already uses for connection-status updates arriving via
  `core.Changes()`.
- On unsupported OS, pressing `S` shows the panel pre-filled with
  `ErrUnsupported`'s text and no y/N prompt (nothing to confirm).
- On `ErrLockHeld`, show the same warning text as the GUI (§5) with a
  "press y again to force" affordance mapping to `Options.Force: true`.

## 7. Binary-Path Resolution (`binpath.go`)

Resolution order, first hit wins:

1. **`Options.DaemonBinaryPath` override** (primarily for tests; no UI
   field exposes this in v1).
2. **Sibling of `os.Executable()`**: resolve the calling process's own
   absolute path (`os.Executable()`, then `filepath.EvalSymlinks` to
   defeat symlink-wrapper launchers), take its directory, look for
   `wireproxy-daemon`/`wireproxy-daemon.exe` there. Covers the common
   "all binaries in one install/release directory" layout that
   `docs/daemon.md` already documents (`/usr/local/bin`).
3. **`$PATH`** via `exec.LookPath("wireproxy-daemon")`.
4. **Fail with `ErrDaemonBinaryNotFound`**, naming every path attempted.

`filepath.EvalSymlinks` is applied to the *resolved daemon candidate*
too (not just the self-path) before writing it into the unit/plist, so a
symlink farm (e.g. Homebrew's layout) produces a stable real path rather
than a symlink that could later be repointed under an already-loaded
service.

Caveats, documented as open risks (§10) rather than solved here: a macOS
`.app` bundle layout (`Contents/MacOS/wireproxy-gui`) only finds a sibling
`wireproxy-daemon` if it's bundled in the same `Contents/MacOS/`
directory — a packaging decision outside this plan's scope; otherwise
resolution falls through to `$PATH`.

Implemented as a pure, fully unit-testable function with swappable
`os.Executable`/`exec.LookPath`/stat function values (follow whatever
indirection style `internal/lock` uses for its unix/windows split).

## 8. Interaction with `internal/lock`

- **Before `Install`** (unless `Options.Force`): probe
  `lock.Probe(storePath + ".lock")`. If held, return `ErrLockHeld`
  without writing anything — a second daemon (foreground or a
  to-be-installed service) racing the same lock at next boot is a
  confusing failure to discover only after reboot, so warn before
  installing. GUI/TUI surface this with a "proceed anyway" retry using
  `Options.Force: true` (§5, §6) rather than a hard, un-retriable block —
  matches `internal/lock`'s existing advisory (non-blocking) philosophy.
- **Before `Uninstall`**: no lock check — stopping+disabling is safe
  regardless (if the service itself holds the lock, stopping it releases
  it normally; if an unrelated foreground daemon holds it, uninstalling
  the service doesn't touch that process).
- **Not solved in v1**: distinguishing "lock held by the service
  instance being reinstalled" from "held by an unrelated foreground
  daemon" — `internal/lock`'s current API has no PID introspection.
  `Options.Force` is the escape hatch; a `lock.Holder() (pid int, ok
  bool)` addition is a possible v2 `internal/lock` change, out of scope
  here (documented in §10).

## 9. Testing Strategy

**Unit-testable, no real systemd/launchd, runs in default `go test`:**
- `binpath_test.go`: all four resolution branches via `t.TempDir()` +
  `t.Setenv`, including the symlink case.
- `templates_test.go`: golden-file rendering for fixed inputs (binary
  path, store path, home dir) — catches template-field regressions
  without any OS calls.
- `sync_test.go`: `packaging/` vs `templates/` structural parity.
- `svcinstall_linux_test.go` / `svcinstall_darwin_test.go`: fake-runner
  unit tests asserting the exact subprocess call sequence (e.g.
  `daemon-reload` before `enable --now`, not after) and idempotent
  no-op-when-never-installed behavior, with `os/exec` calls routed
  through a swappable unexported `runner` function value.
- `svcinstall_test.go`: `Options` zero-value behavior, `ErrLockHeld` via
  a real (fast, local) `lock.TryLock` held in-test with the fake platform
  runner asserted never-invoked.
- TUI `modeService` transitions: synchronous, message-driven, testable
  with fake `serviceResultMsg` values exactly like existing
  `screens/*_test.go` tests.
- Architecture-boundary test extension.

**Requires a real systemd user session / launchd session — gated,
documented manual verification, not assumed to run in CI:**
- The actual `systemctl --user {enable,start,stop,disable,is-active,
  is-enabled}` round trip, including empirically confirming exit-code
  behavior for "already stopped/disabled" (do not assume — verify and
  document what was actually observed).
- The actual `launchctl load/unload/list` round trip on macOS.
- Whether the installed service's tunnel actually comes up unattended at
  real boot/login — explicitly out of scope for automated testing
  (inherits the same unverified status `docs/daemon.md` already states
  for the existing manual-install path); this feature automates file
  placement and service registration, not tunnel functionality.
- Gate these as `//go:build linux`/`//go:build darwin` integration tests
  with a clear comment on what environment they need, run manually once
  per platform by the implementer with a transcript in the task report —
  do not assume GitHub Actions runners provide a working systemd
  `--user`/launchd session without checking first.

## 10. Ordered Implementation Tasks

Task 1 is the sole prerequisite. After Task 1, tracks can parallelize:
- **Core track**: 2 → 3 → {4, 5} → 6
- **GUI track**: 7 (depends on 1's API surface + track-core's 6 landing for full behavior, but can start against a stub `Installer` once Task 1's interface is fixed)
- **TUI track**: 8 (same dependency shape as 7)
- Task 9 (docs) depends on 1–8. Task 10 (Windows) is fully independent/deferred.

### Task 1 — Scaffold `internal/svcinstall` + architecture rule
Files: `internal/svcinstall/doc.go`, `svcinstall.go` (interface, types,
sentinel errors, `New()` returning an always-`ErrUnsupported` stub on
every platform so the package compiles immediately).
Also: `internal/architecture/architecture_test.go` — add
`"svcinstall": {"lock", "profilejson", "profile"}`. Do **not** yet add
`svcinstall` to `ui`/`tui`'s allow-lists (Tasks 7/8 do that alongside the
real call sites).
Acceptance: `go build ./...`, `go test ./internal/architecture/...`,
`go vet ./internal/svcinstall/...` all clean.

### Task 2 — Binary-path resolution
Files: `internal/svcinstall/binpath.go`, `binpath_test.go`.
Work: implement §7's algorithm with swappable exec/stat seams.
Acceptance: all four resolution branches covered by tests, including a
constructed symlink case; zero dependency on a real `wireproxy-daemon`
binary existing on the test runner.

### Task 3 — Embedded templates + golden rendering
Files: `internal/svcinstall/templates/*.tmpl` (copied/adapted from
`packaging/systemd/wireproxy-daemon.service` and `packaging/launchd/
com.github.nrngnl.wireproxy-daemon.plist`, converted to `text/template`
fields per §4), `templates.go`, `templates_test.go`,
`testdata/*.golden`, `sync_test.go`. Also add a "kept in sync with
templates/" comment to both original `packaging/` files.
Acceptance: golden tests pass; `sync_test.go` passes as committed and
would fail on an introduced structural divergence; one manual
`systemd-analyze verify` (or equivalent manual syntax check) run against
rendered output, noted in the task report.

### Task 4 — Linux backend
Files: `internal/svcinstall/svcinstall_linux.go`,
`svcinstall_linux_test.go`.
Work: `Status` via file-stat + bounded `systemctl --user is-active`/
`is-enabled`; `Install` writes the rendered unit to `~/.config/systemd/
user/wireproxy-daemon.service`, runs `daemon-reload` then `enable --now`
(exact order matters, §9); `Uninstall` runs `stop` then `disable`
(tolerating already-stopped/disabled — verify real exit-code behavior,
don't assume), removes the file, `daemon-reload` again. All `os/exec`
calls through a swappable `runner` seam.
Acceptance: fake-runner tests assert exact call sequence/order and
never-installed idempotent no-op; a real-`systemctl` manual verification
transcript (install → status → reboot-equivalent restart check →
uninstall → uninstall-again) in the task report.

### Task 5 — macOS backend
Files: `internal/svcinstall/svcinstall_darwin.go`,
`svcinstall_darwin_test.go`.
Work: mirrors Task 4 for launchd — `Status` via `launchctl list`/`print`,
`Install` writes the rendered plist to `~/Library/LaunchAgents/
com.github.nrngnl.wireproxy-daemon.plist` (home dir substituted directly
via template vars, no shell `sed` needed), creates `~/Library/Logs/
wireproxy-daemon/` if absent, `launchctl load`; `Uninstall` runs
`launchctl unload` then removes the plist.
Acceptance: mirrors Task 4's acceptance criteria for macOS, with its own
real-`launchctl` manual verification transcript.

### Task 6 — `lock` integration + platform dispatch glue
Files: `internal/svcinstall/svcinstall.go` (or a new `lock_integration.go`
if cleaner) for the `ErrLockHeld`/`Options.Force` wiring per §8;
`svcinstall_other.go`; finalize `New()`'s build-tag dispatch now that
Linux/macOS backends exist.
Acceptance: `ErrLockHeld` test using a real `lock.TryLock` held in-test,
fake platform runner asserted never-invoked when the lock blocks and
`Force` is false; `GOOS=linux go build ./...` and `GOOS=darwin go build
./...` both succeed via cross-compilation from one dev machine.

### Task 7 — GUI wiring
Files: `internal/ui/app.go` (tray item + main-window fallback per §5),
`internal/architecture/architecture_test.go` (add `svcinstall` to `ui`'s
allow-list).
Acceptance: `go test ./internal/ui/...` and
`go test ./internal/architecture/...` green; manual smoke-test
transcript (steps or screenshots) of enable/disable on a real desktop
session, since Fyne tray interaction isn't meaningfully unit-testable
end-to-end.

### Task 8 — TUI wiring
Files: `internal/tui/keymap.go` (new `Service` binding), `internal/tui/
model.go` (`modeService`, dispatch, `serviceResultMsg` handling),
`internal/tui/screens/service.go` + `service_test.go`,
`internal/architecture/architecture_test.go` (add `svcinstall` to
`tui`'s allow-list).
Acceptance: `go test ./internal/tui/...` and
`go test ./internal/architecture/...` green; `--list`/`--connect`/
`--disconnect` non-interactive flags unaffected (explicit regression
check).

### Task 9 — Documentation
Files: `docs/daemon.md` (new "Installing as a service (automated)"
section — GUI tray item / TUI `S` key, explicit per-user-only scope,
log-cleanup non-goal, lock-interaction warning; reframe the existing
manual instructions as "manual / system-wide install"), `docs/
architecture.md` (one paragraph acknowledging the new leaf package and
the `ui`/`tui` allow-list change).
Acceptance: doc changes reviewed against Tasks 1–8's actual shipped
behavior (done last, describing what was built, not what was planned).

### Task 10 — Windows stretch goal (explicitly non-blocking for v1)
Files: `internal/svcinstall/svcinstall_windows.go` (real implementation
via `golang.org/x/sys/windows/svc/mgr`), plus — if not already
present — a minimal `svc.Handler` wrapper for `cmd/wireproxy-daemon`/
`internal/daemon` (check `packaging/windows/README.md`'s current status
first; implementing the daemon-side handler is in this task's scope if
still missing).
Acceptance: `GOOS=windows go build ./...` succeeds via cross-compilation;
real-Windows manual verification transcript. Allowed to ship after v1 GA;
must not block Tasks 1–9.

## 11. Open Risks / Questions

1. `systemctl`/`launchctl` exit-code consistency for "already
   stopped/disabled" must be empirically verified in Tasks 4/5, not
   assumed from documentation.
2. Lock-holder PID ambiguity (§8) is unsolved in v1; `Options.Force` is
   the escape hatch. A future `internal/lock` PID-introspection addition
   is a separate, independently-reviewed change.
3. macOS `.app` bundle daemon-binary bundling (§7) is a packaging
   decision outside this plan; `.app`-distributed users fall back to
   `$PATH` resolution until decided.
4. Multiple named service instances per store is a known v1 limitation
   (Non-Goal 5) — document, don't silently allow a confusing
   overwrite-of-a-different-store's-service scenario.
5. CI environment availability of a real systemd `--user`/launchd session
   for the integration tests should be verified, not assumed; skip
   explicitly with a clear reason if unavailable rather than flaking.
6. Windows Defender/SmartScreen friction for Task 10's unsigned-binary
   service install is unaddressed — flagged for whoever picks up Task 10.
</content>
