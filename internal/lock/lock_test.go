package lock

import (
	"path/filepath"
	"testing"
)

func TestTryLockSucceedsAndReturnsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")

	l, ok, err := TryLock(path)
	if err != nil {
		t.Fatalf("TryLock() error = %v", err)
	}
	if !ok {
		t.Fatal("TryLock() ok = false, want true for an unheld lock")
	}
	if l == nil {
		t.Fatal("TryLock() returned nil Lock with ok = true")
	}
	t.Cleanup(func() {
		if err := l.Release(); err != nil {
			t.Errorf("Release() error = %v", err)
		}
	})
}

func TestTryLockContentionFromSecondDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")

	first, ok, err := TryLock(path)
	if err != nil {
		t.Fatalf("first TryLock() error = %v", err)
	}
	if !ok || first == nil {
		t.Fatal("first TryLock() did not acquire the lock")
	}
	defer func() {
		if err := first.Release(); err != nil {
			t.Errorf("Release() error = %v", err)
		}
	}()

	// A second, independent attempt against the same path (simulating a
	// second process) must observe contention and not acquire the lock.
	second, ok, err := TryLock(path)
	if err != nil {
		t.Fatalf("second TryLock() error = %v", err)
	}
	if ok {
		t.Fatal("second TryLock() ok = true, want false while first lock is held")
	}
	if second != nil {
		t.Fatalf("second TryLock() returned non-nil Lock: %#v", second)
	}
}

func TestProbeReflectsLockState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")

	held, err := Probe(path)
	if err != nil {
		t.Fatalf("Probe() before lock error = %v", err)
	}
	if held {
		t.Fatal("Probe() = true before any lock was taken")
	}

	l, ok, err := TryLock(path)
	if err != nil || !ok || l == nil {
		t.Fatalf("TryLock() failed to acquire lock: ok=%v err=%v", ok, err)
	}

	held, err = Probe(path)
	if err != nil {
		t.Fatalf("Probe() while held error = %v", err)
	}
	if !held {
		t.Fatal("Probe() = false while lock is held")
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	held, err = Probe(path)
	if err != nil {
		t.Fatalf("Probe() after release error = %v", err)
	}
	if held {
		t.Fatal("Probe() = true after lock was released")
	}
}

func TestTryLockSucceedsAgainAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")

	first, ok, err := TryLock(path)
	if err != nil || !ok || first == nil {
		t.Fatalf("first TryLock() failed: ok=%v err=%v", ok, err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	second, ok, err := TryLock(path)
	if err != nil {
		t.Fatalf("second TryLock() error = %v", err)
	}
	if !ok || second == nil {
		t.Fatal("second TryLock() did not succeed after first lock was released")
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
}

func TestTryLockErrorsOnMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "store.lock")

	l, ok, err := TryLock(path)
	if err == nil {
		t.Fatal("TryLock() expected an error for a missing parent directory")
	}
	if ok || l != nil {
		t.Fatalf("TryLock() returned ok=%v l=%v on error path, want false/nil", ok, l)
	}
}
