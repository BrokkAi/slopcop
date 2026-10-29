package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitInstallsAndRemovesHooksWithoutRemovingTheSkill(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	makeHarness(t, filepath.Join(home, ".codex"))

	code, stdout, stderr := runCLI(t, "init")
	if code != 0 {
		t.Fatalf("init exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "/hooks") {
		t.Fatalf("init did not explain Codex trust: %q", stdout)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	codexHooks := filepath.Join(home, ".codex", "hooks.json")
	for _, path := range []string{settings, codexHooks} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "slopcop hook") {
			t.Fatalf("%s = %s", path, data)
		}
	}

	code, stdout, stderr = runCLI(t, "init", "--remove-hooks")
	if code != 0 {
		t.Fatalf("remove exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "removed") {
		t.Fatalf("remove stdout = %q", stdout)
	}
	for _, path := range []string{settings, codexHooks} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "slopcop hook") {
			t.Fatalf("%s still holds the hook: %s", path, data)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "slopcop", "SKILL.md")); err != nil {
		t.Fatalf("remove removed the skill: %v", err)
	}

	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	calls := 0
	server := serveCheck(t, repo, "clean", &calls)
	if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 0 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "slopcop hook") {
		t.Fatal("an ordinary check reinstalled a removed hook")
	}
}

func TestInitHookAndSkillOptOutsAreIndependent(t *testing.T) {
	t.Run("no hooks", func(t *testing.T) {
		home := isolateAgentHome(t)
		makeHarness(t, filepath.Join(home, ".claude"))
		code, stdout, stderr := runCLI(t, "init", "--no-hooks")
		if code != 0 {
			t.Fatalf("init exit = %d, stderr = %s", code, stderr)
		}
		if !strings.Contains(stdout, "--no-hooks") {
			t.Fatalf("stdout = %q", stdout)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "slopcop", "SKILL.md")); err != nil {
			t.Fatalf("skill was not installed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
			t.Fatalf("hooks were installed: %v", err)
		}
	})

	t.Run("no skill", func(t *testing.T) {
		home := isolateAgentHome(t)
		makeHarness(t, filepath.Join(home, ".claude"))
		code, _, stderr := runCLI(t, "init", "--no-skill")
		if code != 0 {
			t.Fatalf("init exit = %d, stderr = %s", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
			t.Fatalf("skill was installed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
			t.Fatalf("hook was not installed: %v", err)
		}
	})
}
