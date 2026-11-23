//go:build windows

package review

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes a nonblocking exclusive lock so another check cannot overwrite state.
func lockFile(f *os.File) error {
	var o windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &o)
}

// lockBusy says lockFile failed because another process holds the lock, so
// trying again can succeed.
func lockBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}

// unlockFile releases the repository state lock before closing its handle.
func unlockFile(f *os.File) error {
	var o windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
}
