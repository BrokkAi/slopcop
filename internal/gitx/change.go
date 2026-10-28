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

// WorkingTreePatch builds one git patch that carries the complete change from
// base to the current working tree: committed, uncommitted, and untracked
// files, plus deletions, renames, modes, and binary content.
//
// The patch is built through a temporary index, so the user's index and
// worktree are never modified. Untracked files join through
// `--intent-to-add`; files an ignore rule excludes stay out, except the
// selected policy files, which the service evaluates from the patched
// checkout and therefore must receive even when ignored locally.
func WorkingTreePatch(root, base string, policyFiles []string) (WorkingChange, error) {
	var change WorkingChange
	dir, err := os.MkdirTemp("", "slopcop-index-")
	if err != nil {
		return change, fmt.Errorf("create temporary index: %w", err)
	}
	defer os.RemoveAll(dir)

	env := withEnv(os.Environ(), "GIT_INDEX_FILE", filepath.Join(dir, "index"))
	// Seed the index from HEAD so git can keep the committed mode where the
	// filesystem cannot express it: with core.filemode=false or
	// core.symlinks=false (Git for Windows' defaults), a path with no index
	// entry reads as 100644, which would ship every executable and symlink as
	// a mode change.
	if _, err := RunBytes(root, env, "read-tree", "HEAD"); err != nil {
		return change, err
	}
	if err := keepSkippedPaths(root, env); err != nil {
		return change, err
	}
	if _, err := RunBytes(root, env, "add", "--intent-to-add", "--all", "--", "."); err != nil {
		return change, err
	}

	for _, file := range policyFiles {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
			return change, fmt.Errorf("selected policy file not found: %s", file)
		}
	}
	for _, chunk := range chunks(policyFiles, 256) {
		args := append(
			[]string{"--literal-pathspecs", "add", "--intent-to-add", "--force", "--"},
			chunk...,
		)
		if _, err := RunBytes(root, env, args...); err != nil {
			return change, err
		}
	}

	patch, err := RunBytes(root, env,
		"-c", "core.quotePath=false",
		"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--binary",
		"--full-index", "--ignore-submodules=none",
		"--src-prefix=a/", "--dst-prefix=b/", base,
	)
	if err != nil {
		return change, err
	}
	change.Files = countDiffHeaders(patch)
	change.Patch = patch
	return change, nil
}

// keepSkippedPaths copies the user's skip-worktree bits into the temporary
// index. A sparse checkout leaves every file outside its cone off the disk
// and marks it so; without the bit, git would read each of those files as
// deleted and the patch would delete them. With it, the patch agrees with
// `git status`. Only paths HEAD seeded are marked, since update-index refuses
// a path the index does not hold.
func keepSkippedPaths(root string, env []string) error {
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
