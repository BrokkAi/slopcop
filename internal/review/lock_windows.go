//go:build windows

package review

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes a nonblocking exclusive lock so another check cannot overwrite state.
func lockFile(f *os.File) error {
	var o windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &o)
}

// unlockFile releases the repository state lock before closing its handle.
func unlockFile(f *os.File) error {
	var o windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
}
