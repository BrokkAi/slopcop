package cli

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/config"
	"github.com/BrokkAi/slopcop/internal/report"
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
	// A missing skill is installed; one that differs, an older or an edited
	// one alike, is replaced, and the message says so.
	for dir, said := range map[string]string{
		filepath.Join(home, ".agents", "skills", "slopcop"): "installed the slopcop skill at ",
		filepath.Join(home, ".claude", "skills", "slopcop"): "updated the slopcop skill at ",
	} {
		if !strings.Contains(stderr, said+dir) {
			t.Errorf("stderr = %q, want %q", stderr, said+dir)
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

// With no harness configuration directory yet, init still puts the skill in
// the vendor-neutral ~/.agents/skills, which every agent that reads skills
// checks. It must not create the directories of harnesses that are absent.
func TestInitWithoutAHarnessInstallsTheNeutralSkill(t *testing.T) {
	home := isolateAgentHome(t)

	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	dir := filepath.Join(home, ".agents", "skills", "slopcop")
	if !strings.Contains(stdout, "installed the slopcop skill at "+dir) {
		t.Errorf("stdout = %q, want it to name %s", stdout, dir)
	}
	if got := readSkillFile(t, filepath.Join(dir, "SKILL.md")); got != readEmbeddedSkill(t, "SKILL.md") {
		t.Error("the installed SKILL.md does not match the bundled one")
	}
	for _, name := range []string{".claude", ".codex"} {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			t.Errorf("init created %s for a harness that is not installed", name)
		}
	}
}

// A harness that is present is the only place init writes; it does not also
// create the vendor-neutral directory beside it.
func TestInitWithAHarnessSkipsTheNeutralSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))

	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	if !strings.Contains(stdout, "installed the slopcop skill at "+dir) {
		t.Errorf("stdout = %q, want it to name %s", stdout, dir)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents")); err == nil {
		t.Errorf("init created %s for a harness that is not installed", filepath.Join(home, ".agents"))
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

// writeStampedSkill puts a skill in dir as the given version wrote it: each
// file with a stamp that records it.
func writeStampedSkill(t *testing.T, dir, version string, contents map[string]string) {
	t.Helper()
	var files []skillFile
	for name, content := range contents {
		files = append(files, skillFile{relative: filepath.FromSlash(name), data: []byte(content)})
	}
	if err := writeSkill(dir, files, version); err != nil {
		t.Fatal(err)
	}
}

// failingService answers every check with a refusal, so a check run gets as
// far as the skill sync and a failure report.
func failingService(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code": "repo_renamed", "message": "renamed", "remedy": "fix the remote", "retryable": false}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func skillUpdates(t *testing.T, stdout string) []report.SkillUpdate {
	t.Helper()
	var document struct {
		SkillUpdated []report.SkillUpdate `json:"skillUpdated"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not a json report: %v\n%s", err, stdout)
	}
	return document.SkillUpdated
}

// An update names the version it replaces, the one it installs and the files
// that changed, on stderr and in the json report, and removes the files the
// bundle no longer has. A skill as its version wrote it needs no copy.
func TestCheckNamesTheSkillVersionsAndFilesItChanges(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeStampedSkill(t, dir, "v0.0.9", map[string]string{
		"SKILL.md":       "an older skill\n",
		"notes/stale.md": "a file the bundle dropped\n",
	})
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := failingService(t)

	_, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	wantChanged := []string{"SKILL.md", "agents/openai.yaml", "notes/stale.md"}
	notice := "updated the slopcop skill at " + dir + " from v0.0.9 to test (changed: " + strings.Join(wantChanged, ", ") + ")"
	if !strings.Contains(stderr, notice) {
		t.Errorf("stderr = %q, want %q", stderr, notice)
	}
	if strings.Contains(stderr, "local edits") {
		t.Errorf("stderr = %q, want no copy of a skill without local edits", stderr)
	}
	want := []report.SkillUpdate{{Dir: dir, From: "v0.0.9", To: "test", Changed: wantChanged}}
	if got := skillUpdates(t, stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("skillUpdated = %+v, want %+v", got, want)
	}
	if got := readSkillFile(t, filepath.Join(dir, "SKILL.md")); got != readEmbeddedSkill(t, "SKILL.md") {
		t.Error("SKILL.md is not the bundled one")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the dropped file's folder is still there: %v", err)
	}
	if stamp, ok := readSkillStamp(dir); !ok || stamp.Version != "test" {
		t.Errorf("stamp = %+v, %v, want version test", stamp, ok)
	}

	_, stdout, stderr = runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	if strings.Contains(stderr, "skill") || len(skillUpdates(t, stdout)) != 0 {
		t.Errorf("a current skill was reported: stderr = %q, stdout = %s", stderr, stdout)
	}
}

// A skill with local edits is copied outside every skills directory before
// it is replaced, and the notice and the report say where.
func TestCheckKeepsACopyOfASkillWithLocalEdits(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeStampedSkill(t, dir, "v0.0.9", map[string]string{"SKILL.md": "an older skill\n"})
	writeFile(t, dir, "SKILL.md", "an older skill, with my notes\n")
	writeFile(t, dir, "mine.md", "my own file\n")
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	server := failingService(t)

	_, stdout, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL, "--format", "json")
	updates := skillUpdates(t, stdout)
	if len(updates) != 1 || updates[0].From != "v0.0.9" || updates[0].Backup == "" {
		t.Fatalf("skillUpdated = %+v, want one update from v0.0.9 with a backup", updates)
	}
	backup := updates[0].Backup
	if !strings.Contains(stderr, "local edits; a copy is at "+backup) {
		t.Errorf("stderr = %q, want it to name the copy at %s", stderr, backup)
	}
	backups, err := config.SkillBackupDir()
	requireUnder(t, backups, backup, err)
	if got := filepath.Base(backup); !strings.HasPrefix(got, "claude-") {
		t.Errorf("backup %s is not named for its harness", backup)
	}
	for name, want := range map[string]string{
		"SKILL.md": "an older skill, with my notes\n",
		"mine.md":  "my own file\n",
	} {
		if got := readSkillFile(t, filepath.Join(backup, name)); got != want {
			t.Errorf("the copy's %s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "mine.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file the bundle lacks stayed in the skill: %v", err)
	}
}

// A skill a release before the stamp installed is known by its files, so it
// is named and needs no copy; one that matches no release is unknown and
// edited.
func TestSkillOriginKnowsReleasesBeforeTheStamp(t *testing.T) {
	v009 := map[string]string{
		"SKILL.md":           "506acaa83b67693f56aff616f32d0224091d5602462e1b330166ade1840bd7f8",
		"agents/openai.yaml": "2b3ebe4e9299ac6c1eed486258fb9789338fb904622773a462ec3f227c180765",
	}
	if from, edited := skillOrigin(v009, skillStamp{}, false); from != "v0.0.9" || edited {
		t.Errorf("skillOrigin(v0.0.9) = %q, %v, want v0.0.9 without edits", from, edited)
	}
	v009["SKILL.md"] = fileDigest([]byte("edited\n"))
	if from, edited := skillOrigin(v009, skillStamp{}, false); from != skillVersionUnknown || !edited {
		t.Errorf("skillOrigin(edited) = %q, %v, want unknown with edits", from, edited)
	}
}

// A skill that matches the bundle under another version's stamp only has its
// stamp brought up to date, without a notice.
func TestMatchingSkillOnlyGetsANewStamp(t *testing.T) {
	isolateAgentHome(t)
	files, err := bundledSkillFiles()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "skills", "slopcop")
	if err := writeSkill(dir, files, "v0.0.9"); err != nil {
		t.Fatal(err)
	}
	if _, wrote, err := syncSkillAt(dir, files, "v0.0.10"); err != nil || wrote {
		t.Fatalf("syncSkillAt = %v, %v, want nothing written", wrote, err)
	}
	if stamp, _ := readSkillStamp(dir); stamp.Version != "v0.0.10" {
		t.Errorf("stamp version = %q, want v0.0.10", stamp.Version)
	}
}

// The notice for a release links its notes.
func TestSkillNoticeLinksReleaseNotes(t *testing.T) {
	got := skillNotice(report.SkillUpdate{Dir: "d", From: skillVersionUnknown, To: "v0.0.11", Changed: []string{"SKILL.md"}})
	for _, want := range []string{"from an unrecognised version to v0.0.11", releaseNotesURL + "v0.0.11"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice = %q, want %q", got, want)
		}
	}
}
