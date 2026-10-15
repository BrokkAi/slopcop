package gitx

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixtureConfig keeps the fixtures byte-for-byte what the test wrote,
// whatever the machine's global config says: Git for Windows defaults to
// core.autocrlf=true and core.symlinks=false.
func fixtureConfig() []string {
	return []string{
		"core.autocrlf=false",
		"core.symlinks=" + strconv.FormatBool(symlinkError() == nil),
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main", ".")
	for _, setting := range fixtureConfig() {
		key, value, _ := strings.Cut(setting, "=")
		git(t, dir, "config", key, value)
	}
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test")
	return dir
}

var symlinkProbe struct {
	once sync.Once
	err  error
}

// symlinkError reports why this process cannot create symlinks, or nil if it
// can. Windows needs SeCreateSymbolicLinkPrivilege (admin or Developer Mode).
func symlinkError() error {
	symlinkProbe.once.Do(func() {
		dir, err := os.MkdirTemp("", "slopcop-symlink-probe")
		if err != nil {
			symlinkProbe.err = err
			return
		}
		defer os.RemoveAll(dir)
		symlinkProbe.err = os.Symlink("target", filepath.Join(dir, "link"))
	})
	return symlinkProbe.err
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWorkingTreePatchRoundTripsTheWorkingTree(t *testing.T) {
	root := initRepo(t)
	symlinks := symlinkError() == nil
	if !symlinks {
		t.Logf("skipping the symlink cases: %v", symlinkError())
	}
	writeFile(t, root, ".gitignore", ".slopcop/\nbuild/\n")
	writeFile(t, root, "src/a.txt", "one\n")
	writeFile(t, root, "src/del.txt", "delete me\n")
	writeFile(t, root, "src/moved.bin", "binary\x00one")
	writeFile(t, root, "src/mod.bin", "binary\x00keep")
	if symlinks {
		if err := os.Symlink("target-one", filepath.Join(root, "src", "link")); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")

	writeFile(t, root, "src/a.txt", "two\n")
	// Stage one content, then change the file again: the patch has to ship the
	// worktree state, not the index.
	git(t, root, "add", "src/a.txt")
	writeFile(t, root, "src/a.txt", "three\n")
	if err := os.Remove(filepath.Join(root, "src/del.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(
		filepath.Join(root, "src/moved.bin"),
		filepath.Join(root, "src/renamed.bin"),
	); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "src/mod.bin", "binary\x00changed")
	if symlinks {
		if err := os.Remove(filepath.Join(root, "src", "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target-two", filepath.Join(root, "src", "link")); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, root, "src/new.txt", "untracked\n")
	if err := os.Chmod(filepath.Join(root, "src", "new.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "src/é.txt", "unicode\n")
	writeFile(t, root, "build/out.log", "ignored\n")
	writeFile(t, root, ".slopcop/policies/local.rqlp", "(policy :id \"local\")\n")

	statusBefore := git(t, root, "status", "--porcelain")
	change, err := WorkingTreePatch(root, base, []string{".slopcop/policies/local.rqlp"})
	if err != nil {
		t.Fatalf("WorkingTreePatch: %v", err)
	}
	if change.Files == 0 {
		t.Fatal("expected changed files")
	}
	patch := string(change.Patch)
	required := []string{
		"diff --git a/src/a.txt b/src/a.txt",
		"diff --git a/src/del.txt b/src/del.txt",
		"diff --git a/src/new.txt b/src/new.txt",
		"diff --git a/src/é.txt b/src/é.txt",
		"rename from src/moved.bin",
		"rename to src/renamed.bin",
		"diff --git a/src/mod.bin b/src/mod.bin",
		"diff --git a/.slopcop/policies/local.rqlp b/.slopcop/policies/local.rqlp",
	}
	if symlinks {
		required = append(required, "diff --git a/src/link b/src/link")
	}
	for _, required := range required {
		if !strings.Contains(patch, required) {
			t.Errorf("patch is missing %q", required)
		}
	}
	if strings.Contains(patch, "out.log") {
		t.Error("patch must not carry files an ignore rule excludes")
	}
	if after := git(t, root, "status", "--porcelain"); after != statusBefore {
		t.Errorf("working tree changed while building the patch:\n%s\nvs\n%s", statusBefore, after)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	cloneArgs := []string{"clone", "-q", "--no-hardlinks"}
	for _, setting := range fixtureConfig() {
		cloneArgs = append(cloneArgs, "-c", setting)
	}
	if out, err := exec.Command("git", append(cloneArgs, root, clone)...).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	git(t, clone, "checkout", "-q", base)
	apply := exec.Command("git", "-C", clone, "apply", "--binary", "-")
	apply.Stdin = bytes.NewReader(change.Patch)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply patch: %v\n%s", err, out)
	}
	if got := readFile(t, clone, "src/a.txt"); got != "three\n" {
		t.Errorf("src/a.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(clone, "src/del.txt")); !os.IsNotExist(err) {
		t.Error("src/del.txt should be deleted")
	}
	if got := readFile(t, clone, "src/renamed.bin"); got != "binary\x00one" {
		t.Errorf("src/renamed.bin = %q", got)
	}
	if got := readFile(t, clone, "src/mod.bin"); got != "binary\x00changed" {
		t.Errorf("src/mod.bin = %q", got)
	}
	if got := readFile(t, clone, "src/new.txt"); got != "untracked\n" {
		t.Errorf("src/new.txt = %q", got)
	}
	if got := readFile(t, clone, "src/é.txt"); got != "unicode\n" {
		t.Errorf("src/é.txt = %q", got)
	}
	if got := readFile(t, clone, ".slopcop/policies/local.rqlp"); got != "(policy :id \"local\")\n" {
		t.Errorf("policy file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(clone, "build/out.log")); !os.IsNotExist(err) {
		t.Error("ignored build output must not be applied")
	}
	compareWorkTreeToClone(t, root, clone, base, []string{".slopcop/policies/local.rqlp"})
}

// compareWorkTreeToClone checks the applied clone against the source worktree
// for every tracked and untracked non-ignored file, plus deletions and the
// selected policy file. This is the "send the source as it is" contract.
func compareWorkTreeToClone(t *testing.T, root, clone, base string, extra []string) {
	t.Helper()
	paths := append(
		gitNUL(t, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard"),
		extra...,
	)
	present := make(map[string]bool, len(paths))
	for _, name := range paths {
		if name == "" || present[name] {
			continue
		}
		source := filepath.Join(root, filepath.FromSlash(name))
		applied := filepath.Join(clone, filepath.FromSlash(name))
		sourceInfo, err := os.Lstat(source)
		if os.IsNotExist(err) {
			// Deleted from the worktree; checked against the base tree below.
			continue
		}
		if err != nil {
			t.Errorf("source %s: %v", name, err)
			continue
		}
		present[name] = true
		appliedInfo, err := os.Lstat(applied)
		if err != nil {
			t.Errorf("applied clone is missing %s: %v", name, err)
			continue
		}
		if sourceInfo.Mode()&os.ModeSymlink != 0 || appliedInfo.Mode()&os.ModeSymlink != 0 {
			want, err := os.Readlink(source)
			if err != nil {
				t.Errorf("readlink %s: %v", name, err)
				continue
			}
			got, err := os.Readlink(applied)
			if err != nil || got != want {
				t.Errorf("symlink %s = %q (%v), want %q", name, got, err, want)
			}
			continue
		}
		want, err := os.ReadFile(source)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		got, err := os.ReadFile(applied)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("file %s differs from the working tree (%v)", name, err)
		}
		if (sourceInfo.Mode()&0o111 != 0) != (appliedInfo.Mode()&0o111 != 0) {
			t.Errorf("file %s executable bit was not carried", name)
		}
	}
	for _, name := range gitNUL(t, root, "ls-tree", "-r", "-z", "--name-only", base) {
		if name == "" || present[name] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(clone, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Errorf("deleted file %s is still present in the applied clone", name)
		}
	}
}

func gitNUL(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	var values []string
	for _, value := range bytes.Split(out, []byte{0}) {
		if len(value) > 0 {
			values = append(values, string(value))
		}
	}
	return values
}

func TestWorkingTreePatchEmptyForCleanTree(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "src/a.txt", "one\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")

	change, err := WorkingTreePatch(root, base, nil)
	if err != nil {
		t.Fatalf("WorkingTreePatch: %v", err)
	}
	if change.Files != 0 || len(change.Patch) != 0 {
		t.Fatalf("clean tree produced %d files and %d bytes", change.Files, len(change.Patch))
	}
}

func TestUpstreamBaseAndPatchIncludeUnpushedWork(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	work := filepath.Join(t.TempDir(), "work")
	if out, err := exec.Command("git", "clone", "-q", origin, work).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	git(t, work, "config", "user.email", "test@example.com")
	git(t, work, "config", "user.name", "Test")
	writeFile(t, work, "src/base.txt", "base\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "base")
	git(t, work, "push", "-q", "-u", "origin", "main")
	pushed := git(t, work, "rev-parse", "HEAD")

	writeFile(t, work, "src/committed.txt", "unpushed commit\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "unpushed")
	writeFile(t, work, "src/dirty.txt", "uncommitted\n")

	base, ok, err := UpstreamBase(work)
	if err != nil {
		t.Fatalf("UpstreamBase: %v", err)
	}
	if !ok {
		t.Fatal("expected an upstream base")
	}
	if base != pushed {
		t.Fatalf("base = %s, want %s", base, pushed)
	}
	change, err := WorkingTreePatch(work, base, nil)
	if err != nil {
		t.Fatalf("WorkingTreePatch: %v", err)
	}
	patch := string(change.Patch)
	for _, required := range []string{"src/committed.txt", "src/dirty.txt"} {
		if !strings.Contains(patch, required) {
			t.Errorf("patch is missing %q", required)
		}
	}
}

func TestCanonicalRepoURL(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"git@github.com:BrokkAi/slopcop.git", "https://github.com/BrokkAi/slopcop"},
		{"ssh://git@github.com/BrokkAi/slopcop.git", "https://github.com/BrokkAi/slopcop"},
		{"https://gitlab.com/group/sub/repo.git/", "https://gitlab.com/group/sub/repo"},
		{"https://user:secret@bitbucket.org/team/repo.git", "https://bitbucket.org/team/repo"},
		{"git@GitHub.com:Owner/Repo.git", "https://github.com/Owner/Repo"},
	}
	for _, tc := range cases {
		got, err := CanonicalRepoURL(tc.raw)
		if err != nil {
			t.Errorf("CanonicalRepoURL(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("CanonicalRepoURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	for _, raw := range []string{"", "not a url", "/tmp/repo"} {
		if got, err := CanonicalRepoURL(raw); err == nil {
			t.Errorf("CanonicalRepoURL(%q) = %q, want an error", raw, got)
		}
	}
}
