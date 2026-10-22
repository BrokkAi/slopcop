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

func cloneFixture(t *testing.T, from, to string, flags ...string) {
	t.Helper()
	args := append([]string{"clone", "-q"}, flags...)
	for _, setting := range fixtureConfig() {
		args = append(args, "-c", setting)
	}
	if out, err := exec.Command("git", append(args, from, to)...).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
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

func TestWorkingTreePackRoundTripsTheWorkingTree(t *testing.T) {
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
	// Stage one content, then change the file again: the snapshot must ship the
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
	change, err := WorkingTreePack(root, base, base, []string{".slopcop/policies/local.rqlp"})
	if err != nil {
		t.Fatalf("WorkingTreePack: %v", err)
	}
	if change.Files == 0 {
		t.Fatal("expected changed files")
	}
	patch := git(t, root, "-c", "core.quotePath=false", "diff", "--binary", "--no-ext-diff", "-M", base, change.Commit)
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
		t.Errorf("working tree changed while building the snapshot:\n%s\nvs\n%s", statusBefore, after)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	cloneFixture(t, root, clone, "--no-hardlinks")
	git(t, clone, "checkout", "-q", base)
	apply := exec.Command("git", "-C", clone, "index-pack", "--stdin")
	apply.Stdin = bytes.NewReader(change.Pack)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("index pack: %v\n%s", err, out)
	}
	git(t, clone, "checkout", "-q", change.Commit)
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

// compareWorkTreeToClone checks the snapshot clone against the source worktree
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

// Git for Windows defaults to core.filemode=false and core.symlinks=false, so
// the filesystem cannot say a file is executable or a link: the committed
// mode has to survive unless the user changed it.
func TestWorkingTreePackKeepsModesTheFilesystemCannotExpress(t *testing.T) {
	root := initRepo(t)
	git(t, root, "config", "core.filemode", "false")
	git(t, root, "config", "core.symlinks", "false")
	writeFile(t, root, "run.sh", "#!/bin/sh\n")
	writeFile(t, root, "link", "target")
	git(t, root, "add", "-A")
	git(t, root, "update-index", "--chmod=+x", "run.sh")
	link := git(t, root, "hash-object", "-w", "link")
	git(t, root, "update-index", "--cacheinfo", "120000,"+link+",link")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")

	change, err := WorkingTreePack(root, base, base, nil)
	if err != nil {
		t.Fatalf("WorkingTreePack: %v", err)
	}
	if change.Files != 0 {
		t.Fatalf("unchanged tree reports %d files", change.Files)
	}

	writeFile(t, root, "run.sh", "#!/bin/sh\necho changed\n")
	change, err = WorkingTreePack(root, base, base, nil)
	if err != nil {
		t.Fatalf("WorkingTreePack: %v", err)
	}
	patch := git(t, root, "-c", "core.quotePath=false", "diff", "--binary", "--no-ext-diff", "-M", base, change.Commit)
	if change.Files != 1 || !strings.Contains(patch, "index ") || strings.Contains(patch, "mode") {
		t.Fatalf("an edit to an executable must carry content only:\n%s", patch)
	}
}

func TestWorkingTreePackReportsNoFilesForCleanTree(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "src/a.txt", "one\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")

	change, err := WorkingTreePack(root, base, base, nil)
	if err != nil {
		t.Fatalf("WorkingTreePack: %v", err)
	}
	if change.Files != 0 || len(change.Pack) == 0 {
		t.Fatalf("clean tree produced %d files and %d bytes", change.Files, len(change.Pack))
	}
}

func TestUpstreamBaseAndPackIncludeUnpushedWork(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	work := filepath.Join(t.TempDir(), "work")
	cloneFixture(t, origin, work)
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
	change, err := WorkingTreePack(work, base, base, nil)
	if err != nil {
		t.Fatalf("WorkingTreePack: %v", err)
	}
	patch := git(t, work, "diff", "--binary", "--no-ext-diff", "-M", base, change.Commit)
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

func TestSnapshotDeterminismAndPackAgainstShallowBase(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "old", "ancestor\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "ancestor")
	writeFile(t, root, "base", "base\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")
	// The receiving clone has precisely the base at depth 1.
	clone := filepath.Join(t.TempDir(), "receiver")
	cloneFixture(t, "file:///"+strings.TrimPrefix(filepath.ToSlash(root), "/"), clone, "--depth=1", "--no-local")
	writeFile(t, root, "since", "last run\n")
	since, err := WorkingTreePack(root, base, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "since")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "committed", "unpushed\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "unpushed")
	writeFile(t, root, "now", "this run\n")
	git(t, root, "config", "commit.gpgSign", "true")
	first, err := WorkingTreePack(root, base, since.Commit, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_AUTHOR_NAME", "different")
	t.Setenv("GIT_AUTHOR_DATE", "2026-10-21T00:00:00 +0000")
	second, err := WorkingTreePack(root, base, since.Commit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Commit != second.Commit {
		t.Fatalf("snapshot is not deterministic: %s vs %s", first.Commit, second.Commit)
	}
	command := exec.Command("git", "-C", clone, "index-pack", "--stdin")
	command.Stdin = bytes.NewReader(first.Pack)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("index pack in shallow clone: %v: %s", err, out)
	}
	git(t, clone, "cat-file", "-e", first.Commit+"^{commit}")
	git(t, clone, "cat-file", "-e", since.Commit+"^{commit}")
	packHash := strings.Fields(string(out))
	if len(packHash) == 0 {
		t.Fatalf("no pack hash: %q", out)
	}
	index := filepath.Join(clone, ".git", "objects", "pack", "pack-"+packHash[len(packHash)-1]+".idx")
	objects := git(t, clone, "verify-pack", "-v", index)
	for _, line := range strings.Split(git(t, root, "rev-list", "--objects", base), "\n") {
		id := strings.Fields(line)[0]
		if strings.Contains(objects, id+" ") {
			t.Errorf("pack redundantly contains BASE object %s", id)
		}
	}
}

func TestResolveSince(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "base", "base")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")
	ref := "refs/slopcop/last-run/main"
	check := func(rev, want, wantRef string) {
		t.Helper()
		got, gotRef, err := ResolveSince(root, base, rev)
		if err != nil || got != want || gotRef != wantRef {
			t.Fatalf("ResolveSince(%q) = %s, %s, %v; want %s, %s", rev, got, gotRef, err, want, wantRef)
		}
	}
	check("", base, ref)
	writeFile(t, root, "now", "new")
	snapshot, err := WorkingTreePack(root, base, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordLastRun(root, ref, snapshot.Commit); err != nil {
		t.Fatal(err)
	}
	check("", snapshot.Commit, ref)
	check("merge-base", base, ref)
	check("HEAD", base, ref)
	if _, _, err := ResolveSince(root, base, "missing-commit"); err == nil {
		t.Fatal("invalid revision accepted")
	}
	// A dangling marker must fall back to BASE.
	if err := os.WriteFile(filepath.Join(root, ".git", filepath.FromSlash(ref)), []byte(strings.Repeat("a", 40)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("", base, ref)
	git(t, root, "checkout", "--detach", "-q", "HEAD")
	check("", base, "")
	check("merge-base", base, "")
	if err := RecordLastRun(root, "", snapshot.Commit); err != nil {
		t.Fatal(err)
	}
}

// Only the two snapshots should cross the wire, regardless of branch history.
func TestPackExcludesIntermediateBranchHistory(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "large.txt", strings.Repeat("base\n", 20000))
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")
	clone := filepath.Join(t.TempDir(), "receiver")
	cloneFixture(t, "file:///"+strings.TrimPrefix(filepath.ToSlash(root), "/"), clone, "--depth=1", "--no-local")
	var commits, blobs []string
	for _, content := range []string{"first", "second", "third"} {
		writeFile(t, root, "large.txt", strings.Repeat(content+"\n", 20000))
		git(t, root, "add", "-A")
		git(t, root, "commit", "-qm", content)
		commits = append(commits, git(t, root, "rev-parse", "HEAD"))
		blobs = append(blobs, git(t, root, "rev-parse", "HEAD:large.txt"))
	}
	writeFile(t, root, "large.txt", strings.Repeat("dirty\n", 20000))
	since, _, err := ResolveSince(root, base, commits[1])
	if err != nil {
		t.Fatal(err)
	}
	change, err := WorkingTreePack(root, base, since, nil)
	if err != nil {
		t.Fatal(err)
	}
	if since == commits[1] {
		t.Fatal("SINCE was not wrapped")
	}
	for _, commit := range []string{change.Commit, since} {
		if parent := git(t, root, "rev-parse", commit+"^"); parent != base {
			t.Errorf("%s parent = %s, want BASE %s", commit, parent, base)
		}
	}
	if tree := git(t, root, "rev-parse", since+"^{tree}"); tree != git(t, root, "rev-parse", commits[1]+"^{tree}") {
		t.Fatal("wrapping SINCE changed its tree")
	}
	command := exec.Command("git", "-C", clone, "index-pack", "--stdin")
	command.Stdin = bytes.NewReader(change.Pack)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("index pack: %v: %s", err, out)
	}
	// Missing batch entries are reported per line (git itself still exits 0).
	missing := append(append([]string{}, commits...), blobs[0], blobs[2])
	ids := append(append([]string{}, missing...), change.Commit, since)
	checked, err := runBytesInput(clone, nil, []byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch-check")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(checked)), "\n")
	if len(lines) != len(ids) {
		t.Fatalf("batch-check: %s", checked)
	}
	for i, id := range ids {
		if i < len(missing) {
			if lines[i] != id+" missing" {
				t.Errorf("intermediate object was shipped: %s", lines[i])
			}
		} else if !strings.HasPrefix(lines[i], id+" commit ") {
			t.Errorf("snapshot missing: %s", lines[i])
		}
	}
	packHash := strings.Fields(string(out))
	index := filepath.Join(clone, ".git", "objects", "pack", "pack-"+packHash[len(packHash)-1]+".idx")
	count := 0
	for _, line := range strings.Split(git(t, clone, "verify-pack", "-v", index), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[1] == "commit" {
			count++
			if fields[0] != change.Commit && fields[0] != since {
				t.Errorf("unexpected packed commit %s", fields[0])
			}
		}
	}
	if count != 2 {
		t.Fatalf("pack has %d commits, want exactly NOW and SINCE", count)
	}
}

func TestResolveSinceReanchorsLastRunWhenBaseMoves(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "file", "base")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	oldBase := git(t, root, "rev-parse", "HEAD")
	writeFile(t, root, "file", "last run")
	last, err := WorkingTreePack(root, oldBase, oldBase, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/slopcop/last-run/main"
	if err := RecordLastRun(root, ref, last.Commit); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "file", "new base")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "new base")
	base := git(t, root, "rev-parse", "HEAD")
	since, gotRef, err := ResolveSince(root, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotRef != ref || since == last.Commit {
		t.Fatalf("last run not rewrapped: %s, %s", since, gotRef)
	}
	if got := git(t, root, "rev-parse", since+"^"); got != base {
		t.Fatalf("SINCE parent = %s", got)
	}
	if got := git(t, root, "rev-parse", since+"^{tree}"); got != git(t, root, "rev-parse", last.Commit+"^{tree}") {
		t.Fatal("SINCE tree changed")
	}
	explicit, _, err := ResolveSince(root, base, last.Commit)
	if err != nil || explicit != since {
		t.Fatalf("explicit wrapping differs: %s, %v", explicit, err)
	}
	repeated, _, err := ResolveSince(root, base, since)
	if err != nil || repeated != since {
		t.Fatalf("wrapping not deterministic: %s, %v", repeated, err)
	}
	git(t, root, "commit", "--allow-empty", "-qm", "same tree")
	equalTree := git(t, root, "rev-parse", "HEAD")
	got, _, err := ResolveSince(root, base, equalTree)
	if err != nil || got != base {
		t.Fatalf("BASE-equivalent tree = %s, %v", got, err)
	}
}
