package gitx

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func exportRepo(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := Run(root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git("init")
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "alpha\n", 0600)
	write("dir/run.sh", "#!/bin/sh\n", 0700)
	write(".agents/hooks.json", "{}", 0600)
	write("big.bin", strings.Repeat("x", 100), 0600)
	// Attributes must not change what a reviewer reads.
	write(".gitattributes", "a.txt export-ignore\n", 0600)
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	git("add", "-A")
	blob := git("rev-parse", ":a.txt")
	git("update-index", "--add", "--cacheinfo", "160000,"+blob+",submodule")
	return root, git("write-tree")
}

func TestExportTree(t *testing.T) {
	root, tree := exportRepo(t)
	dest := t.TempDir()
	paths, err := ExportTree(context.Background(), root, tree, dest, ExportLimits{MaxFile: 50, MaxTotal: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".agents/hooks.json", ".gitattributes", "a.txt", "dir/run.sh"}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths %v, want %v", paths, want)
	}
	data, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	if err != nil || string(data) != "alpha\n" {
		t.Fatalf("a.txt = %q, %v", data, err)
	}
	for _, absent := range []string{"link", "submodule", "big.bin"} {
		if _, err := os.Lstat(filepath.Join(dest, absent)); !os.IsNotExist(err) {
			t.Errorf("%s was exported: %v", absent, err)
		}
	}
}

func TestChangedFiles(t *testing.T) {
	root, tree := exportRepo(t)
	base, err := Run(root, "hash-object", "-w", "-t", "tree", os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ChangedFiles(context.Background(), root, base, tree)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".agents/hooks.json", ".gitattributes", "a.txt", "big.bin", "dir/run.sh"}
	if !slices.Equal(got, want) {
		t.Fatalf("changed %v, want %v (symlinks and submodules are not regular files)", got, want)
	}
	if got, err = ChangedFiles(context.Background(), root, tree, base); err != nil || len(got) != 0 {
		t.Fatalf("deletions = %v, %v", got, err)
	}
}

func TestExportTreeRefusesATreeOverTheLimit(t *testing.T) {
	root, tree := exportRepo(t)
	if _, err := ExportTree(context.Background(), root, tree, t.TempDir(), ExportLimits{MaxFile: 1000, MaxTotal: 10}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}

func TestSafeExportPath(t *testing.T) {
	for path, want := range map[string]bool{
		"a/b.txt":        true,
		".agents/x":      true,
		".gitattributes": true,
		"../escape":      false,
		"a/../../escape": false,
		"/etc/passwd":    false,
		".git/config":    false,
		"sub/.GIT/hooks": false,
		"a//b":           false,
		`a\b`:            false,
	} {
		if got := safeExportPath(path); got != want {
			t.Errorf("safeExportPath(%q) = %v, want %v", path, got, want)
		}
	}
}
