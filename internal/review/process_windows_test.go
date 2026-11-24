//go:build windows

package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// assertStopped is omitted on Windows because the fake's process has no POSIX PID probe.
func assertStopped(t *testing.T, trace string) { t.Helper() }

// TestWindowsAccessLists checks real access lists. The directories are below
// the user cache, whose default list is private.
func TestWindowsAccessLists(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	privateDir := func(t *testing.T) string {
		t.Helper()
		dir, err := os.MkdirTemp(cache, "slopcop-acl-test-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	grantEveryone := func(t *testing.T, dir, rights string) {
		t.Helper()
		if out, err := exec.Command("icacls", dir, "/grant", "*S-1-1-0:("+rights+")").CombinedOutput(); err != nil {
			t.Fatalf("icacls: %v: %s", err, out)
		}
	}
	want := func(t *testing.T, dir string, shared bool) {
		t.Helper()
		if got, err := writableByOthers(dir); err != nil || got != shared {
			t.Fatalf("writableByOthers(%s) = %v, %v; want %v", dir, got, err, shared)
		}
	}
	t.Run("private", func(t *testing.T) { want(t, privateDir(t), false) })
	t.Run("everyone may write", func(t *testing.T) {
		dir := privateDir(t)
		grantEveryone(t, dir, "W")
		want(t, dir, true)
		if shared, err := protectParent(dir); err != nil || !shared {
			t.Fatalf("protectParent = %v, %v", shared, err)
		}
	})
	t.Run("everyone may add folders gets a locked node_modules", func(t *testing.T) {
		dir := privateDir(t)
		grantEveryone(t, dir, "AD")
		want(t, dir, true)
		for range 2 {
			if shared, err := protectParent(dir); err != nil || shared {
				t.Fatalf("protectParent = %v, %v", shared, err)
			}
		}
		want(t, filepath.Join(dir, "node_modules"), false)
	})
	t.Run("an existing node_modules others can write to is refused", func(t *testing.T) {
		dir := privateDir(t)
		grantEveryone(t, dir, "AD")
		modules := filepath.Join(dir, "node_modules")
		if err := os.Mkdir(modules, 0700); err != nil {
			t.Fatal(err)
		}
		grantEveryone(t, modules, "W")
		if _, err := protectParent(dir); err == nil || !strings.Contains(err.Error(), "another account") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a junction at node_modules is refused", func(t *testing.T) {
		dir := privateDir(t)
		grantEveryone(t, dir, "AD")
		target := privateDir(t)
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(dir, "node_modules"), target).CombinedOutput(); err != nil {
			t.Fatalf("mklink: %v: %s", err, out)
		}
		if _, err := protectParent(dir); err == nil || !strings.Contains(err.Error(), "another account") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an entry inside node_modules others can write to is refused", func(t *testing.T) {
		dir := privateDir(t)
		grantEveryone(t, dir, "AD")
		if shared, err := protectParent(dir); err != nil || shared {
			t.Fatalf("protectParent = %v, %v", shared, err)
		}
		entry := filepath.Join(dir, "node_modules", "planted")
		if err := os.Mkdir(entry, 0700); err != nil {
			t.Fatal(err)
		}
		if shared, err := protectParent(dir); err != nil || shared {
			t.Fatalf("with a private entry: %v, %v", shared, err)
		}
		grantEveryone(t, entry, "W")
		if _, err := protectParent(dir); err == nil || !strings.Contains(err.Error(), "another account") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a Node command's parents get a locked node_modules", func(t *testing.T) {
		parent := privateDir(t)
		grantEveryone(t, parent, "AD")
		install := filepath.Join(parent, "nodejs")
		if err := os.Mkdir(install, 0700); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(install, "agy.exe")
		npx := filepath.Join(install, "npx.cmd")
		for _, p := range []string{other, npx} {
			if err := os.WriteFile(p, nil, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := protectCommandParents(other); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(parent, "node_modules")); !os.IsNotExist(err) {
			t.Fatalf("locked a folder for a command that is not Node: %v", err)
		}
		if err := protectCommandParents(npx); err != nil {
			t.Fatal(err)
		}
		want(t, filepath.Join(parent, "node_modules"), false)
		if _, err := os.Stat(filepath.Join(install, "node_modules")); !os.IsNotExist(err) {
			t.Fatalf("touched the install directory: %v", err)
		}
	})
	t.Run("a Node command's parent that lets others delete entries is refused", func(t *testing.T) {
		parent := privateDir(t)
		grantEveryone(t, parent, "DC")
		install := filepath.Join(parent, "nodejs")
		if err := os.Mkdir(install, 0700); err != nil {
			t.Fatal(err)
		}
		npx := filepath.Join(install, "npx.cmd")
		if err := os.WriteFile(npx, nil, 0700); err != nil {
			t.Fatal(err)
		}
		if err := protectCommandParents(npx); err == nil || !strings.Contains(err.Error(), "replace what is in it") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the parent passes write access to new entries", func(t *testing.T) {
		home := privateDir(t)
		useCacheDir(t, home)
		current, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Join(current, "slopcop", "adapters")
		if err = os.MkdirAll(base, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("icacls", base, "/grant", "*S-1-1-0:(OI)(CI)(IO)(M)").CombinedOutput(); err != nil {
			t.Fatalf("icacls: %v: %s", err, out)
		}
		if _, err = adapterWorkDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "inherits write access") {
			t.Fatalf("err = %v", err)
		}
	})
}
