package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenthooks "github.com/BrokkAi/slopcop/internal/hooks"
)

// TestInitNoHooksNoSkillKeepUserLevelInstalls checks that the opt-out flags
// leave the machine-wide hook and skill alone.
func TestInitNoHooksNoSkillKeepUserLevelInstalls(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	if _, err := agenthooks.Install(); err != nil {
		t.Fatal(err)
	}
	files, err := bundledSkillFiles(skillName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installSkillAt(filepath.Join(home, ".claude", "skills"), skillName, files, "test"); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	skill := filepath.Join(home, ".claude", "skills", skillName, skillFileName)
	hooked := func() bool {
		data, _ := os.ReadFile(settings)
		return strings.Contains(string(data), "slopcop")
	}
	if !hooked() {
		t.Fatal("user-level hook was not installed")
	}
	repo := testInitRepo(t)
	if code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo, "--no-hooks", "--no-skill"); code != 0 {
		t.Fatal(stderr)
	}
	if !hooked() {
		t.Fatal("--no-hooks removed the user-level hook")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("--no-skill removed the user-level skill: %v", err)
	}
	if code, _, stderr := runCLI(t, "init", "--no-reviewers", "--root", repo); code != 0 {
		t.Fatal(stderr)
	}
	if !hooked() {
		t.Fatal("init removed the user-level hook")
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("init removed the user-level skill: %v", err)
	}
}
