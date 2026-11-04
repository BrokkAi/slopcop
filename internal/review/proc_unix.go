//go:build !windows

package review

import (
	"os/exec"
	"syscall"
)

// configureProcess prepares the adapter for whole-tree cancellation.
func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killProcessTree stops the adapter and descendants on timeout or cancellation.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
