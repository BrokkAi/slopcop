//go:build windows

package review

import (
	"os/exec"
	"strconv"
)

// configureProcess prepares the adapter for whole-tree cancellation.
func configureProcess(cmd *exec.Cmd) {}

// killProcessTree stops the adapter and descendants on timeout or cancellation.
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		_ = cmd.Process.Kill()
	}
}
