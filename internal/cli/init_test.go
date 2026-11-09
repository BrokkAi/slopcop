package cli

import (
	"bytes"
	"github.com/BrokkAi/slopcop/internal/report"
	"github.com/BrokkAi/slopcop/skills"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func isolateAgentHome(t *testing.T) string {
	t.Helper()
	isolateUserConfig(t)
	home, _ := os.UserHomeDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	return home
}
func makeHarness(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
}
func gitAt(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func testInitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitAt(t, repo, "init")
	return repo
}

func TestInitRepositoryFilesAndExclude(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	for i := 0; i < 2; i++ {
		code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo)
		if code != 0 {
			t.Fatalf("init %d: %s", code, stderr)
		}
	}
	for _, p := range []string{".agents/skills/slopcop/SKILL.md", ".claude/skills/slopcop/SKILL.md", ".claude/settings.local.json", ".codex/hooks.json"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(repo, ".claude/settings.local.json"))
	if !strings.Contains(string(data), `"command": "slopcop gate --hook claude"`) || strings.Contains(string(data), "slopcop gate --hook claude ||") {
		t.Fatalf("Claude gate: %s", data)
	}
	data, _ = os.ReadFile(filepath.Join(repo, ".codex/hooks.json"))
	if strings.Contains(string(data), "gate") {
		t.Fatalf("unexpected Codex gate: %s", data)
	}
	path := gitAt(t, repo, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	data, _ = os.ReadFile(path)
	for _, p := range []string{"/.agents/skills/slopcop/", "/.claude/skills/slopcop/", "/.claude/settings.local.json", "/.codex/hooks.json"} {
		if strings.Count(string(data), p) != 1 {
			t.Errorf("exclude %s: %s", p, data)
		}
	}
}
func TestInitTrackedTargetAndSyncExistingOnly(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	path := filepath.Join(repo, ".claude/settings.local.json")
	makeHarness(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("{}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, repo, "add", "-f", ".claude/settings.local.json")
	code, out, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo)
	if code != 0 {
		t.Fatalf("%s", stderr)
	}
	if !strings.Contains(out, "tracked target") {
		t.Fatal(out)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{}\n" {
		t.Fatal(string(data))
	}
	skill := filepath.Join(repo, ".agents/skills/slopcop/SKILL.md")
	if err := os.WriteFile(skill, []byte("---\nname: slopcop\n---\nold"), 0644); err != nil {
		t.Fatal(err)
	}
	updates, err := syncBundledSkill(repo, "test")
	if err != nil || len(updates) != 1 {
		t.Fatalf("updates %v: %v", updates, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents/skills/slopcop")); !os.IsNotExist(err) {
		t.Fatalf("user skill created: %v", err)
	}
}
func TestInitOutsideRepository(t *testing.T) {
	isolateAgentHome(t)
	code, _, stderr := runCLI(t, "init", "--root", t.TempDir())
	if code == 0 || !strings.Contains(stderr, "requires a Git repository") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestLoginDoesNotInstallSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	code, _, stderr := runCLI(t, "login", "--token", "test-token")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, p := range []string{filepath.Join(home, ".claude/skills/slopcop"), filepath.Join(home, ".agents/skills/slopcop")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("login created %s: %v", p, err)
		}
	}
}

func readEmbeddedSkill(t *testing.T, name string) string {
	t.Helper()
	data, err := skills.Files.ReadFile("slopcop/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
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

// TestInitLeavesAnExistingSkillAlone preserves a locally edited repo skill.
func TestInitLeavesAnExistingSkillAlone(t *testing.T) {
	isolateAgentHome(t)
	repo := testInitRepo(t)
	path := filepath.Join(repo, ".agents/skills/slopcop/SKILL.md")
	makeHarness(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("mine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "mine\n" {
		t.Fatalf("existing skill changed: %q", data)
	}
}

// TestInitNoSkillWritesNothing covers both absent and existing repo skills.
func TestInitNoSkillWritesNothing(t *testing.T) {
	isolateAgentHome(t)
	repo := testInitRepo(t)
	code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo, "--no-skill")
	if code != 0 {
		t.Fatal(stderr)
	}
	path := filepath.Join(repo, ".agents/skills/slopcop/SKILL.md")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("--no-skill wrote skill: %v", err)
	}
	code, _, stderr = runCLI(t, "init", "--no-reviewers", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	if err := os.WriteFile(path, []byte("mine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCLI(t, "init", "--no-reviewers", "--root", repo, "--no-skill")
	if code != 0 {
		t.Fatal(stderr)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "mine\n" {
		t.Fatalf("--no-skill changed skill: %q", data)
	}
}

// TestInitInstallsForEveryHarnessFound checks the two project destinations.
func TestInitInstallsForEveryHarnessFound(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	makeHarness(t, filepath.Join(home, ".codex"))
	repo := testInitRepo(t)
	code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, p := range []string{".agents/skills/slopcop/SKILL.md", ".claude/skills/slopcop/SKILL.md"} {
		data, err := os.ReadFile(filepath.Join(repo, p))
		if err != nil || string(data) != readEmbeddedSkill(t, "SKILL.md") {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".codex/skills/slopcop")); !os.IsNotExist(err) {
		t.Errorf("unexpected Codex-specific skill: %v", err)
	}
}

// TestInitWithoutAHarnessInstallsTheNeutralSkill covers a fresh agent home.
func TestInitWithoutAHarnessInstallsTheNeutralSkill(t *testing.T) {
	home := isolateAgentHome(t)
	repo := testInitRepo(t)
	code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, ".agents/skills/slopcop/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude/skills/slopcop")); !os.IsNotExist(err) {
		t.Errorf("unexpected Claude skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents/skills/slopcop")); !os.IsNotExist(err) {
		t.Errorf("unexpected user skill: %v", err)
	}
}

// TestInitWithAHarnessSkipsTheNeutralSkill now asserts that the neutral
// project skill remains present alongside Claude's project skill.
func TestInitWithAHarnessSkipsTheNeutralSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, p := range []string{".claude/skills/slopcop/SKILL.md", ".agents/skills/slopcop/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".agents/skills/slopcop")); !os.IsNotExist(err) {
		t.Errorf("unexpected user skill: %v", err)
	}
}

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

// TestCheckNamesTheSkillVersionsAndFilesItChanges exercises the check's sync
// entry point and its machine-readable update without involving the service.
func TestCheckNamesTheSkillVersionsAndFilesItChanges(t *testing.T) {
	isolateAgentHome(t)
	repo := testInitRepo(t)
	dir := filepath.Join(repo, ".agents/skills/slopcop")
	writeStampedSkill(t, dir, "v0.0.9", map[string]string{"SKILL.md": "an older skill\n", "notes/stale.md": "stale\n"})
	var out, errOut bytes.Buffer
	updates := syncSkillAndReport(&out, &errOut, repo, "test")
	wantChanged := []string{"SKILL.md", "agents/openai.yaml", "notes/stale.md"}
	if len(updates) != 1 || updates[0].From != "v0.0.9" || !reflect.DeepEqual(updates[0].Changed, wantChanged) {
		t.Fatalf("updates: %+v", updates)
	}
	if !strings.Contains(out.String(), "from v0.0.9 to test") || !strings.Contains(out.String(), "changed: "+strings.Join(wantChanged, ", ")) {
		t.Fatal(out.String())
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if string(data) != readEmbeddedSkill(t, "SKILL.md") {
		t.Fatal("skill was not updated")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes")); !os.IsNotExist(err) {
		t.Fatalf("stale file remains: %v", err)
	}
	out.Reset()
	if got := syncSkillAndReport(&out, &errOut, repo, "test"); len(got) != 0 || out.Len() != 0 {
		t.Fatalf("current skill reported: %+v %s", got, out.String())
	}
}

// TestCheckKeepsACopyOfASkillWithLocalEdits verifies the backup and notice.
func TestCheckKeepsACopyOfASkillWithLocalEdits(t *testing.T) {
	isolateAgentHome(t)
	repo := testInitRepo(t)
	dir := filepath.Join(repo, ".claude/skills/slopcop")
	writeStampedSkill(t, dir, "v0.0.9", map[string]string{"SKILL.md": "an older skill\n"})
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("my edited skill\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mine.md"), []byte("my own file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	updates := syncSkillAndReport(&out, &errOut, repo, "test")
	if len(updates) != 1 || updates[0].Backup == "" {
		t.Fatalf("updates: %+v", updates)
	}
	backup := updates[0].Backup
	if !strings.Contains(out.String(), "local edits; a copy is at "+backup) {
		t.Fatal(out.String())
	}
	for name, want := range map[string]string{"SKILL.md": "my edited skill\n", "mine.md": "my own file\n"} {
		data, err := os.ReadFile(filepath.Join(backup, name))
		if err != nil || string(data) != want {
			t.Errorf("backup %s: %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "mine.md")); !os.IsNotExist(err) {
		t.Errorf("unbundled file remains: %v", err)
	}
}
