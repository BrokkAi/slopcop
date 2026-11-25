package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportPerFilePatches(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s: %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	write := func(p, s string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(root, p), []byte(s), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("edit", "before\n")
	write("delete", "deleted\n")
	write("binary", "\x00before readable text\n")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD^{tree}")
	write("edit", "after\n")
	os.Remove(filepath.Join(root, "delete"))
	write("binary", "\x00after readable text\n")
	write("add", "new\n")
	git("add", "-A")
	tree := git("write-tree")
	dest := t.TempDir()
	result, e := ExportPerFilePatches(context.Background(), root, base, tree, dest, 1<<20, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	if result.Files != 4 {
		t.Fatal(result)
	}
	index, e := os.ReadFile(filepath.Join(dest, "patches.txt"))
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range strings.Split(strings.TrimSpace(string(index)), "\n") {
		cols := strings.Split(row, "\t")
		data, e := os.ReadFile(filepath.Join(dest, cols[2]))
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(data), "diff --git") {
			t.Fatal(string(data))
		}
		if cols[1] == "binary" && !strings.Contains(string(data), "after readable text") {
			t.Fatal(string(data))
		}
	}
	if _, e = ExportPerFilePatches(context.Background(), root, base, tree, t.TempDir(), 1<<20, 1); e == nil {
		t.Fatal("total limit ignored")
	}
}

func TestPatchLimitsFailClosed(t *testing.T) {
	for _, binary := range []bool{false, true} {
		for _, size := range []int{40 << 10, 1100 << 10} {
			root := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				c := exec.Command("git", args...)
				c.Dir = root
				b, err := c.CombinedOutput()
				if err != nil {
					t.Fatalf("git: %s %v", b, err)
				}
				return strings.TrimSpace(string(b))
			}
			git("init")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.com")
			git("commit", "--allow-empty", "-m", "base")
			base := git("rev-parse", "HEAD^{tree}")
			content := strings.Repeat("readable script line\n", size/21) + "FINAL_LINE\n"
			if binary {
				content = "\x00\n" + content
			}
			if err := os.WriteFile(filepath.Join(root, "script"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", ".")
			tree := git("write-tree")
			patch, err := FilePatch(context.Background(), root, base, tree, "script")
			dest := t.TempDir()
			_, exportErr := ExportPerFilePatches(context.Background(), root, base, tree, dest, 1<<20, 8<<20)
			if size > 1<<20 {
				if !errors.Is(err, ErrPatchTruncated) || !errors.Is(exportErr, ErrPatchTruncated) {
					t.Fatalf("binary=%v: %v, %v", binary, err, exportErr)
				}
			} else {
				if err != nil || exportErr != nil || !strings.Contains(string(patch), "FINAL_LINE") {
					t.Fatalf("binary=%v: incomplete patch: %v, %v", binary, err, exportErr)
				}
				exported, err := os.ReadFile(filepath.Join(dest, "patches/000001.patch"))
				if err != nil || string(exported) != string(patch) {
					t.Fatalf("export differs: %v", err)
				}
			}
		}
	}
}
