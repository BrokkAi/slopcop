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
