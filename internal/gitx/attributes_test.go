package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterCheckTreeIgnoresRepoAttributes(t *testing.T) {
	root := initRepo(t)
	git(t, root, "commit", "-q", "--allow-empty", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD")
	large := int(MaxCheckDataBytes) + 1
	for name, content := range map[string]string{
		".gitattributes": "marked.txt binary\n",
		"marked.txt":     strings.Repeat("t", large),
		"real.bin":       "\x00" + strings.Repeat("b", large),
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "change")
	filtered, err := FilterCheckTree(root, base, git(t, root, "rev-parse", "HEAD^{tree}"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.OmittedFiles) != 1 || filtered.OmittedFiles[0].Path != "real.bin" || filtered.OmittedFiles[0].Reason != "large-binary" {
		t.Fatalf("want only the real binary omitted, got %+v", filtered.OmittedFiles)
	}
}

// A linked worktree keeps its git directory elsewhere and its own checkout of
// .gitattributes.
func TestRunWithoutRepoAttributesInLinkedWorktree(t *testing.T) {
	root := initRepo(t)
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	base := git(t, root, "rev-parse", "HEAD^{tree}")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "-q", "-b", "side", linked)
	for name, content := range map[string]string{".gitattributes": "a.txt binary\n", "a.txt": "one\ntwo\n"} {
		if err := os.WriteFile(filepath.Join(linked, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, linked, "add", "-A")
	git(t, linked, "commit", "-q", "-m", "change")
	tree := git(t, linked, "rev-parse", "HEAD^{tree}")
	if plain := git(t, linked, "diff", base, tree); !strings.Contains(plain, "Binary files") {
		t.Fatalf("fixture does not hide the edit from a plain diff:\n%s", plain)
	}
	out, err := RunWithoutRepoAttributes(linked, "diff", "--no-ext-diff", "--no-textconv", base, tree)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "+two") {
		t.Fatalf("edit not shown:\n%s", out)
	}
}
