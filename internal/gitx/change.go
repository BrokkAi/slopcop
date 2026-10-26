package gitx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkingChange carries the snapshot and its self-contained transfer pack.
type WorkingChange struct {
	Pack   []byte
	Commit string
	Files  int
}

// WorkingTreePack snapshots the working tree using a temporary index. Ignored
// files stay out except selected policy files. The real index is untouched.
// Even a clean tree gets a snapshot commit, with fixed identity and timestamps
// so identical trees and BASE parents produce identical commit IDs. Both
// comparison snapshots are parented by BASE to exclude branch history.
func WorkingTreePack(root, base, since string, policyFiles []string) (WorkingChange, error) {
	var change WorkingChange
	dir, err := os.MkdirTemp("", "slopcop-index-")
	if err != nil {
		return change, fmt.Errorf("create temporary index: %w", err)
	}
	defer os.RemoveAll(dir)
	env := withEnv(os.Environ(), "GIT_INDEX_FILE", filepath.Join(dir, "index"))
	// Preserve committed executable and symlink modes on filesystems that
	// cannot express them (core.filemode=false or core.symlinks=false).
	head, err := HeadCommit(root)
	if err != nil {
		return change, err
	}
	if _, err = RunBytes(root, env, "read-tree", head); err != nil {
		return change, err
	}
	if _, err = RunBytes(root, env, "add", "--all", "--", "."); err != nil {
		return change, err
	}
	for _, file := range policyFiles {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(file))); err != nil {
			return change, fmt.Errorf("selected policy file not found: %s", file)
		}
	}
	for _, chunk := range chunks(policyFiles, 256) {
		args := append([]string{"--literal-pathspecs", "add", "--force", "--"}, chunk...)
		if _, err = RunBytes(root, env, args...); err != nil {
			return change, err
		}
	}
	tree, err := RunBytes(root, env, "write-tree")
	if err != nil {
		return change, err
	}
	change.Commit, err = snapshotCommit(root, strings.TrimSpace(string(tree)), base, env)
	if err != nil {
		return change, err
	}
	files, err := RunBytes(root, env, "diff", "--name-only", "-z", "--no-ext-diff", base, change.Commit, "--")
	if err != nil {
		return change, err
	}
	change.Files = bytes.Count(files, []byte{0})
	// No --thin: deltas must never depend on objects outside this pack.
	change.Pack, err = runBytesInput(root, env, []byte(change.Commit+"\n"+since+"\n^"+base+"\n"),
		"pack-objects", "--revs", "--stdout", "--no-reuse-delta")
	return change, err
}

// snapshotCommit anchors a tree directly to BASE, without carrying its original
// history. Fixed metadata makes wrapping an existing snapshot idempotent.
func snapshotCommit(root, tree, base string, env []string) (string, error) {
	if env == nil {
		env = os.Environ()
	}
	for key, value := range map[string]string{
		"GIT_AUTHOR_NAME": "SlopCop", "GIT_COMMITTER_NAME": "SlopCop",
		"GIT_AUTHOR_EMAIL": "snapshot@slopcop.com", "GIT_COMMITTER_EMAIL": "snapshot@slopcop.com",
		"GIT_AUTHOR_DATE": "2000-01-01T00:00:00 +0000", "GIT_COMMITTER_DATE": "2000-01-01T00:00:00 +0000",
	} {
		env = withEnv(env, key, value)
	}
	commit, err := RunBytes(root, env, "-c", "commit.gpgSign=false", "commit-tree", tree, "-p", base, "-m", "slopcop snapshot")
	return strings.TrimSpace(string(commit)), err
}

func snapshotSince(root, base, commit string) (string, error) {
	tree, err := Run(root, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return "", err
	}
	baseTree, err := Run(root, "rev-parse", "--verify", base+"^{tree}")
	if err != nil {
		return "", err
	}
	if tree == baseTree {
		return base, nil
	}
	return snapshotCommit(root, tree, base, nil)
}

// ResolveSince returns the comparison commit and the branch ref to advance
// after a passing run. Non-base trees are wrapped in deterministic commits
// parented by the current BASE. Detached HEADs have no last-run ref.
func ResolveSince(root, base, rev string) (commit, ref string, err error) {
	// The full refname, not --short, which turns `feat` into `heads/feat`
	// once a tag `feat` exists and so would lose the marker.
	head, headErr := Run(root, "symbolic-ref", "--quiet", "HEAD")
	if branch, ok := strings.CutPrefix(head, "refs/heads/"); headErr == nil && ok && branch != "" {
		ref = "refs/slopcop/last-run/" + branch
	}
	switch rev {
	case "merge-base":
		return base, ref, nil
	case "":
		if ref != "" {
			if last, resolveErr := ResolveCommit(root, ref); resolveErr == nil {
				if _, existsErr := Run(root, "cat-file", "-e", last+"^{commit}"); existsErr == nil {
					commit, err = snapshotSince(root, base, last)
					return commit, ref, err
				}
			}
		}
		return base, ref, nil
	default:
		commit, err = ResolveCommit(root, rev)
		if err != nil {
			return "", ref, err
		}
		commit, err = snapshotSince(root, base, commit)
		return commit, ref, err
	}
}

// RecordLastRun advances the captured branch's marker, skipping detached HEAD.
func RecordLastRun(root, ref, commit string) error {
	if ref == "" {
		return nil
	}
	_, err := Run(root, "update-ref", ref, commit)
	return err
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
