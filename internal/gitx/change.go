package gitx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkingChange is the patch one check ships.
type WorkingChange struct {
	// Patch is the complete git patch from the base commit to the working tree.
	Patch []byte
	// Files counts the patch's diff headers.
	Files int
}

// Snapshot names the captured working tree and the tree used for the service
// patch. PatchTree also contains selected policy files ignored by Git.
type Snapshot struct {
	Tree      string
	PatchTree string
}

// CaptureWorkingTree writes the working tree to tree objects through a
// temporary index, so the user's index and worktree are never modified. Tree
// is what committing the working tree would produce: tracked edits,
// deletions, and untracked files an ignore rule does not exclude; an approval
// names it, so committing the checked state unchanged gives HEAD the same
// tree. PatchTree adds the selected policy files even when ignored, since the
// service must receive them; it equals Tree when none is ignored.
func CaptureWorkingTree(root string, policyFiles []string) (Snapshot, error) {
	var snapshot Snapshot
	dir, err := os.MkdirTemp("", "slopcop-index-")
	if err != nil {
		return snapshot, fmt.Errorf("create temporary index: %w", err)
	}
	defer os.RemoveAll(dir)

	env := withEnv(os.Environ(), "GIT_INDEX_FILE", filepath.Join(dir, "index"))
	// Seed the index from HEAD so git can keep the committed mode where the
	// filesystem cannot express it: with core.filemode=false or
	// core.symlinks=false (Git for Windows' defaults), a path with no index
	// entry reads as 100644, which would ship every executable and symlink as
	// a mode change.
	if _, err := RunBytes(root, env, "read-tree", "HEAD"); err != nil {
		return snapshot, err
	}
	if err := keepSkippedPaths(root, env, policyFiles); err != nil {
		return snapshot, err
	}
	if _, err := RunBytes(root, env, "add", "--all", "--", "."); err != nil {
		return snapshot, err
	}
	if snapshot.Tree, err = writeTree(root, env); err != nil {
		return Snapshot{}, err
	}

	for _, file := range policyFiles {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
			return Snapshot{}, fmt.Errorf("selected policy file not found: %s", file)
		}
	}
	for _, chunk := range chunks(policyFiles, 256) {
		args := append(
			[]string{"--literal-pathspecs", "add", "--force", "--"},
			chunk...,
		)
		if _, err := RunBytes(root, env, args...); err != nil {
			return Snapshot{}, err
		}
	}
	snapshot.PatchTree, err = writeTree(root, env)
	return snapshot, err
}

func writeTree(root string, env []string) (string, error) {
	out, err := RunBytes(root, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// TreePatch builds the complete binary patch between a base commit or tree
// and a captured tree. Files counts the patch's diff headers.
func TreePatch(root, base, tree string) (WorkingChange, error) {
	var change WorkingChange
	patch, err := RunBytes(root, nil,
		"-c", "core.quotePath=false",
		"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--binary",
		"--full-index", "--ignore-submodules=none",
		"--src-prefix=a/", "--dst-prefix=b/", base, tree,
	)
	if err != nil {
		return change, err
	}
	change.Files = countDiffHeaders(patch)
	change.Patch = patch
	return change, nil
}

// WorkingTreePatch builds one git patch that carries the complete change from
// base to the current working tree: committed, uncommitted, and untracked
// files, plus deletions, renames, modes, and binary content. Files an ignore
// rule excludes stay out, except the selected policy files, which the service
// evaluates from the patched checkout and therefore must receive even when
// ignored locally. A check that also reviews the change captures the tree
// once with CaptureWorkingTree and diffs it with TreePatch instead, so both
// halves see the same content.
func WorkingTreePatch(root, base string, policyFiles []string) (WorkingChange, error) {
	snapshot, err := CaptureWorkingTree(root, policyFiles)
	if err != nil {
		return WorkingChange{}, err
	}
	return TreePatch(root, base, snapshot.PatchTree)
}

// keepSkippedPaths copies the user's skip-worktree bits into the temporary
// index. A sparse checkout leaves every file outside its cone off the disk
// and marks it so; without the bit, git would read each of those files as
// deleted and the patch would delete them. With it, the patch agrees with
// `git status`, which also leaves out a file someone marked by hand to keep
// their edits to it out of commits. Only paths HEAD seeded are marked, since
// update-index refuses a path the index does not hold. A selected policy file
// is never marked: the service evaluates its current content, and git refuses
// to add a marked path.
func keepSkippedPaths(root string, env []string, policyFiles []string) error {
	// The user's own index, as `git status` reads it.
	tagged, err := RunBytes(root, nil, "ls-files", "-t", "-z")
	if err != nil {
		return err
	}
	var skipped [][]byte
	for _, entry := range bytes.Split(tagged, []byte{0}) {
		if path, ok := bytes.CutPrefix(entry, []byte("S ")); ok {
			skipped = append(skipped, path)
		}
	}
	if len(skipped) == 0 {
		return nil
	}
	seeded, err := RunBytes(root, env, "ls-files", "-z")
	if err != nil {
		return err
	}
	inIndex := make(map[string]bool)
	for _, path := range bytes.Split(seeded, []byte{0}) {
		inIndex[string(path)] = true
	}
	for _, file := range policyFiles {
		delete(inIndex, file)
	}
	var marked bytes.Buffer
	for _, path := range skipped {
		if inIndex[string(path)] {
			marked.Write(path)
			marked.WriteByte(0)
		}
	}
	if marked.Len() == 0 {
		return nil
	}
	_, err = RunInput(root, env, marked.Bytes(), "update-index", "-z", "--skip-worktree", "--stdin")
	return err
}

func countDiffHeaders(patch []byte) int {
	count := 0
	for _, line := range bytes.Split(patch, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("diff --git ")) {
			count++
		}
	}
	return count
}

func chunks(values []string, size int) [][]string {
	var out [][]string
	for len(values) > size {
		out = append(out, values[:size])
		values = values[size:]
	}
	if len(values) > 0 {
		out = append(out, values)
	}
	return out
}

func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, prefix+value)
}

// ChangedPaths lists the repository-relative paths that pathspecs match and
// that differ from HEAD in the working tree, untracked ones included and
// ignored ones left out, as `git status` sees them. It reads only paths, never
// file contents, and takes no lock on the user's index.
func ChangedPaths(root string, pathspecs []string) ([]string, error) {
	args := append([]string{
		"--no-optional-locks", "-c", "core.quotePath=false",
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--",
	}, pathspecs...)
	out, err := RunBytes(root, nil, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range bytes.Split(out, []byte{0}) {
		// Each entry is two status letters, a space, then the path.
		if len(entry) > 3 {
			paths = append(paths, string(entry[3:]))
		}
	}
	return paths, nil
}
