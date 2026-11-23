//go:build !windows

package review

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes a nonblocking exclusive lock so another check cannot overwrite state.
func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }

// lockBusy says lockFile failed because another process holds the lock, so
// trying again can succeed.
func lockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR)
}

// unlockFile releases the repository state lock before closing its handle.
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
