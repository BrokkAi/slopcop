package cli

import (
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/gitx"
)

func TestPrepareCheckSyntheticMergeBaseline(t *testing.T) {
	for _, mode := range []string{"stacked", "default", "exact"} {
		t.Run(mode, func(t *testing.T) {
			root := newRepo(t, "")
			supported, err := gitx.SupportsMergeTreeWriteTree(root)
			if err != nil {
				t.Fatal(err)
			}
			if !supported {
				t.Skip("requires Git >= 2.39.5")
			}
			initial := runGit(t, root, "rev-parse", "HEAD")
			runGit(t, root, "checkout", "-qb", "stack")
			writeFile(t, root, "stack.txt", "stack\n")
			runGit(t, root, "add", "-A")
			runGit(t, root, "commit", "-qm", "stack")
			base := runGit(t, root, "rev-parse", "HEAD")
			runGit(t, root, "checkout", "-qb", "work")
			runGit(t, root, "update-ref", "refs/remotes/origin/work", base)
			runGit(t, root, "config", "remote.origin.url", "https://example.com/repo.git")
			runGit(t, root, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
			runGit(t, root, "config", "branch.work.remote", "origin")
			runGit(t, root, "config", "branch.work.merge", "refs/heads/work")
			runGit(t, root, "checkout", "-qb", "incoming", initial)
			writeFile(t, root, "incoming.txt", "incoming\n")
			runGit(t, root, "add", "-A")
			runGit(t, root, "commit", "-qm", "incoming")
			merged := runGit(t, root, "rev-parse", "HEAD")
			runGit(t, root, "update-ref", "refs/remotes/origin/main", merged)
			runGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			runGit(t, root, "checkout", "work")
			runGit(t, root, "merge", "--no-edit", "incoming")
			writeFile(t, root, "own.txt", "own\n")
			opts := &checkOptions{root: root, mode: modeAI}
			if mode == "stacked" {
				opts.base = "stack"
			}
			if mode == "exact" {
				opts.baseExact = base
			}
			prepared, err := prepareCheck(opts, config.Config{}, "", "test", "finding")
			if err != nil {
				t.Fatal(err)
			}
			if prepared.request.BaseCommit != base {
				t.Fatalf("hosted base changed: %s, want %s", prepared.request.BaseCommit, base)
			}
			if mode == "exact" {
				if prepared.reviewBaseTree != "" || len(prepared.reviewMergedIn) != 0 {
					t.Fatal("exact base synthesized")
				}
				return
			}
			if prepared.reviewBaseTree == "" || len(prepared.reviewMergedIn) != 1 || prepared.reviewMergedIn[0] != merged || prepared.reviewUnattributed != "" {
				t.Fatalf("missing attribution: tree=%s merged=%v reason=%s", prepared.reviewBaseTree, prepared.reviewMergedIn, prepared.reviewUnattributed)
			}
			paths := runGit(t, root, "diff", "--name-only", prepared.reviewBaseTree, prepared.reviewTree)
			if strings.TrimSpace(paths) != "own.txt" {
				t.Fatalf("local diff: %s", paths)
			}
		})
	}
}
