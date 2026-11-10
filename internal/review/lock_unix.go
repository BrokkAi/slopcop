//go:build !windows

package review

import (
	"os"
	"syscall"
)

// lockFile takes a nonblocking exclusive lock so another check cannot overwrite state.
func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }

// unlockFile releases the repository state lock before closing its handle.
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
