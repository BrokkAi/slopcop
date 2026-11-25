package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeTreeBaseline(t *testing.T) {
	for _, kind := range []string{"clean", "text", "binary", "unrelated"} {
		t.Run(kind, func(t *testing.T) {
			root := initRepo(t)
			supported, err := SupportsMergeTreeWriteTree(root)
			if err != nil {
				t.Fatal(err)
			}
			if !supported {
				t.Skip("requires Git >= 2.39.5")
			}
			put := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			commit := func() string {
				git(t, root, "add", "-A")
				git(t, root, "commit", "-qm", "change")
				return git(t, root, "rev-parse", "HEAD")
			}
			content := "initial\n"
			if kind == "binary" {
				content = "initial\x00"
			}
			put("conflict", content)
			commit()
			git(t, root, "checkout", "-qb", "stack")
			put("stack", "stack\n")
			if kind == "text" {
				put("conflict", "stack\n")
			}
			if kind == "binary" {
				put("conflict", "stack\x00")
			}
			base := commit()
			if kind == "unrelated" {
				git(t, root, "checkout", "--orphan", "incoming")
				git(t, root, "rm", "-rf", ".")
			} else {
				git(t, root, "checkout", "main")
			}
			put("incoming", "incoming\n")
			if kind == "text" {
				put("conflict", "incoming\n")
			}
			if kind == "binary" {
				put("conflict", "incoming\x00")
			}
			merged := commit()
			git(t, root, "checkout", "stack")
			// Construct the author's resolved tree independently of merge's exit status.
			put("incoming", "incoming\n")
			put("own", "own\n")
			if kind == "text" {
				put("conflict", "resolved\n")
			}
			head := commit()
			baseline, err := MergeTreeBaseline(root, base, merged, head+"^{tree}")
			if err != nil {
				t.Fatal(err)
			}
			paths := git(t, root, "diff", "--name-only", baseline, head)
			if strings.Contains(paths, "incoming") || strings.Contains(paths, "stack") || !strings.Contains(paths, "own") {
				t.Fatalf("unexpected diff: %s", paths)
			}
			if kind == "text" {
				markers := git(t, root, "show", baseline+":conflict")
				if !strings.Contains(markers, "<<<<<<<") {
					t.Fatalf("missing conflict markers: %s", markers)
				}
			}
			if kind == "binary" {
				if got := git(t, root, "ls-tree", baseline, "--", "conflict"); got != "" {
					t.Fatalf("fallback conflict retained: %s", got)
				}
				if !strings.Contains(paths, "conflict") {
					t.Fatal("resolution hidden")
				}
			}
		})
	}
}

func TestMergeTreeBaselineRejectsInvalidCommit(t *testing.T) {
	root := initRepo(t)
	if _, err := MergeTreeBaseline(root, "missing", "also-missing", "HEAD"); err == nil {
		t.Fatal("expected merge-tree failure")
	}
}
