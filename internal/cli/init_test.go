package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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

// init is the only thing that writes the skill: running another command leaves
// the filesystem alone.
func TestOnlyInitInstallsTheSkill(t *testing.T) {
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

// A skill with local changes stays as it is, so local edits survive.
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
	if !strings.Contains(stdout, "local changes, or a newer slopcop wrote it), so it was left alone") {
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

// An untouched skill from an earlier release is brought up to date, so an
// agent learns what this release can do; a file the earlier release did not
// ship is added.
func TestInitUpdatesAnUntouchedEarlierSkill(t *testing.T) {
	earlier := earlierSkill(t)

	cases := []struct {
		name string
		// metadata is the installed agents/openai.yaml, or nothing.
		metadata *string
		want     string
	}{
		{name: "every file installed", metadata: ptr(readEmbeddedSkill(t, "agents/openai.yaml")), want: "updated"},
		{name: "a file the earlier release lacked", want: "updated"},
		{name: "edited metadata", metadata: ptr("mine\n"), want: "local changes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateAgentHome(t)
			makeHarness(t, filepath.Join(home, ".claude"))
			dir := filepath.Join(home, ".claude", "skills", "slopcop")
			writeFile(t, dir, "SKILL.md", earlier)
			if tc.metadata != nil {
				writeFile(t, dir, "agents/openai.yaml", *tc.metadata)
			}

			code, stdout, stderr := runCLI(t, "init")
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, stderr)
			}
			if !strings.Contains(stdout, tc.want) || !strings.Contains(stdout, dir) {
				t.Errorf("stdout = %q, want %q about %s", stdout, tc.want, dir)
			}
			got := readSkillFile(t, filepath.Join(dir, "SKILL.md"))
			if tc.want == "updated" {
				if got != readEmbeddedSkill(t, "SKILL.md") {
					t.Error("the earlier SKILL.md was not replaced with this release's")
				}
				if readSkillFile(t, filepath.Join(dir, "agents", "openai.yaml")) != readEmbeddedSkill(t, "agents/openai.yaml") {
					t.Error("the harness metadata is not this release's")
				}
				code, stdout, _ = runCLI(t, "init")
				if code != 0 || !strings.Contains(stdout, "already") {
					t.Errorf("a second init = %d, %q; want the skill already up to date", code, stdout)
				}
				return
			}
			if got != earlier {
				t.Errorf("a skill with local changes was rewritten: %q", got)
			}
		})
	}
}

func ptr(value string) *string { return &value }

// pinnedSkillDigests are the SHA-256 of this release's copy of every bundled
// skill file, by its slash-separated path inside the skill folder. Changing,
// adding or removing a file fails TestSkillChangesUpdateThePin until its pin
// moves, which is the moment to add the replaced digest to skills.Previous if
// that copy shipped, so untouched installs of it still update.
var pinnedSkillDigests = map[string]string{
	"SKILL.md":           "67f6409a28fa86db3f333e8f59f2d11f3f8f641969f561611650220e671f34a0",
	"agents/openai.yaml": "2b3ebe4e9299ac6c1eed486258fb9789338fb904622773a462ec3f227c180765",
}

func TestSkillChangesUpdateThePin(t *testing.T) {
	const instructions = "if the pinned copy shipped in a release, add it to skills.Previous; then move the pin"
	files, err := bundledSkillFiles()
	if err != nil {
		t.Fatal(err)
	}
	bundled := make(map[string]bool, len(files))
	for _, file := range files {
		name := filepath.ToSlash(file.relative)
		bundled[name] = true
		sum := sha256.Sum256(file.data)
		got := hex.EncodeToString(sum[:])
		if pinned, ok := pinnedSkillDigests[name]; !ok {
			t.Errorf("%s is %s and has no pin: add it to pinnedSkillDigests", name, got)
		} else if got != pinned {
			t.Errorf("%s is now %s, pinned %s: %s", name, got, pinned, instructions)
		}
	}
	for name := range pinnedSkillDigests {
		if !bundled[name] {
			t.Errorf("%s is pinned but no longer bundled: %s", name, instructions)
		}
	}
}

// The digests of earlier releases are SHA-256 hex, of files the skill still
// ships, and never this release's own copy.
func TestPreviousSkillDigestsAreWellFormed(t *testing.T) {
	for name, digests := range skills.Previous {
		data, err := skills.Files.ReadFile("slopcop/" + name)
		if err != nil {
			t.Errorf("skills.Previous names %s, which the skill does not ship: %v", name, err)
			continue
		}
		sum := sha256.Sum256(data)
		current := hex.EncodeToString(sum[:])
		for _, digest := range digests {
			if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
				t.Errorf("%s: %q is not a lowercase SHA-256 hex digest", name, digest)
			}
			if digest == current {
				t.Errorf("%s: %s is this release's copy, not an earlier one", name, digest)
			}
		}
	}
}

// The skill init installs teaches an agent to report a false positive and
// carries the current error codes.
func TestBundledSkillTeachesFalsePositiveReports(t *testing.T) {
	content := readEmbeddedSkill(t, "SKILL.md")
	for _, want := range []string{
		"## Report a false positive",
		"slopcop report ",
		"--reason",
		"`summary.dismissedFindings`",
		"`finding_not_found`",
		"`invalid_report`",
		"When unsure, fix the code or ask the human instead.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("the bundled skill lacks %q", want)
		}
	}
	for _, gone := range []string{"indexing_in_progress", "Analyze the repository on SlopCop first"} {
		if strings.Contains(content, gone) {
			t.Errorf("the bundled skill still says %q", gone)
		}
	}
}

// A skill init cannot rewrite (a read-only directory, say) is reported and
// left as it was; the other harnesses still get theirs, and init succeeds.
func TestInitGoesOnWhenAnEarlierSkillCannotBeUpdated(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs Unix permissions that bind this user")
	}
	earlier := earlierSkill(t)

	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".agents"))
	makeHarness(t, filepath.Join(home, ".claude"))
	locked := filepath.Join(home, ".agents", "skills", "slopcop")
	writeFile(t, locked, "SKILL.md", earlier)
	writeFile(t, locked, "agents/openai.yaml", readEmbeddedSkill(t, "agents/openai.yaml"))
	for path, mode := range map[string]os.FileMode{
		filepath.Join(locked, "SKILL.md"):              0o444,
		filepath.Join(locked, "agents", "openai.yaml"): 0o444,
		filepath.Join(locked, "agents"):                0o555,
		locked:                                         0o555,
	} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o755)
		_ = os.Chmod(filepath.Join(locked, "agents"), 0o755)
	})

	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "could not update the slopcop skill at "+locked) ||
		!strings.Contains(stderr, "run slopcop init again once it is writable") {
		t.Errorf("stderr = %q, want the locked skill named", stderr)
	}
	if readSkillFile(t, filepath.Join(locked, "SKILL.md")) != earlier {
		t.Error("the locked skill changed")
	}
	claude := filepath.Join(home, ".claude", "skills", "slopcop")
	if !strings.Contains(stdout, "installed the slopcop skill at "+claude) {
		t.Errorf("stdout = %q, want the other harness installed", stdout)
	}
}

// An unreadable skill in one harness is a failure, since the harness cannot
// read it either; the other harnesses are still installed and reported.
func TestInitReportsEveryHarnessWhenOneCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs Unix permissions that bind this user")
	}
	home := isolateAgentHome(t)
	t.Setenv("CODEX_HOME", "")
	for _, harness := range []string{".agents", ".claude", ".codex"} {
		makeHarness(t, filepath.Join(home, harness))
	}
	unreadable := filepath.Join(home, ".claude", "skills", "slopcop")
	writeFile(t, unreadable, "SKILL.md", "mine\n")
	if err := os.Chmod(filepath.Join(unreadable, "SKILL.md"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(unreadable, "SKILL.md"), 0o644) })

	code, stdout, stderr := runCLI(t, "init")
	if code != initFailed {
		t.Errorf("exit = %d, want %d", code, initFailed)
	}
	if !strings.Contains(stderr, "could not read the slopcop skill at "+unreadable) ||
		strings.Contains(stderr, "partly updated") {
		t.Errorf("stderr = %q, want the unreadable skill named as a failure", stderr)
	}
	for _, harness := range []string{".agents", ".codex"} {
		dir := filepath.Join(home, harness, "skills", "slopcop")
		if !strings.Contains(stdout, "installed the slopcop skill at "+dir) {
			t.Errorf("stdout = %q, want %s installed", stdout, dir)
		}
	}
}

// A skill file that is a symlink (a dotfile manager's, say) is written through,
// so the link stays a link.
func TestInitWritesThroughASymlinkedSkill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	earlier := earlierSkill(t)

	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dotfiles := t.TempDir()
	writeFile(t, dotfiles, "SKILL.md", earlier)
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeFile(t, dir, "agents/openai.yaml", readEmbeddedSkill(t, "agents/openai.yaml"))
	link := filepath.Join(dir, "SKILL.md")
	if err := os.Symlink(filepath.Join(dotfiles, "SKILL.md"), link); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(dotfiles, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	if code, stdout, stderr := runCLI(t, "init"); code != 0 || !strings.Contains(stdout, "updated") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("SKILL.md is no longer a symlink: %v", err)
	}
	if readSkillFile(t, filepath.Join(dotfiles, "SKILL.md")) != readEmbeddedSkill(t, "SKILL.md") {
		t.Error("the link's target was not updated")
	}
	after, err := os.Stat(filepath.Join(dotfiles, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the link's target was rewritten in place, not replaced atomically")
	}
}

// earlierSkill makes an earlier release's SKILL.md for the test, which init
// brings up to date.
func earlierSkill(t *testing.T) string {
	t.Helper()
	const earlier = "---\nname: slopcop\ndescription: an earlier release\n---\n"
	sum := sha256.Sum256([]byte(earlier))
	previous := skills.Previous["SKILL.md"]
	skills.Previous["SKILL.md"] = append(append([]string{}, previous...), hex.EncodeToString(sum[:]))
	t.Cleanup(func() { skills.Previous["SKILL.md"] = previous })
	return earlier
}

// A symlinked skill whose target directory is read-only cannot be updated:
// that is reported, and the earlier skill stays in use.
func TestInitReportsASymlinkedSkillItCannotUpdate(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs Unix symlinks and permissions that bind this user")
	}
	earlier := earlierSkill(t)
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	store := t.TempDir()
	writeFile(t, store, "SKILL.md", earlier)
	if err := os.Chmod(store, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeFile(t, dir, "agents/openai.yaml", readEmbeddedSkill(t, "agents/openai.yaml"))
	link := filepath.Join(dir, "SKILL.md")
	if err := os.Symlink(filepath.Join(store, "SKILL.md"), link); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, "init")
	if code != 0 || !strings.Contains(stderr, "could not update the slopcop skill at "+dir) {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("SKILL.md is no longer a symlink: %v", err)
	}
	if readSkillFile(t, link) != earlier {
		t.Error("the read-only target changed")
	}
}

// A read-only skill file in a writable directory is left as it is, and that
// is reported; the earlier skill stays in use.
func TestInitLeavesAReadOnlySkillFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs Unix permissions")
	}
	earlier := earlierSkill(t)
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeFile(t, dir, "SKILL.md", earlier)
	writeFile(t, dir, "agents/openai.yaml", readEmbeddedSkill(t, "agents/openai.yaml"))
	path := filepath.Join(dir, "SKILL.md")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, "init")
	if code != 0 || !strings.Contains(stderr, "could not update the slopcop skill at "+dir) ||
		!strings.Contains(stderr, "read-only") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if readSkillFile(t, path) != earlier {
		t.Error("the read-only SKILL.md was replaced")
	}
}

// A fresh install that fails part way exits 2, even though it wrote SKILL.md
// before failing: there was no skill before, so the harness has no working
// one.
func TestInitFailsAFreshInstallThatFailsPartWay(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	// A file where the agents directory goes fails the second file only.
	writeFile(t, dir, "agents", "not a directory\n")

	code, _, stderr := runCLI(t, "init")
	if code != initFailed || !strings.Contains(stderr, "could not install the slopcop skill at "+dir) {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md was not written before the failure, so this test proves nothing: %v", err)
	}
}

// A fresh install through a dangling symlink (a dotfile manager's link set up
// before its target exists) creates the target where the link points,
// relative to the link's directory, and the link stays a link.
func TestInitCreatesTheTargetOfADanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dotfiles := filepath.Join(home, "dotfiles")
	makeHarness(t, dotfiles)
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	makeHarness(t, dir)
	link := filepath.Join(dir, "SKILL.md")
	relative, err := filepath.Rel(dir, filepath.Join(dotfiles, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, link); err != nil {
		t.Fatal(err)
	}

	if code, stdout, stderr := runCLI(t, "init"); code != 0 || !strings.Contains(stdout, "installed the slopcop skill at "+dir) {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("SKILL.md is no longer a symlink: %v", err)
	}
	if readSkillFile(t, filepath.Join(dotfiles, "SKILL.md")) != readEmbeddedSkill(t, "SKILL.md") {
		t.Error("the link's target was not created with the bundled skill")
	}
}

// A dangling symlink whose target directory does not exist cannot be written
// through: a fresh install says why and exits 2.
func TestInitFailsADanglingSymlinkWithoutItsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	makeHarness(t, dir)
	missing := filepath.Join(home, "dotfiles", "slopcop")
	if err := os.Symlink(filepath.Join(missing, "SKILL.md"), filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, "init")
	if code != initFailed || !strings.Contains(stderr, "could not install the slopcop skill at "+dir) ||
		!strings.Contains(stderr, "whose directory "+missing+" does not exist") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("init created %s: %v", missing, err)
	}
}

// An update keeps a replaced file's permission bits, and leaves a file that
// already matches as it is.
func TestInitKeepsAReplacedFilesModeAndLeavesMatchingFiles(t *testing.T) {
	earlier := earlierSkill(t)
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeFile(t, dir, "SKILL.md", earlier)
	writeFile(t, dir, "agents/openai.yaml", readEmbeddedSkill(t, "agents/openai.yaml"))
	skill := filepath.Join(dir, "SKILL.md")
	metadata := filepath.Join(dir, "agents", "openai.yaml")
	if err := os.Chmod(skill, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(metadata)
	if err != nil {
		t.Fatal(err)
	}

	if code, stdout, stderr := runCLI(t, "init"); code != 0 || !strings.Contains(stdout, "updated") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if readSkillFile(t, skill) != readEmbeddedSkill(t, "SKILL.md") {
		t.Fatal("the earlier SKILL.md was not replaced")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(skill); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("SKILL.md mode = %v, %v; want 0600 kept", info.Mode().Perm(), err)
		}
	}
	if after, err := os.Stat(metadata); err != nil || !os.SameFile(before, after) {
		t.Errorf("the matching agents/openai.yaml was rewritten: %v", err)
	}
}

// A file besides SKILL.md that cannot be read leaves the skill alone, as local
// changes do: SKILL.md is readable, so a harness still uses the skill, and
// init warns and exits 0.
func TestInitLeavesASkillWithAnUnreadableFileBesidesSkillMD(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs Unix permissions that bind this user")
	}
	earlier := earlierSkill(t)
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeFile(t, dir, "SKILL.md", earlier)
	writeFile(t, dir, "agents/openai.yaml", readEmbeddedSkill(t, "agents/openai.yaml"))
	metadata := filepath.Join(dir, "agents", "openai.yaml")
	if err := os.Chmod(metadata, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(metadata, 0o644) })

	code, _, stderr := runCLI(t, "init")
	if code != 0 || !strings.Contains(stderr, "could not read all of the slopcop skill at "+dir) ||
		!strings.Contains(stderr, "left alone") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if readSkillFile(t, filepath.Join(dir, "SKILL.md")) != earlier {
		t.Error("the skill was changed, not left alone")
	}
}

// An update that fails after replacing SKILL.md says the skill may be partly
// updated, not that the earlier one stays in use, and exits 0.
func TestInitSaysAFailedUpdateMayBePartlyDone(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("needs Unix permissions that bind this user")
	}
	earlier := earlierSkill(t)
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	dir := filepath.Join(home, ".claude", "skills", "slopcop")
	writeFile(t, dir, "SKILL.md", earlier)
	agents := filepath.Join(dir, "agents")
	if err := os.Mkdir(agents, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(agents, 0o755) })

	code, _, stderr := runCLI(t, "init")
	if code != 0 || !strings.Contains(stderr, "could not update the slopcop skill at "+dir) ||
		!strings.Contains(stderr, "it may be partly updated, so run slopcop init again once it is writable") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if readSkillFile(t, filepath.Join(dir, "SKILL.md")) != readEmbeddedSkill(t, "SKILL.md") {
		t.Fatal("SKILL.md was not replaced before the failure, so this test proves nothing")
	}
}
