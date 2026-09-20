package lock

// Lock represents an advisory, non-blocking exclusive lock held on a file
// on disk. It is acquired with TryLock and released with Release.
type Lock struct {
	file *lockFile
}

// TryLock attempts to open (creating if necessary) the file at path and
// acquire a non-blocking exclusive advisory lock on it.
//
// On success it returns a *Lock the caller owns and must Release, with ok
// true. If another process (or another independent open by this process)
// already holds the lock, it returns (nil, false, nil) — this is the
// expected contention case, not an error. Any other I/O failure (for
// example permission denied or a missing parent directory) is returned as
// a non-nil error with ok false.
func TryLock(path string) (*Lock, bool, error) {
	lf, held, err := tryLockFile(path)
	if err != nil {
		return nil, false, err
	}
	if held {
		return nil, false, nil
	}
	return &Lock{file: lf}, true, nil
}

// Probe non-destructively reports whether the lock at path is currently
// held by any process, without taking ownership of it. It works by
// attempting a TryLock on a fresh file descriptor; if that succeeds, the
// lock was free and is immediately released again before returning. It
// does not disturb a separate *Lock this process may already hold.
//
// Probe returns (true, nil) if the lock is currently held, (false, nil) if
// it is free, and (false, err) on a genuine I/O error.
func Probe(path string) (bool, error) {
	lf, held, err := tryLockFile(path)
	if err != nil {
		return false, err
	}
	if held {
		return true, nil
	}
	if err := lf.unlockAndClose(); err != nil {
		return false, err
	}
	return false, nil
}

// Release unlocks and closes the underlying file handle. Calling Release
// more than once on the same *Lock is not guaranteed to be safe and
// should be avoided by callers.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.unlockAndClose()
}
