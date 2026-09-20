package svcinstall

import "github.com/NRngnl/wireproxy-gui/internal/lock"

// checkLockBeforeInstall implements the lock-interaction policy from
// docs/svcinstall-action-plan.md §8: unless force is set, probe the
// profile store's advisory lock before Install proceeds. If the lock is
// currently held, ErrLockHeld is returned so Install can stop before
// writing anything. A lock-probe error (e.g. a missing parent directory
// on a fresh install, matching cmd/wireproxy-gui/main.go and
// cmd/wireproxy-tui/main.go's existing "errors are treated as not-held"
// handling of lock.Probe) is not treated as held and must not block
// Install.
func checkLockBeforeInstall(storePath string, force bool) error {
	if force {
		return nil
	}
	held, err := lock.Probe(storePath + ".lock")
	if err != nil {
		return nil
	}
	if held {
		return ErrLockHeld
	}
	return nil
}
