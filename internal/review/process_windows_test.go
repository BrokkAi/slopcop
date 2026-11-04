//go:build windows

package review

import "testing"

// assertStopped is omitted on Windows because the fake's process has no POSIX PID probe.
func assertStopped(t *testing.T, trace string) { t.Helper() }
