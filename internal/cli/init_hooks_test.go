package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedLegacyRepositoryInstall(t *testing.T, repo string) (claudeSettings, codexSettings, skill string) {
	t.Helper()
	claudeSettings = filepath.Join(repo, ".claude/settings.local.json")
	codexSettings = filepath.Join(repo, ".codex/hooks.json")
	skill = filepath.Join(repo, ".agents/skills/slopcop")
	makeHarness(t, filepath.Dir(claudeSettings))
	makeHarness(t, filepath.Dir(codexSettings))
	if err := os.WriteFile(claudeSettings, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"slopcop hook claude || true"}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"slopcop gate --hook claude"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexSettings, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"slopcop hook codex || true"}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"slopcop gate --hook codex"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := bundledSkillFiles(skillName)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSkill(skill, files, "v0.0.17"); err != nil {
		t.Fatal(err)
	}
	return claudeSettings, codexSettings, skill
}

func assertNoSlopCop(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "slopcop") {
		t.Fatalf("SlopCop remains in %s: %s", path, data)
	}
}

func TestInitRemoveHooksLeavesSkillsAndMigratesCurrentRepository(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	claude, codex, legacySkill := seedLegacyRepositoryInstall(t, repo)

	code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	assertNoSlopCop(t, claude)
	assertNoSlopCop(t, codex)
	if _, err := os.Stat(legacySkill); !os.IsNotExist(err) {
		t.Fatalf("legacy repository skill remains: %v", err)
	}
	userSkill := filepath.Join(home, ".claude/skills/slopcop/SKILL.md")
	if _, err := os.Stat(userSkill); err != nil {
		t.Fatal(err)
	}

	code, _, stderr = runCLI(t, "init", "--remove-hooks", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	assertNoSlopCop(t, filepath.Join(home, ".claude/settings.json"))
	if _, err := os.Stat(userSkill); err != nil {
		t.Fatalf("--remove-hooks removed the user skill: %v", err)
	}
}

func TestInitHookAndSkillOptOutsAreIndependent(t *testing.T) {
	t.Run("no hooks", func(t *testing.T) {
		home := isolateAgentHome(t)
		makeHarness(t, filepath.Join(home, ".claude"))
		code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", t.TempDir(), "--no-hooks")
		if code != 0 {
			t.Fatal(stderr)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude/skills/slopcop/SKILL.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude/settings.json")); !os.IsNotExist(err) {
			t.Errorf("hook settings created: %v", err)
		}
	})
	t.Run("no skill", func(t *testing.T) {
		home := isolateAgentHome(t)
		makeHarness(t, filepath.Join(home, ".claude"))
		code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", t.TempDir(), "--no-skill")
		if code != 0 {
			t.Fatal(stderr)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude/skills/slopcop")); !os.IsNotExist(err) {
			t.Errorf("skill installed: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
		if err != nil || !strings.Contains(string(data), "slopcop hook claude") {
			t.Fatalf("hooks missing: %s %v", data, err)
		}
	})
}

// A checkout whose old hook runs after the upgrade is migrated by that hook's
// first command, so users do not have to revisit every existing repository.
func TestAnExistingRepositoryHookMigratesItself(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	if code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", t.TempDir()); code != 0 {
		t.Fatal(stderr)
	}
	repo := testInitRepo(t)
	claude, codex, legacySkill := seedLegacyRepositoryInstall(t, repo)
	payload, _ := json.Marshal(map[string]any{
		"cwd": repo, "tool_name": "Bash", "tool_input": map[string]string{"command": "git status"},
	})
	cmd := newGateCommand()
	cmd.SetIn(bytes.NewReader(payload))
	cmd.SetArgs([]string{"--hook", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	assertNoSlopCop(t, claude)
	assertNoSlopCop(t, codex)
	if _, err := os.Stat(legacySkill); !os.IsNotExist(err) {
		t.Fatalf("legacy repository skill remains: %v", err)
	}
}
