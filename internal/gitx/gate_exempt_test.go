package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exemptionCommit(t *testing.T, root, path, content string) string {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "--", path)
	git(t, root, "commit", "-qm", "change")
	return git(t, root, "rev-parse", "HEAD")
}
func exemptionTree(t *testing.T, root string) string {
	t.Helper()
	return git(t, root, "rev-parse", "HEAD^{tree}")
}
func exemptionReview(t *testing.T, root string) map[string]bool {
	t.Helper()
	tree := exemptionTree(t, root)
	git(t, root, "update-ref", "refs/slopcop/reviewed/"+tree, tree)
	trees, err := ReviewedTrees(root)
	if err != nil {
		t.Fatal(err)
	}
	return trees
}
func TestDocumentationPaths(t *testing.T) {
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"README.md", true}, {"docs/guide.md", true}, {"doc/foo.rst", true}, {".agents/plans/plan.md", true}, {"CHANGELOG", true}, {"LICENSE", true}, {"COPYING", true}, {"AUTHORS", true}, {"CONTRIBUTORS", true}, {"NEWS", true}, {"docs/with space\nand newline.adoc", true}, {"nested/docs/a.asciidoc", true}, {"CONTRIBUTING.markdown", true},
		{"AGENTS.md", false}, {"docs/AGENTS.override.md", false}, {"CLAUDE.md", false}, {"docs/CLAUDE.local.md", false}, {"docs/GEMINI.md", false}, {"docs/SKILL.md", false}, {"docs/a.instructions.md", false}, {"docs/a.prompt.md", false}, {".agents/foo.md", false}, {".agents/skills/x/a.md", false}, {".github/foo.md", false}, {".slopcop/config.toml", false}, {".bifrostignore", false}, {"foo.md", false}, {"main.go", false}, {"README/main.go", false}, {"docs/a.MD", false}, {"nested/LICENSE", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			root := initRepo(t)
			git(t, root, "commit", "--allow-empty", "-qm", "base")
			base := exemptionTree(t, root)
			exemptionCommit(t, root, tc.path, "text\n")
			got, err := IsDocumentationOnlyDiff(root, base, exemptionTree(t, root))
			if err != nil || got != tc.allowed {
				t.Fatalf("got %v, %v; want %v", got, err, tc.allowed)
			}
		})
	}
}
func TestDocumentationNonPlainChanges(t *testing.T) {
	for _, kind := range []string{"binary", "executable", "symlink", "gitlink", "rename", "attributes"} {
		t.Run(kind, func(t *testing.T) {
			root := initRepo(t)
			baseCommit := exemptionCommit(t, root, "code.go", "code\n")
			base := exemptionTree(t, root)
			switch kind {
			case "binary", "attributes":
				if kind == "attributes" {
					exemptionCommit(t, root, ".gitattributes", "*.md diff\n")
					base = exemptionTree(t, root)
				}
				exemptionCommit(t, root, "docs/a.md", "binary\x00data\n")
			case "executable":
				exemptionCommit(t, root, "docs/a.md", "text\n")
				git(t, root, "update-index", "--chmod=+x", "docs/a.md")
				git(t, root, "commit", "-qm", "mode")
			case "symlink":
				if err := os.MkdirAll(filepath.Join(root, "docs"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../code.go", filepath.Join(root, "docs/a.md")); err != nil {
					t.Skip(err)
				}
				git(t, root, "add", ".")
				git(t, root, "commit", "-qm", "link")
			case "gitlink":
				git(t, root, "update-index", "--add", "--cacheinfo", "160000,"+baseCommit+",docs/a.md")
				git(t, root, "commit", "-qm", "gitlink")
			case "rename":
				if err := os.MkdirAll(filepath.Join(root, "docs"), 0755); err != nil {
					t.Fatal(err)
				}
				git(t, root, "mv", "code.go", "docs/a.md")
				git(t, root, "commit", "-qm", "rename")
			}
			ok, err := IsDocumentationOnlyDiff(root, base, exemptionTree(t, root))
			if err != nil || ok {
				t.Fatalf("allowed=%v err=%v", ok, err)
			}
		})
	}
}
func TestDocumentationPushBaselines(t *testing.T) {
	for _, kind := range []string{"upstream", "reviewed", "new-remote", "mismatch", "initial", "unresolved"} {
		t.Run(kind, func(t *testing.T) {
			root := initRepo(t)
			trees := map[string]bool{}
			if kind != "initial" {
				base := exemptionCommit(t, root, "code.go", "base\n")
				git(t, root, "remote", "add", "origin", "https://example.com/repo.git")
				git(t, root, "update-ref", "refs/remotes/origin/main", base)
				if kind == "upstream" || kind == "reviewed" || kind == "unresolved" {
					git(t, root, "branch", "--set-upstream-to=origin/main")
				}
				if kind == "reviewed" {
					exemptionCommit(t, root, "code.go", "reviewed\n")
					trees = exemptionReview(t, root)
				}
				if kind == "mismatch" {
					git(t, root, "remote", "set-url", "--push", "origin", "https://example.com/other.git")
				}
				if kind == "unresolved" {
					git(t, root, "update-ref", "-d", "refs/remotes/origin/main")
				}
			}
			if kind == "initial" {
				git(t, root, "remote", "add", "origin", "https://example.com/repo.git")
			}
			head := exemptionCommit(t, root, "README.md", "docs\n")
			ok, err := IsDocumentationOnlyPush(root, head, exemptionTree(t, root), trees, PushDestination{AllowExemptions: true})
			want := kind != "mismatch" && kind != "unresolved"
			if err != nil || ok != want {
				t.Fatalf("allowed=%v err=%v want=%v", ok, err, want)
			}
		})
	}
}
func TestCleanUpstreamMerges(t *testing.T) {
	for _, kind := range []string{"clean", "extra", "intermediate", "conflict", "nonmerge", "octopus", "force", "ambiguous", "mismatch"} {
		t.Run(kind, func(t *testing.T) {
			root := initRepo(t)
			base := exemptionCommit(t, root, "code.go", "base\n")
			git(t, root, "remote", "add", "origin", "https://example.com/repo.git")
			git(t, root, "checkout", "-qb", "feature")
			exemptionCommit(t, root, "feature.go", "reviewed\n")
			trees := exemptionReview(t, root)
			if kind == "ambiguous" {
				git(t, root, "checkout", "-qb", "other", base)
				exemptionCommit(t, root, "other.go", "other\n")
				for tree := range exemptionReview(t, root) {
					trees[tree] = true
				}
				git(t, root, "checkout", "feature")
				git(t, root, "merge", "--no-edit", "other")
			}
			if kind == "intermediate" {
				exemptionCommit(t, root, "unreviewed.go", "new\n")
			}
			if kind == "conflict" {
				exemptionCommit(t, root, "code.go", "local\n")
			}
			p1 := git(t, root, "rev-parse", "HEAD")
			git(t, root, "checkout", "main")
			exemptionCommit(t, root, "upstream.go", "upstream\n")
			if kind == "conflict" {
				exemptionCommit(t, root, "code.go", "remote\n")
			}
			p2 := git(t, root, "rev-parse", "HEAD")
			git(t, root, "update-ref", "refs/remotes/origin/main", p2)
			git(t, root, "checkout", "feature")
			if kind == "nonmerge" {
				p2 = p1
			}
			if kind == "conflict" {
				tree := exemptionTree(t, root)
				merge := git(t, root, "commit-tree", tree, "-p", p1, "-p", p2, "-m", "resolved")
				git(t, root, "reset", "--hard", merge)
			} else if kind != "nonmerge" {
				git(t, root, "merge", "--no-edit", "main")
			}
			if kind == "extra" {
				exemptionCommit(t, root, "extra.go", "extra\n")
				tree := exemptionTree(t, root)
				merge := git(t, root, "commit-tree", tree, "-p", p1, "-p", p2, "-m", "extra")
				git(t, root, "reset", "--hard", merge)
			}
			if kind == "octopus" {
				tree := exemptionTree(t, root)
				merge := git(t, root, "commit-tree", tree, "-p", p1, "-p", p2, "-p", base, "-m", "octopus")
				git(t, root, "reset", "--hard", merge)
			}
			if kind == "force" {
				git(t, root, "checkout", "-qb", "discarded", base)
				discarded := exemptionCommit(t, root, "discarded.go", "lost\n")
				git(t, root, "update-ref", "refs/remotes/origin/destination", discarded)
				git(t, root, "checkout", "feature")
				git(t, root, "branch", "--set-upstream-to=origin/destination")
			}
			if kind == "mismatch" {
				git(t, root, "remote", "set-url", "--push", "origin", "https://example.com/other.git")
			}
			ok, err := IsCleanUpstreamMergePush(root, git(t, root, "rev-parse", "HEAD"), exemptionTree(t, root), trees, PushDestination{AllowExemptions: true})
			if err != nil || ok != (kind == "clean") {
				t.Fatalf("allowed=%v err=%v", ok, err)
			}
		})
	}
}
func TestReviewedTreesRejectsMalformedRefs(t *testing.T) {
	root := initRepo(t)
	commit := exemptionCommit(t, root, "README.md", "docs\n")
	tree := exemptionTree(t, root)
	git(t, root, "update-ref", "refs/slopcop/reviewed/wrong", tree)
	git(t, root, "update-ref", "refs/slopcop/reviewed/"+commit, commit)
	trees, err := ReviewedTrees(root)
	if err != nil || len(trees) != 0 {
		t.Fatalf("%v %v", trees, err)
	}
	trees = exemptionReview(t, root)
	if len(trees) != 1 || !trees[tree] {
		t.Fatal(trees)
	}
}

func TestPushDestinationHistory(t *testing.T) {
	for _, kind := range []string{"origin", "explicit", "other", "pushRemote", "pushDefault", "different-ref", "unknown", "discarded"} {
		t.Run(kind, func(t *testing.T) {
			root := initRepo(t)
			base := exemptionCommit(t, root, "code.go", "base\n")
			git(t, root, "remote", "add", "origin", "https://example.com/origin.git")
			git(t, root, "remote", "add", "fork", "https://example.com/fork.git")
			git(t, root, "update-ref", "refs/remotes/origin/main", base)
			git(t, root, "branch", "--set-upstream-to=origin/main")
			dest := PushDestination{AllowExemptions: true}
			switch kind {
			case "explicit":
				dest.Remote, dest.DestRef = "origin", "main"
			case "other":
				dest.Remote, dest.DestRef = "fork", "main"
			case "pushRemote":
				git(t, root, "config", "branch.main.pushRemote", "fork")
			case "pushDefault":
				git(t, root, "config", "remote.pushDefault", "fork")
			case "different-ref":
				dest.DestRef = "new"
			case "unknown":
				dest.AllowExemptions = false
			case "discarded":
				discarded := exemptionCommit(t, root, "README.md", "discarded\n")
				git(t, root, "update-ref", "refs/remotes/origin/main", discarded)
				git(t, root, "reset", "--hard", base)
			}
			head := exemptionCommit(t, root, "README.md", "docs\n")
			ok, err := IsDocumentationOnlyPush(root, head, exemptionTree(t, root), nil, dest)
			want := kind == "origin" || kind == "explicit" || kind == "different-ref"
			if err != nil || ok != want {
				t.Fatalf("allowed=%v err=%v want=%v", ok, err, want)
			}
		})
	}
}

func TestDocumentationInitialPushSHA256(t *testing.T) {
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"README.md", true},
		{"code.go", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			root := t.TempDir()
			git(t, root, "init", "-q", "-b", "main", "--object-format=sha256", ".")
			for _, setting := range fixtureConfig() {
				key, value, _ := strings.Cut(setting, "=")
				git(t, root, "config", key, value)
			}
			git(t, root, "config", "user.email", "test@example.com")
			git(t, root, "config", "user.name", "Test")
			git(t, root, "remote", "add", "origin", "https://example.com/repo.git")
			head := exemptionCommit(t, root, tc.path, "content\n")
			ok, err := IsDocumentationOnlyPush(root, head, exemptionTree(t, root), map[string]bool{}, PushDestination{AllowExemptions: true})
			if err != nil || ok != tc.allowed {
				t.Fatalf("allowed=%v err=%v want=%v", ok, err, tc.allowed)
			}
		})
	}
}

func TestDocumentationCustomFetchRefspecRejected(t *testing.T) {
	root := initRepo(t)
	git(t, root, "remote", "add", "origin", "https://example.com/repo.git")
	// Map remote's prod branch into refs/remotes/origin/main instead of refs/heads/main.
	git(t, root, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/prod:refs/remotes/origin/main")
	code := exemptionCommit(t, root, "app.go", "package app\n")
	git(t, root, "update-ref", "refs/remotes/origin/main", code)
	head := exemptionCommit(t, root, "README.md", "docs\n")
	ok, err := IsDocumentationOnlyPush(root, head, exemptionTree(t, root), nil, PushDestination{Remote: "origin", DestRef: "main", AllowExemptions: true})
	if err != nil || ok {
		t.Fatalf("custom fetch refspec should not exempt push to main: ok=%v err=%v", ok, err)
	}
}

func TestDocumentationRejectsLocalBranchFetchTargetAndStaleFallbackRefs(t *testing.T) {
	t.Run("local branch fetch destination", func(t *testing.T) {
		root := initRepo(t)
		git(t, root, "remote", "add", "origin", "https://example.com/repo.git")
		// Mapping into refs/heads/main would compare HEAD against itself.
		git(t, root, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/main:refs/heads/main")
		head := exemptionCommit(t, root, "app.go", "package app\n")
		ok, err := IsDocumentationOnlyPush(root, head, exemptionTree(t, root), nil, PushDestination{Remote: "origin", DestRef: "main", AllowExemptions: true})
		if err != nil || ok {
			t.Fatalf("local branch fetch target must not exempt unreviewed code: ok=%v err=%v", ok, err)
		}
	})
	t.Run("stale unmapped fallback ref", func(t *testing.T) {
		root := initRepo(t)
		git(t, root, "remote", "add", "origin", "https://example.com/repo.git")
		// Narrow fetch refspec so refs/remotes/origin/stale is no longer covered.
		git(t, root, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/only/*:refs/remotes/origin/only/*")
		code := exemptionCommit(t, root, "app.go", "package app\n")
		git(t, root, "update-ref", "refs/remotes/origin/stale", code)
		git(t, root, "checkout", "-qb", "only/topic")
		head := exemptionCommit(t, root, "README.md", "docs\n")
		ok, err := IsDocumentationOnlyPush(root, head, exemptionTree(t, root), nil, PushDestination{Remote: "origin", DestRef: "only/topic", AllowExemptions: true})
		if err != nil || ok {
			t.Fatalf("stale ref outside fetch refspec must not serve as fallback baseline: ok=%v err=%v", ok, err)
		}
	})
}
