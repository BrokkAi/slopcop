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

// Only init, login and a check write the skill: running another command
// leaves the filesystem alone.
func TestOnlyInitLoginAndCheckInstallTheSkill(t *testing.T) {
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

	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	dir := filepath.Join(agents, "skills", "slopcop")
	if !strings.Contains(stdout, dir) {
		t.Errorf("stdout = %q, want it to name %s", stdout, dir)
	}
	if got := readSkillFile(t, filepath.Join(dir, "SKILL.md")); got != readEmbeddedSkill(t, "SKILL.md") {
		t.Error("the installed SKILL.md does not match the bundled one")
	}
	if _, err := os.Stat(filepath.Join(dir, "agents", "openai.yaml")); err != nil {
		t.Errorf("harness metadata was not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Error("a harness that is not installed here got a skills directory")
	}
}

func TestInitInstallsForEveryHarnessFound(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".agents"))
	makeHarness(t, filepath.Join(home, ".claude"))
	makeHarness(t, filepath.Join(home, ".codex"))

	want := []string{
		filepath.Join(home, ".agents", "skills", "slopcop"),
		filepath.Join(home, ".claude", "skills", "slopcop"),
		filepath.Join(home, ".codex", "skills", "slopcop"),
	}
	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	for _, dir := range want {
		if !strings.Contains(stdout, dir) {
			t.Errorf("stdout = %q, want it to name %s", stdout, dir)
		}
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			t.Errorf("no skill at %s: %v", dir, err)
		}
	}

	code, stdout, stderr = runCLI(t, "init")
	if code != 0 {
		t.Fatalf("second exit = %d, stderr = %s", code, stderr)
	}
	if strings.Contains(stdout, "installed") {
		t.Errorf("the second run installed something again: %q", stdout)
	}
	if !strings.Contains(stdout, "already") {
		t.Errorf("stdout = %q, want it to report the skill as already there", stdout)
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

// Signing in installs the bundled skill, so a user who only signs in and
// leaves the rest to an agent still has it.
func TestLoginInstallsTheBundledSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))

	code, stdout, stderr := runCLI(t,
		"login", "--service", "http://127.0.0.1:9999", "--token", "secret",
	)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	if !strings.Contains(stdout, "installed the slopcop skill at "+dir) {
		t.Errorf("stdout = %q, want it to name %s", stdout, dir)
	}
	if got := readSkillFile(t, filepath.Join(dir, "SKILL.md")); got != readEmbeddedSkill(t, "SKILL.md") {
		t.Error("the installed SKILL.md does not match the bundled one")
	}
}

// init leaves a skill that is already there as it is.
func TestInitLeavesAnExistingSkillAlone(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".agents"))
	path := filepath.Join(home, ".agents", "skills", "slopcop", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "already") {
		t.Errorf("stdout = %q", stdout)
	}
	if got := readSkillFile(t, path); got != "mine\n" {
		t.Errorf("SKILL.md = %q, want the local file untouched", got)
	}
}

func TestInitWithoutAHarnessInstallsNothing(t *testing.T) {
	home := isolateAgentHome(t)

	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to install") {
		t.Errorf("stdout = %q", stdout)
	}
	for _, dir := range []string{".agents", ".claude", ".codex"} {
		if _, err := os.Stat(filepath.Join(home, dir)); err == nil {
			t.Errorf("init created %s for a harness that is not installed", dir)
		}
	}
}

// --no-skill leaves the skill alone, whether it is installed or not.
func TestInitNoSkillWritesNothing(t *testing.T) {
	home := isolateAgentHome(t)
	agents := filepath.Join(home, ".agents")
	makeHarness(t, agents)

	code, stdout, stderr := runCLI(t, "init", "--no-skill")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "--no-skill") {
		t.Errorf("stdout = %q, want it to say the skill step was skipped", stdout)
	}
	if _, err := os.Stat(filepath.Join(agents, "skills")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("--no-skill wrote %s", filepath.Join(agents, "skills"))
	}

	// An installed skill is left as it is, and stays untouched.
	if code, _, stderr := runCLI(t, "init"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	path := filepath.Join(agents, "skills", "slopcop", "SKILL.md")
	if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, "init", "--no-skill"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if got := readSkillFile(t, path); got != "mine\n" {
		t.Errorf("SKILL.md = %q, want --no-skill to leave it alone", got)
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
