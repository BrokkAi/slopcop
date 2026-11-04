package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitMigratesUserHooksAndSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	userHook := filepath.Join(home, ".claude/settings.json")
	if err := os.WriteFile(userHook, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"slopcop hook claude"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(home, ".agents/skills/slopcop/SKILL.md")
	makeHarness(t, filepath.Dir(skill))
	if err := os.WriteFile(skill, []byte("---\nname: slopcop\n---\nlocal edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCLI(t, "init", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(out, "removed the user-level hook") || !strings.Contains(out, "backed up") || !strings.Contains(out, "removed the user-level skill") {
		t.Fatal(out)
	}
	if _, err := os.Stat(skill); !os.IsNotExist(err) {
		t.Fatalf("user skill remains: %v", err)
	}
	data, _ := os.ReadFile(userHook)
	if strings.Contains(string(data), "slopcop hook") {
		t.Fatal(string(data))
	}
	code, _, stderr = runCLI(t, "init", "--root", repo, "--remove-hooks")
	if code != 0 {
		t.Fatal(stderr)
	}
	data, _ = os.ReadFile(filepath.Join(repo, ".claude/settings.local.json"))
	if strings.Contains(string(data), "slopcop") {
		t.Fatal(string(data))
	}
}

// TestInitInstallsAndRemovesHooksWithoutRemovingTheSkill covers both harness
// settings files and confirms a later skill sync does not restore hooks.
func TestInitInstallsAndRemovesHooksWithoutRemovingTheSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	code, _, stderr := runCLI(t, "init", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	claude := filepath.Join(repo, ".claude/settings.local.json")
	codex := filepath.Join(repo, ".codex/hooks.json")
	for path, want := range map[string]string{claude: "slopcop gate --hook claude", codex: "slopcop hook codex"} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), want) {
			t.Errorf("%s: %s %v", path, data, err)
		}
	}
	code, _, stderr = runCLI(t, "init", "--root", repo, "--remove-hooks")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, path := range []string{claude, codex} {
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), "slopcop") {
			t.Errorf("hook remains at %s: %s %v", path, data, err)
		}
	}
	for _, p := range []string{".agents/skills/slopcop/SKILL.md", ".claude/skills/slopcop/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
			t.Errorf("removed skill %s: %v", p, err)
		}
	}
	if _, err := syncBundledSkill(repo, "test"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{claude, codex} {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "slopcop") {
			t.Errorf("sync restored hook at %s", path)
		}
	}
}

// TestInitHookAndSkillOptOutsAreIndependent applies each flag to a repo.
func TestInitHookAndSkillOptOutsAreIndependent(t *testing.T) {
	t.Run("no hooks", func(t *testing.T) {
		home := isolateAgentHome(t)
		makeHarness(t, filepath.Join(home, ".claude"))
		repo := testInitRepo(t)
		code, _, stderr := runCLI(t, "init", "--root", repo, "--no-hooks")
		if code != 0 {
			t.Fatal(stderr)
		}
		if _, err := os.Stat(filepath.Join(repo, ".agents/skills/slopcop/SKILL.md")); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{".claude/settings.local.json", ".codex/hooks.json"} {
			if _, err := os.Stat(filepath.Join(repo, p)); !os.IsNotExist(err) {
				t.Errorf("hook installed at %s: %v", p, err)
			}
		}
	})
	t.Run("no skill", func(t *testing.T) {
		home := isolateAgentHome(t)
		makeHarness(t, filepath.Join(home, ".claude"))
		repo := testInitRepo(t)
		code, _, stderr := runCLI(t, "init", "--root", repo, "--no-skill")
		if code != 0 {
			t.Fatal(stderr)
		}
		for _, p := range []string{".agents/skills/slopcop/SKILL.md", ".claude/skills/slopcop/SKILL.md"} {
			if _, err := os.Stat(filepath.Join(repo, p)); !os.IsNotExist(err) {
				t.Errorf("skill installed at %s: %v", p, err)
			}
		}
		for _, p := range []string{".claude/settings.local.json", ".codex/hooks.json"} {
			if _, err := os.Stat(filepath.Join(repo, p)); err != nil {
				t.Errorf("hook missing at %s: %v", p, err)
			}
		}
	})
}
