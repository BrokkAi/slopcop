package gitx

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterCheckTreeClassificationAndPatchRoundTrip(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "exact.json", strings.Repeat("a", 500000))
	writeFile(t, root, "over.json", strings.Repeat("b", 500001))
	writeFile(t, root, "small.json", `{"before":true}`)
	writeFile(t, root, "settings.JSON", "small upper-case extension\n")
	writeFile(t, root, "source.go", strings.Repeat("s", 500001))
	writeFile(t, root, "host.JSONL", strings.Repeat("l", 500001))
	writeFile(t, root, "events.NDJSON", strings.Repeat("n", 500001))
	writeFile(t, root, "unchanged.json", strings.Repeat("u", 500001))
	writeFile(t, root, "delete.json", strings.Repeat("d", 500001))
	writeFile(t, root, "shrink.json", strings.Repeat("x", 500001))
	writeFile(t, root, "rename.json", strings.Repeat("r", 500001))
	writeFile(t, root, "json-rename.json", strings.Repeat("j", 500001))
	writeFile(t, root, "binary-exact.bin", "\x00"+strings.Repeat("e", 499999))
	writeFile(t, root, "binary-over.bin", "\x00"+strings.Repeat("o", 500000))
	writeFile(t, root, "staged.txt", "base\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")

	writeFile(t, root, "exact.json", strings.Repeat("z", 500000))
	writeFile(t, root, "over.json", strings.Repeat("c", 500000)) // A shrinking large JSON still exceeds the bound on the base side.
	writeFile(t, root, "small.json", `{"after":true}`)
	writeFile(t, root, "settings.JSON", strings.Repeat("U", 500001))
	writeFile(t, root, "source.go", strings.Repeat("t", 500002))
	writeFile(t, root, "host.JSONL", "small log now\n")
	writeFile(t, root, "events.NDJSON", strings.Repeat("N", 500002))
	if err := os.Remove(filepath.Join(root, "delete.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "shrink.json", "small now\n")
	if err := os.Rename(filepath.Join(root, "rename.json"), filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "json-rename.json"), filepath.Join(root, "json-renamed.JSON")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "binary-exact.bin", "\x00"+strings.Repeat("f", 499999))
	writeFile(t, root, "binary-over.bin", "\x00"+strings.Repeat("p", 500000))
	writeFile(t, root, "added.json", strings.Repeat("a", 500001))
	writeFile(t, root, "staged.txt", "staged version\n")
	git(t, root, "add", "staged.txt")
	writeFile(t, root, "staged.txt", "worktree version\n")

	indexBefore := git(t, root, "ls-files", "-s")
	cachedBefore := git(t, root, "diff", "--cached")
	statusBefore := git(t, root, "status", "--porcelain")
	snapshot, err := CaptureWorkingTree(root, nil)
	if err != nil {
		t.Fatalf("CaptureWorkingTree: %v", err)
	}
	result, err := FilterCheckTree(root, base, snapshot.Tree)
	if err != nil {
		t.Fatalf("FilterCheckTree: %v", err)
	}
	if result.Tree == snapshot.Tree {
		t.Fatal("large changed data files should produce a filtered tree")
	}
	if got := git(t, root, "ls-files", "-s"); got != indexBefore {
		t.Errorf("user index changed:\n%s\nvs\n%s", indexBefore, got)
	}
	if got := git(t, root, "diff", "--cached"); got != cachedBefore {
		t.Errorf("cached diff changed:\n%s\nvs\n%s", cachedBefore, got)
	}
	if got := git(t, root, "status", "--porcelain"); got != statusBefore {
		t.Errorf("worktree status changed:\n%s\nvs\n%s", statusBefore, got)
	}
	if got := readFile(t, root, "staged.txt"); got != "worktree version\n" {
		t.Errorf("worktree file changed to %q", got)
	}
	if got := git(t, root, "show", snapshot.Tree+":over.json"); len(got) != 500000 {
		t.Errorf("captured snapshot was mutated: over.json size %d", len(got))
	}

	wantOmitted := map[string]struct {
		bytes  int64
		reason string
	}{
		"over.json":         {500001, "large-json"},
		"settings.JSON":     {500001, "large-json"},
		"host.JSONL":        {500001, "large-json"},
		"events.NDJSON":     {500002, "large-json"},
		"delete.json":       {500001, "large-json"},
		"shrink.json":       {500001, "large-json"},
		"rename.json":       {500001, "large-json"},
		"renamed.txt":       {500001, "large-json"},
		"json-rename.json":  {500001, "large-json"},
		"json-renamed.JSON": {500001, "large-json"},
		"binary-over.bin":   {500001, "large-binary"},
		"added.json":        {500001, "large-json"},
	}
	if len(result.OmittedFiles) != len(wantOmitted) {
		t.Fatalf("omitted files = %+v; want %d entries", result.OmittedFiles, len(wantOmitted))
	}
	for _, omitted := range result.OmittedFiles {
		want, ok := wantOmitted[omitted.Path]
		if !ok {
			t.Errorf("unexpected omitted path: %+v", omitted)
			continue
		}
		if omitted.Bytes != want.bytes || omitted.Reason != want.reason {
			t.Errorf("omitted %q = %+v; want bytes=%d reason=%q", omitted.Path, omitted, want.bytes, want.reason)
		}
		delete(wantOmitted, omitted.Path)
	}
	for name := range wantOmitted {
		t.Errorf("missing omitted path %q", name)
	}
	if got := git(t, root, "diff", "--name-only", "-z", "--no-renames", base, result.Tree); strings.Contains(got, "unchanged.json") {
		t.Error("unchanged large JSON must not be omitted or changed")
	}

	change, err := TreePatch(root, base, result.Tree)
	if err != nil {
		t.Fatalf("TreePatch: %v", err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	cloneFixture(t, root, clone, "--no-hardlinks")
	git(t, clone, "checkout", "-q", base)
	apply := exec.Command("git", "-C", clone, "apply", "--binary", "-")
	apply.Stdin = bytes.NewReader(change.Patch)
	if output, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply filtered patch: %v\n%s", err, output)
	}
	for name, want := range map[string]string{
		"exact.json":       strings.Repeat("z", 500000),
		"over.json":        strings.Repeat("b", 500001),
		"small.json":       `{"after":true}`,
		"settings.JSON":    "small upper-case extension\n",
		"source.go":        strings.Repeat("t", 500002),
		"host.JSONL":       strings.Repeat("l", 500001),
		"events.NDJSON":    strings.Repeat("n", 500001),
		"unchanged.json":   strings.Repeat("u", 500001),
		"delete.json":      strings.Repeat("d", 500001),
		"shrink.json":      strings.Repeat("x", 500001),
		"rename.json":      strings.Repeat("r", 500001),
		"json-rename.json": strings.Repeat("j", 500001),
		"binary-exact.bin": "\x00" + strings.Repeat("f", 499999),
		"binary-over.bin":  "\x00" + strings.Repeat("o", 500000),
		"staged.txt":       "worktree version\n",
	} {
		if got := readFile(t, clone, name); got != want {
			at := firstDifferentByte(got, want)
			start := at - 4
			if start < 0 {
				start = 0
			}
			end := at + 5
			if end > len(got) {
				end = len(got)
			}
			wantEnd := at + 5
			if wantEnd > len(want) {
				wantEnd = len(want)
			}
			t.Errorf("applied %s differs at byte %d: got %d bytes (%q), want %d bytes (%q)", name, at, len(got), got[start:end], len(want), want[start:wantEnd])
		}
	}
	for _, name := range []string{"renamed.txt", "json-renamed.JSON", "added.json"} {
		if _, err := os.Stat(filepath.Join(clone, name)); !os.IsNotExist(err) {
			t.Errorf("filtered patch unexpectedly created %s (stat err %v)", name, err)
		}
	}
}

func TestFilterCheckTreeIncludesExactGlobAndRenamePaths(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "literal[1].json", strings.Repeat("a", 500001))
	writeFile(t, root, "literal1.json", strings.Repeat("b", 500001))
	writeFile(t, root, "src/a.json", strings.Repeat("c", 500001))
	writeFile(t, root, "src/deep/b.json", strings.Repeat("d", 500001))
	writeFile(t, root, "rename-source.json", strings.Repeat("e", 500001))
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")
	writeFile(t, root, "literal[1].json", strings.Repeat("A", 500001))
	writeFile(t, root, "literal1.json", strings.Repeat("B", 500001))
	writeFile(t, root, "src/a.json", strings.Repeat("C", 500001))
	writeFile(t, root, "src/deep/b.json", strings.Repeat("D", 500001))
	if err := os.Rename(filepath.Join(root, "rename-source.json"), filepath.Join(root, "rename-destination.txt")); err != nil {
		t.Fatal(err)
	}
	target, err := CaptureWorkingTree(root, nil)
	if err != nil {
		t.Fatalf("CaptureWorkingTree: %v", err)
	}

	defaultResult, err := FilterCheckTree(root, base, target.Tree)
	if err != nil {
		t.Fatalf("FilterCheckTree default: %v", err)
	}
	if len(defaultResult.OmittedFiles) != 6 {
		t.Fatalf("default omitted paths = %+v; want all six large JSON rename endpoints and files", defaultResult.OmittedFiles)
	}

	included, err := FilterCheckTree(root, base, target.Tree,
		"literal[1].json",        // exact path wins over glob metacharacter interpretation
		"src/**/*.json",          // Git globstar spans both src levels
		"rename-destination.txt", // either endpoint includes the whole detected rename
		"missing/**/*.json",      // a nonmatching include has no effect
	)
	if err != nil {
		t.Fatalf("FilterCheckTree with includes: %v", err)
	}
	gotOmitted := make(map[string]bool)
	for _, omitted := range included.OmittedFiles {
		gotOmitted[omitted.Path] = true
	}
	for _, name := range []string{"rename-source.json", "rename-destination.txt"} {
		if gotOmitted[name] {
			t.Errorf("included path %q or its rename partner was omitted", name)
		}
	}
	for _, name := range []string{"literal[1].json", "src/a.json", "src/deep/b.json"} {
		if gotOmitted[name] {
			t.Errorf("include pattern failed to exempt %q", name)
		}
	}
	if len(gotOmitted) != 1 || !gotOmitted["literal1.json"] {
		t.Errorf("omitted paths after includes = %v; want only literal1.json", gotOmitted)
	}

	nonmatch, err := FilterCheckTree(root, base, target.Tree, "absent/**/*.ndjson")
	if err != nil {
		t.Fatalf("FilterCheckTree with nonmatching include: %v", err)
	}
	if len(nonmatch.OmittedFiles) != len(defaultResult.OmittedFiles) {
		t.Errorf("nonmatching include changed filtering: %+v vs %+v", nonmatch.OmittedFiles, defaultResult.OmittedFiles)
	}
}

func TestFilterCheckTreeUnusualPaths(t *testing.T) {
	root := initRepo(t)
	jsonPath := "space tab\tnewline\ncolon:glob[abc]*.json"
	binaryPath := "binary\tname\n:[]*.bin"
	writeFile(t, root, jsonPath, "old-json-"+strings.Repeat("j", 500001))
	writeFile(t, root, binaryPath, "\x00old-binary-"+strings.Repeat("b", 500001))
	writeFile(t, root, "kept.txt", "before\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")
	writeFile(t, root, jsonPath, "new-json-"+strings.Repeat("J", 500001))
	writeFile(t, root, binaryPath, "\x00new-binary-"+strings.Repeat("B", 500001))
	writeFile(t, root, "kept.txt", "after\n")
	target, err := CaptureWorkingTree(root, nil)
	if err != nil {
		t.Fatalf("CaptureWorkingTree: %v", err)
	}

	filtered, err := FilterCheckTree(root, base, target.Tree)
	if err != nil {
		t.Fatalf("FilterCheckTree: %v", err)
	}
	got := make(map[string]OmittedFile)
	for _, omitted := range filtered.OmittedFiles {
		got[omitted.Path] = omitted
	}
	if len(got) != 2 || got[jsonPath].Reason != "large-json" || got[jsonPath].Bytes != 500010 ||
		got[binaryPath].Reason != "large-binary" || got[binaryPath].Bytes != 500013 {
		t.Fatalf("omitted unusual paths = %+v", filtered.OmittedFiles)
	}

	// A literal include must work even when the actual path contains Git
	// pathspec metacharacters, a colon, a tab, and a newline.
	included, err := FilterCheckTree(root, base, target.Tree, jsonPath)
	if err != nil {
		t.Fatalf("FilterCheckTree exact unusual include: %v", err)
	}
	if len(included.OmittedFiles) != 1 || included.OmittedFiles[0].Path != binaryPath {
		t.Errorf("exact unusual include omitted %+v; want only %q", included.OmittedFiles, binaryPath)
	}

	change, err := TreePatch(root, base, filtered.Tree)
	if err != nil {
		t.Fatalf("TreePatch: %v", err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	cloneFixture(t, root, clone, "--no-hardlinks")
	git(t, clone, "checkout", "-q", base)
	apply := exec.Command("git", "-C", clone, "apply", "--binary", "-")
	apply.Stdin = bytes.NewReader(change.Patch)
	if output, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply filtered patch: %v\n%s", err, output)
	}
	if got := readFile(t, clone, jsonPath); got != "old-json-"+strings.Repeat("j", 500001) {
		t.Errorf("filtered JSON path content changed, size %d", len(got))
	}
	if got := readFile(t, clone, binaryPath); got != "\x00old-binary-"+strings.Repeat("b", 500001) {
		t.Errorf("filtered binary path content changed, size %d", len(got))
	}
	if got := readFile(t, clone, "kept.txt"); got != "after\n" {
		t.Errorf("kept.txt = %q", got)
	}
}

func TestFilterCheckTreeDirectoryFileTransitionRequiresInclude(t *testing.T) {
	root := initRepo(t)
	writeFile(t, root, "large.json", strings.Repeat("j", 500001))
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "base")
	base := git(t, root, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(root, "large.json")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "large.json/main.go", "package main\n")
	target, err := CaptureWorkingTree(root, nil)
	if err != nil {
		t.Fatalf("CaptureWorkingTree: %v", err)
	}

	if _, err := FilterCheckTree(root, base, target.Tree); err == nil || !strings.Contains(err.Error(), "--include") {
		t.Fatalf("FilterCheckTree error = %v; want an actionable file/directory conflict with --include remedy", err)
	}
	withInclude, err := FilterCheckTree(root, base, target.Tree, "large.json/main.go")
	if err != nil {
		t.Fatalf("FilterCheckTree with transition include: %v", err)
	}
	if withInclude.Tree != target.Tree || len(withInclude.OmittedFiles) != 0 {
		t.Errorf("including one transition side must retain the complete captured transition: %+v", withInclude)
	}
}

func firstDifferentByte(a, b string) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}
