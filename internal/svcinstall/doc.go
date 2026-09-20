// Package svcinstall installs and uninstalls wireproxy-daemon as a
// per-user login-time OS service (systemd --user on Linux, a launchd
// LaunchAgent on macOS). It never installs a system-wide service and
// never requires elevated privileges: writing a systemd --user unit or
// a ~/Library/LaunchAgents plist is always within the invoking user's
// own permissions.
//
// Platform support matrix (v1):
//   - linux:   implemented (systemd --user)
//   - darwin:  implemented (launchd LaunchAgent)
//   - windows: stub, returns ErrUnsupported (stretch goal, not in v1)
//   - other:   stub, returns ErrUnsupported
//
// internal/svcinstall has no dependency on internal/application,
// internal/ui, internal/tui, internal/daemon, internal/wireproxy, or
// internal/tailscale. Its only internal dependencies are internal/lock
// (to probe, not hold, the profile-store lock before installing) and
// internal/profilejson plus internal/profile (to compute the default
// profile store path used in the generated unit/plist).
package svcinstall
