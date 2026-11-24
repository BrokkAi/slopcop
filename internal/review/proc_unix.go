//go:build !windows

package review

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// configureProcess prepares the adapter for whole-tree cancellation.
func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killProcessTree stops the adapter and descendants on timeout or cancellation.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// writableByOthers reports whether another user can create files in dir.
func writableByOthers(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true, nil
	}
	// A Linux ACL's mask shows in the group bits, which sharedDirectory reads.
	// A macOS ACL does not show in the mode at all.
	if runtime.GOOS == "darwin" {
		listing, err := macACLListing(dir)
		if err != nil {
			return false, err
		}
		if macACLGrantsWrite(listing) {
			return true, nil
		}
	}
	return sharedDirectory(info.Mode().Perm(), stat.Uid, os.Geteuid()), nil
}

// macACLListing returns a path's mode line and ACL entries.
func macACLListing(path string) (string, error) {
	list := exec.Command("/bin/ls", "-lde", path)
	list.Env = []string{"LC_ALL=C"}
	out, err := list.Output()
	if err != nil {
		return "", fmt.Errorf("read the access list of %s: %w", path, err)
	}
	return string(out), nil
}

// passesWriteToNewEntries reports whether what is created in dir could get
// write access for another user: a macOS inheritable ACL, or any Linux default
// ACL. Such an entry would be open from the moment it exists.
func passesWriteToNewEntries(dir string) (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		listing, err := macACLListing(dir)
		if err != nil {
			return false, err
		}
		return macACLPassesWrite(listing), nil
	case "linux":
		_, err := unix.Getxattr(dir, "system.posix_acl_default", nil)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
			return false, nil
		}
		return false, fmt.Errorf("read the default access list of %s: %w", dir, err)
	}
	return false, nil
}

// protectParent reports whether another user can plant files in dir. Only
// Windows has a default directory that needs more than this check.
func protectParent(dir string) (bool, error) { return writableByOthers(dir) }

// protectCommandParents has nothing to do outside Windows, where no default
// directory above a Node install lets other users add folders.
func protectCommandParents(string) error { return nil }

// sharedDirectory reports whether a directory is group- or world-writable, or
// owned by someone other than this user or root. Group write counts even for
// a group named after the user, since its membership cannot be confirmed.
func sharedDirectory(perm os.FileMode, owner uint32, me int) bool {
	return perm&0o022 != 0 || (owner != 0 && int64(owner) != int64(me))
}
