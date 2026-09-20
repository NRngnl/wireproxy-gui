//go:build windows

package lock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile holds the OS resources for one LockFileEx-based advisory lock.
type lockFile struct {
	f *os.File
}

// tryLockFile opens (creating if necessary) the file at path and attempts
// a non-blocking exclusive LockFileEx lock on it. held reports whether
// some other descriptor already holds the lock.
func tryLockFile(path string) (lf *lockFile, held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}

	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		closeErr := f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, true, nil
		}
		if closeErr != nil {
			return nil, false, closeErr
		}
		return nil, false, err
	}

	return &lockFile{f: f}, false, nil
}

func (lf *lockFile) unlockAndClose() error {
	var overlapped windows.Overlapped
	unlockErr := windows.UnlockFileEx(windows.Handle(lf.f.Fd()), 0, 1, 0, &overlapped)
	closeErr := lf.f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
