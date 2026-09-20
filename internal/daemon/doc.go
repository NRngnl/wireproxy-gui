// Package daemon is the headless inbound adapter used by cmd/wireproxy-daemon.
// It depends only on internal/application, internal/buildinfo,
// internal/connection, and internal/profile, mirroring internal/ui's
// boundary rules. It never constructs or imports persistence or runtime
// adapters directly.
package daemon
