//go:build unix

package lock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile holds the OS resources for one flock(2)-based advisory lock.
type lockFile struct {
	f *os.File
}

// tryLockFile opens (creating if necessary) the file at path and attempts
// a non-blocking exclusive flock on it. held reports whether some other
// descriptor already holds the lock.
func tryLockFile(path string) (lf *lockFile, held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}

	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		closeErr := f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
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
	unlockErr := unix.Flock(int(lf.f.Fd()), unix.LOCK_UN)
	closeErr := lf.f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
