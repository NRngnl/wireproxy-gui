package svcinstall

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/NRngnl/wireproxy-gui/internal/lock"
)

func TestCheckLockBeforeInstall_HeldNoForce(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")

	l, ok, err := lock.TryLock(storePath + ".lock")
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !ok {
		t.Fatal("expected to acquire lock in test setup")
	}
	defer l.Release()

	if err := checkLockBeforeInstall(storePath, false); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("checkLockBeforeInstall() = %v, want ErrLockHeld", err)
	}
}

func TestCheckLockBeforeInstall_HeldWithForce(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")

	l, ok, err := lock.TryLock(storePath + ".lock")
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !ok {
		t.Fatal("expected to acquire lock in test setup")
	}
	defer l.Release()

	if err := checkLockBeforeInstall(storePath, true); err != nil {
		t.Fatalf("checkLockBeforeInstall() with force = %v, want nil", err)
	}
}

func TestCheckLockBeforeInstall_Free(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store.json")

	if err := checkLockBeforeInstall(storePath, false); err != nil {
		t.Fatalf("checkLockBeforeInstall() = %v, want nil for a free lock", err)
	}
}

func TestCheckLockBeforeInstall_MissingDirectoryNotBlocking(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "does", "not", "exist", "store.json")

	if err := checkLockBeforeInstall(storePath, false); err != nil {
		t.Fatalf("checkLockBeforeInstall() = %v, want nil when the lock probe errors", err)
	}
}
