//go:build !windows

package review

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// assertStopped checks that a fake adapter's PID no longer exists after Run.
func assertStopped(t *testing.T, trace string) {
	t.Helper()
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "pid="); ok {
			pid, err := strconv.Atoi(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Fatalf("adapter PID %d still exists: %v", pid, err)
			}
			return
		}
	}
	t.Fatal("missing adapter PID")
}
