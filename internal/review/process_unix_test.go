//go:build !windows

package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestSharedDirectory checks which owners and modes let another user plant
// files in a directory above an adapter.
func TestSharedDirectory(t *testing.T) {
	const me, other = 501, 502
	for _, c := range []struct {
		perm  os.FileMode
		owner uint32
		want  bool
	}{
		{0o700, me, false},
		{0o755, me, false},
		{0o755, 0, false},
		{0o770, me, true},
		{0o775, 0, true},
		{0o777, me, true},
		{0o755, other, true},
		{0o700, other, true},
	} {
		if got := sharedDirectory(c.perm, c.owner, me); got != c.want {
			t.Errorf("mode %o owner %d: got %v, want %v", c.perm, c.owner, got, c.want)
		}
	}
}

// TestWritableByOthersReadsTheDirectory checks the mode and owner on disk.
func TestWritableByOthersReadsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	for mode, want := range map[os.FileMode]bool{0o700: false, 0o770: true, 0o777: true} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if got, err := writableByOthers(dir); err != nil || got != want {
			t.Errorf("mode %o: got %v, %v; want %v", mode, got, err, want)
		}
	}
}

// TestWritableByOthersReadsAMacACL checks a real ACL grant on a directory
// whose mode looks private.
func TestWritableByOthersReadsAMacACL(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS extended ACLs")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := writableByOthers(dir); err != nil || got {
		t.Fatalf("before the grant: %v, %v", got, err)
	}
	if out, err := exec.Command("/bin/chmod", "+a", "user:nobody allow add_file,add_subdirectory", dir).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v: %s", err, out)
	}
	if got, err := writableByOthers(dir); err != nil || !got {
		t.Fatalf("after the grant: %v, %v", got, err)
	}
}

// TestAdapterWorkDirRefusesAnInheritedMacACL checks a grant the parent holds
// only for what is created in it, which the parent's own check does not see.
func TestAdapterWorkDirRefusesAnInheritedMacACL(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS extended ACLs")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(cache, "slopcop-acl-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	useCacheDir(t, home)
	current, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(current, "slopcop", "adapters")
	if err = os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/chmod", "+a", "user:nobody allow add_file,add_subdirectory,file_inherit,directory_inherit,only_inherit", base).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v: %s", err, out)
	}
	if _, err = adapterWorkDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "inherits write access") {
		t.Fatalf("err = %v", err)
	}
	if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
		t.Fatalf("left behind: %v, %v", entries, err)
	}
}

// TestCreateEmptyManifestDoesNotFollowASymlink checks that a link planted at
// the manifest's path cannot redirect the write to another file.
func TestCreateEmptyManifestDoesNotFollowASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "package.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := createEmptyManifest(link); err == nil {
		t.Fatal("wrote through a symbolic link")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("target = %q, %v", data, err)
	}
}

// TestProtectParentsRefusesASharedNodeModules checks a private parent whose
// node_modules/.bin, on npm's PATH, another user can write to or is linked to
// somewhere they can.
func TestProtectParentsRefusesASharedNodeModules(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(cache, "slopcop-acl-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	child := filepath.Join(dir, "child")
	bin := filepath.Join(dir, "node_modules", ".bin")
	for _, p := range []string{child, bin} {
		if err = os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err = protectParents(child); err != nil {
		t.Fatalf("private node_modules: %v", err)
	}
	if err = os.Chmod(bin, 0o777); err != nil {
		t.Fatal(err)
	}
	if err = protectParents(child); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("writable .bin: %v", err)
	}
	if err = os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(dir, "shared")
	if err = os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(shared, bin); err != nil {
		t.Fatal(err)
	}
	if err = protectParents(child); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf(".bin linked to a shared directory: %v", err)
	}
}

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
