package reposize

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func write(t *testing.T, root, name string, size int) {
	t.Helper()
	writeText(t, root, name, strings.Repeat("x", size))
}

func writeText(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repository commits files, each name mapped to its size, and returns the
// repository and the commit.
func repository(t *testing.T, files map[string]int) (string, string) {
	t.Helper()
	// A user's global excludes file must not change what counts.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	writeText(t, home, "config/git/ignore", "records/\n")
	root := t.TempDir()
	git(t, root, "init", "-q", "-b", "main", ".")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "config", "user.name", "Test")
	for name, size := range files {
		write(t, root, name, size)
	}
	git(t, root, "add", "-A", "-f")
	git(t, root, "commit", "-qm", "base")
	return root, git(t, root, "rev-parse", "HEAD")
}

// bySize is a checkout near the bytes bound.
var bySize = &Usage{Bytes: 1, MaxBytes: 1}

func paths(entries []Entry) []string {
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Path)
	}
	return names
}

func TestComputeNamesTheChildThatHoldsMostOfADirectory(t *testing.T) {
	root, base := repository(t, map[string]int{
		".agents/records/a.json": 9000,
		".agents/records/b.json": 9000,
		".agents/plans/p.md":     100,
		"src/main.go":            3000,
		"src/util/u.go":          2000,
		"dump.sql":               4000,
		"README.md":              10,
	})
	breakdown, err := Compute(context.Background(), root, base, bySize)
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.Base != base || breakdown.Files != 7 || breakdown.Bytes != 27110 || breakdown.ExcludedFiles != 0 || breakdown.RankedBy != RankedByBytes {
		t.Fatalf("totals = %+v", breakdown)
	}
	want := []Entry{
		{Path: ".agents/records", Directory: true, Files: 2, Bytes: 18000, Pattern: ".agents/records/**"},
		{Path: "src", Directory: true, Files: 2, Bytes: 5000, Pattern: "src/**"},
		{Path: "dump.sql", Files: 1, Bytes: 4000, Pattern: "/dump.sql"},
		{Path: "README.md", Files: 1, Bytes: 10, Pattern: "/README.md"},
	}
	if !reflect.DeepEqual(breakdown.Largest, want) {
		t.Fatalf("largest = %+v", breakdown.Largest)
	}
}

func TestComputeLeavesOutWhatTheRepositoryAlreadyExcludes(t *testing.T) {
	root, base := repository(t, map[string]int{
		"vendor/lib.js":         5000,
		"web/dist/app.js":       4000,
		"web/src/app.ts":        100,
		"fixtures/big.json":     3000,
		"fixtures/keep.json":    50,
		"src/main.go":           200,
		"untracked/data.bin":    2500,
		"symlinked/ignored.txt": 1500,
	})
	writeText(t, root, ".bifrostignore", "vendor/\n")
	writeText(t, root, "web/.bifrostignore", "dist/\n")
	// Untracked, but part of the change a check of the working tree sends.
	writeText(t, root, "untracked/.bifrostignore", "*.bin\n")
	writeText(t, root, ".slopcop/config.toml", "service_url = \"https://example.com\"\n\n[check]\nexclude = [\"fixtures/*.json\", \"!fixtures/keep.json\"]\n")
	if err := os.Symlink("../.bifrostignore", filepath.Join(root, "symlinked", ".bifrostignore")); err != nil {
		t.Fatal(err)
	}
	breakdown, err := Compute(context.Background(), root, base, bySize)
	if err != nil {
		t.Fatal(err)
	}
	// The ignore files written after the base commit are not in its tree, so
	// only the base's files count.
	if breakdown.ExcludedFiles != 4 || breakdown.ExcludedBytes != 5000+4000+3000+2500 {
		t.Fatalf("excluded = %d files, %d bytes", breakdown.ExcludedFiles, breakdown.ExcludedBytes)
	}
	if got := paths(breakdown.Largest); !reflect.DeepEqual(got, []string{"symlinked/ignored.txt", "src/main.go", "web/src", "fixtures/keep.json"}) {
		t.Fatalf("largest = %v", got)
	}
	if !breakdown.HasCheck || !reflect.DeepEqual(breakdown.Exclude, []string{"fixtures/*.json", "!fixtures/keep.json"}) || breakdown.ConfigError != "" {
		t.Fatalf("config = %+v", breakdown)
	}
}

func TestComputeSaysWhenTheConfigDoesNotParse(t *testing.T) {
	root, base := repository(t, map[string]int{"a/b.txt": 10})
	writeText(t, root, ".slopcop/config.toml", "[check\nexclude = [\n")
	breakdown, err := Compute(context.Background(), root, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(breakdown.ConfigError, ".slopcop/config.toml does not parse") || breakdown.HasCheck || breakdown.Files != 1 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

func TestComputeRanksByTheBoundTheRepositoryIsCloserTo(t *testing.T) {
	files := map[string]int{"big/blob.bin": 10000}
	for _, name := range []string{"a", "b", "c", "d", "e", "f"} {
		files["many/"+name+".txt"] = 1
	}
	root, base := repository(t, files)
	breakdown, err := Compute(context.Background(), root, base, &Usage{Files: 45000, MaxFiles: 50000, Bytes: 10 << 20, MaxBytes: 512 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.RankedBy != RankedByFiles || breakdown.Largest[0].Path != "many" || breakdown.Largest[0].Files != 6 {
		t.Fatalf("by files = %+v", breakdown)
	}
	breakdown, err = Compute(context.Background(), root, base, &Usage{Files: 100, MaxFiles: 50000, Bytes: 500 << 20, MaxBytes: 512 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.RankedBy != RankedByBytes || breakdown.Largest[0].Path != "big/blob.bin" {
		t.Fatalf("by bytes = %+v", breakdown)
	}
}

func TestComputeListsAtMostFive(t *testing.T) {
	files := map[string]int{}
	for i, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		files[name+"/x"] = 10 * (i + 1)
		files[name+"/y"] = 10 * (i + 1)
	}
	root, base := repository(t, files)
	breakdown, err := Compute(context.Background(), root, base, bySize)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(breakdown.Largest); !reflect.DeepEqual(got, []string{"g", "f", "e", "d", "c"}) {
		t.Fatalf("largest = %v", got)
	}
}

// TestComputeWithoutUsageRanksByTheLocalTree covers a repository_too_large
// refusal, which carries no counts: the local tree decides.
func TestComputeWithoutUsageRanksByTheLocalTree(t *testing.T) {
	root, base := repository(t, map[string]int{"a/b": 1, "a/c": 1, "big": 100})
	breakdown, err := Compute(context.Background(), root, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if breakdown.RankedBy != RankedByFiles || breakdown.Largest[0].Path != "a" {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

func TestComputeFailsOnAnUnknownBase(t *testing.T) {
	root, _ := repository(t, map[string]int{"a": 1})
	if _, err := Compute(context.Background(), root, strings.Repeat("0", 40), nil); err == nil {
		t.Fatal("want an error")
	}
}

func TestPatternEscapesGitignoreSyntax(t *testing.T) {
	for _, tc := range []struct {
		name      string
		directory bool
		want      string
	}{
		{".agents/records", true, ".agents/records/**"},
		{"dump.sql", false, "/dump.sql"},
		{"#notes", true, `\#notes/**`},
		{"!bang", false, `/\!bang`},
		{"a[1]*?", false, `/a\[1]\*\?`},
		{"trailing ", false, `/trailing\ `},
	} {
		if got := Pattern(tc.name, tc.directory); got != tc.want {
			t.Errorf("Pattern(%q) = %q want %q", tc.name, got, tc.want)
		}
	}
}
