# Windows service packaging (design note only, v1)

`wireproxy-daemon` does not ship a Windows service wrapper in v1. This note
records the intended design for when that work is picked up.

## Design

- `golang.org/x/sys/windows/svc` is already an indirect dependency (pulled
  in transitively via wireguard-windows-related packages), so no new
  third-party dependency is required to add Windows Service Control
  Manager (SCM) support.
- The SCM integration would wrap the same `daemon.Run(ctx, core, loadErr,
  logger, opts)` loop used by the Unix binary: the `svc.Handler`
  implementation's `Execute` method would call `daemon.Run` with a
  `context.Context` that is cancelled when the SCM delivers a stop or
  shutdown control request, instead of relying on Unix signal delivery via
  `signal.NotifyContext`.
- `SIGHUP`-triggered profile-store reload has no direct SCM equivalent;
  the reload path would need to be re-triggered by a different mechanism
  on Windows (for example a custom SCM control code, or simply left as a
  Unix-only feature until a concrete need is identified).
- Logging would continue to use `log/slog` to stdout/a log file, matching
  the Unix daemon; Windows Event Log integration is a separate, later
  enhancement, not required to get a minimally working service.

## Status

Not implemented in v1. `wireproxy-daemon` currently builds and runs as a
plain console-mode binary on Windows (no service wrapper), which is
sufficient for manual/foreground use but not for unattended
install-as-a-service deployment. Tracked as a follow-up once Windows
daemon demand is confirmed (see Open Risk #3 in
`docs/tui-daemon-action-plan.md`).
