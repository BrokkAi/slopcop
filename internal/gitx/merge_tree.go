package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// SupportsMergeTreeWriteTree reports whether Git meets Mjolnir's merge-tree floor.
func SupportsMergeTreeWriteTree(root string) (bool, error) {
	version, err := Run(root, "--version")
	if err != nil {
		return false, err
	}
	parts := regexp.MustCompile(`^git version (\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(version)
	if parts == nil {
		return false, fmt.Errorf("unrecognized Git version: %s", version)
	}
	floor := []int{2, 39, 5}
	for i := range floor {
		n, err := strconv.Atoi(parts[i+1])
		if err != nil {
			return false, err
		}
		if n != floor[i] {
			return n > floor[i], nil
		}
	}
	return true, nil
}

// MergeTreeBaseline absorbs mergedCommit into baseCommit for local review.
// Both inputs must be commits: merge-tree cannot fold a synthetic tree into
// another merge. Text conflict markers are retained to expose the resolution.
func MergeTreeBaseline(root, baseCommit, mergedCommit, headTree string) (string, error) {
	// Run merge-tree in an empty worktree with an empty index and attr.tree
	// cleared so repository .gitattributes merge drivers cannot execute or hide
	// conflict resolutions.
	gitDirOut, err := RunBytes(root, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSuffix(string(gitDirOut), "\n")
	emptyDir, err := os.MkdirTemp("", "slopcop-merge-tree-")
	if err != nil {
		return "", fmt.Errorf("create empty work tree: %w", err)
	}
	defer os.RemoveAll(emptyDir)
	mergeEnv := slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, "GIT_ATTR_SOURCE=")
	})
	mergeEnv = withEnv(mergeEnv, "GIT_INDEX_FILE", filepath.Join(emptyDir, "index"))
	command := exec.Command("git", "-C", emptyDir, "--git-dir="+gitDir, "--work-tree="+emptyDir, "-c", "attr.tree=", "merge-tree", "--write-tree", "--allow-unrelated-histories", "--no-messages", "--name-only", "-z", baseCommit, mergedCommit)
	command.Env = mergeEnv
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	conflicted := false
	if err != nil {
		var status *exec.ExitError
		if !errors.As(err, &status) || status.ExitCode() != 1 {
			return "", fmt.Errorf("git merge-tree: %w: %s", err, stderr.String())
		}
		conflicted = true
	}
	tokens := bytes.Split(output, []byte{0})
	tree := string(tokens[0])
	if tree == "" {
		return "", fmt.Errorf("git merge-tree printed no tree id")
	}
	if !conflicted {
		return tree, nil
	}
	var removals bytes.Buffer
	seen := map[string]bool{}
	for _, token := range tokens[1:] {
		name := string(token)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		entries := make([][]byte, 2)
		for i, treeish := range []string{tree, headTree} {
			out, err := RunBytes(root, nil, "--literal-pathspecs", "ls-tree", "-r", "-z", treeish, "--", name)
			if err != nil {
				return "", err
			}
			// Match the exact entry, not descendants selected by a directory pathspec.
			for _, record := range bytes.Split(out, []byte{0}) {
				metadata, path, ok := bytes.Cut(record, []byte{'\t'})
				if ok && string(path) == name {
					entries[i] = metadata
				}
			}
		}
		if len(entries[0]) > 0 && bytes.Equal(entries[0], entries[1]) {
			removals.WriteString(name)
			removals.WriteByte(0)
		}
	}
	if removals.Len() == 0 {
		return tree, nil
	}
	index, err := os.CreateTemp("", "slopcop-merge-index-")
	if err != nil {
		return "", err
	}
	name := index.Name()
	defer os.Remove(name)
	if err := index.Close(); err != nil {
		return "", err
	}
	// read-tree requires either a valid index or a nonexistent file.
	if err := os.Remove(name); err != nil {
		return "", err
	}
	env := withEnv(os.Environ(), "GIT_INDEX_FILE", name)
	if _, err := RunBytes(root, env, "read-tree", tree); err != nil {
		return "", err
	}
	if _, err := RunInput(root, env, removals.Bytes(), "update-index", "--force-remove", "-z", "--stdin"); err != nil {
		return "", err
	}
	out, err := RunBytes(root, env, "write-tree")
	return strings.TrimSpace(string(out)), err
}
