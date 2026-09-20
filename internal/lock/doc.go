// Package lock provides an advisory, cross-process file lock used to warn
// or hard-fail when multiple wireproxy-gui frontends (GUI, TUI, daemon)
// point at the same profile store concurrently. It has no dependency on
// any other internal package.
package lock
