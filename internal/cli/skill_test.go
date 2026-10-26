package cli

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/skills"
)

// isolateAgentHome points every home directory the CLI resolves at a fresh
// directory, so a test never installs a skill outside its own tree.
func isolateAgentHome(t *testing.T) string {
	t.Helper()
	isolateUserConfig(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return home
}

// makeHarness creates the configuration directory that means a harness is
// installed on this machine.
func makeHarness(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func readSkillFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readEmbeddedSkill(t *testing.T, name string) string {
	t.Helper()
	data, err := skills.Files.ReadFile("slopcop/" + name)
	if err != nil {
		t.Fatalf("read the bundled %s: %v", name, err)
	}
	return string(data)
}

// Only a check writes the skill: running another command leaves the
// filesystem alone.
func TestOnlyACheckInstallsTheSkill(t *testing.T) {
	home := isolateAgentHome(t)
	agents := filepath.Join(home, ".agents")
	makeHarness(t, agents)

	code, _, stderr := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(agents, "skills")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a plain run wrote %s, want nothing", filepath.Join(agents, "skills"))
	}
}

// Every check installs the bundled skill where it is missing and rewrites
// one that differs, so an agent follows the version that runs. A check that
// finds the skill current writes nothing and says nothing about it.
func TestEveryCheckInstallsTheBundledSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".agents"))
	makeHarness(t, filepath.Join(home, ".claude"))
	stale := filepath.Join(home, ".claude", "skills", "slopcop", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("an older skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code": "repo_renamed", "message": "renamed", "remedy": "fix the remote", "retryable": false}`))
	}))
	defer server.Close()

	_, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	want := readEmbeddedSkill(t, "SKILL.md")
	for _, dir := range []string{
		filepath.Join(home, ".agents", "skills", "slopcop"),
		filepath.Join(home, ".claude", "skills", "slopcop"),
	} {
		if !strings.Contains(stderr, "installed the slopcop skill at "+dir) {
			t.Errorf("stderr = %q, want it to name %s", stderr, dir)
		}
		if got := readSkillFile(t, filepath.Join(dir, "SKILL.md")); got != want {
			t.Errorf("%s does not hold the bundled SKILL.md", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); err == nil {
		t.Error("a harness that is not installed here got a skills directory")
	}

	_, _, stderr = runCLI(t, "check", "--root", repo, "--service", server.URL)
	if strings.Contains(stderr, "skill") {
		t.Errorf("stderr = %q, want nothing about a skill that is current", stderr)
	}
}

// go:embed ships the bytes on disk, so a CRLF checkout would install a
// different skill than the release does.
func TestBundledSkillIsLF(t *testing.T) {
	err := fs.WalkDir(skills.Files, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := skills.Files.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "\r") {
			t.Errorf("%s has CRLF line endings; .gitattributes pins LF, but an existing clone needs `git rm -r --cached . && git reset --hard` to re-checkout", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The skill has to carry the frontmatter a harness reads to find it.
func TestBundledSkillIsDiscoverable(t *testing.T) {
	content := readEmbeddedSkill(t, "SKILL.md")
	if !strings.HasPrefix(content, "---\n") {
		t.Fatalf("the bundled skill has no frontmatter:\n%s", content)
	}
	frontmatter := strings.SplitN(content, "---\n", 3)[1]
	for _, want := range []string{"name: slopcop", "description: "} {
		if !strings.Contains(frontmatter, want) {
			t.Errorf("frontmatter %q lacks %q", frontmatter, want)
		}
	}
}
