package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func doctorJSON(t *testing.T, dir string) setupReport {
	t.Helper()
	code, out, stderr := runCLI(t, "doctor", "--root", dir, "--format", "json")
	if code != 0 {
		t.Fatalf("doctor exited %d: %s", code, stderr)
	}
	var report setupReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return report
}

func initNoReviewers(t *testing.T, flags ...string) {
	t.Helper()
	code, _, stderr := runCLI(t, append([]string{"init", "--no-reviewers"}, flags...)...)
	if code != 0 {
		t.Fatal(stderr)
	}
}

func TestDoctorStates(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	makeHarness(t, filepath.Join(home, ".codex"))
	dir := t.TempDir()

	fresh := doctorJSON(t, dir)
	if fresh.State != setupNotInstalled || fresh.Next != "slopcop init" || len(fresh.Harnesses) != 2 || len(fresh.Missing) != 6 {
		t.Fatalf("fresh: %+v", fresh)
	}

	initNoReviewers(t)
	ready := doctorJSON(t, dir)
	if ready.State != setupReady || len(ready.Missing) != 0 || ready.Next != "" {
		t.Fatalf("ready: %+v", ready)
	}
	for _, h := range ready.Harnesses {
		if !h.Skill || !h.StopHook || !h.PushGate {
			t.Fatalf("ready harness: %+v", h)
		}
	}
	// Codex hooks are installed and their trust cannot be read, so doctor
	// says what Codex needs.
	if !strings.Contains(strings.Join(ready.Notes, "\n"), "/hooks") {
		t.Fatalf("no Codex trust note: %+v", ready.Notes)
	}

	// A skill file without its frontmatter would not load.
	claudeSkill := filepath.Join(home, ".claude/skills/slopcop", skillFileName)
	original, _ := os.ReadFile(claudeSkill)
	if err := os.WriteFile(claudeSkill, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	report := doctorJSON(t, dir)
	if strings.Join(report.Missing, "|") != "Claude Code's slopcop skill" {
		t.Fatalf("empty skill counted: %+v", report)
	}
	// Init keeps an existing file, so doctor says to move it aside first.
	if !strings.Contains(strings.Join(report.Notes, "\n"), claudeSkill+" is not a valid slopcop skill") {
		t.Fatalf("no remedy for the broken skill: %+v", report.Notes)
	}
	if err := os.WriteFile(claudeSkill, original, 0o644); err != nil {
		t.Fatal(err)
	}

	// A hook taken out by hand is missing, and doctor only reports it.
	settings := filepath.Join(home, ".codex/hooks.json")
	if err := os.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	partial := doctorJSON(t, dir)
	if partial.State != setupIncomplete || strings.Join(partial.Missing, "|") != "Codex's Stop hook|Codex's push gate" {
		t.Fatalf("partial: %+v", partial)
	}
	if data, _ := os.ReadFile(settings); string(data) != "{}\n" {
		t.Fatalf("doctor changed the settings: %s", data)
	}

	// A Stop hook of another type or under another event, and a gate whose
	// matcher misses the shell, would not run, so they do not count.
	misplaced := `{"hooks":{"Stop":[{"hooks":[{"type":"prompt","command":"slopcop hook codex || true"}]}],` +
		`"SessionStart":[{"hooks":[{"type":"command","command":"slopcop hook codex || true"}]}],` +
		`"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"slopcop gate --hook codex"}]}]}}`
	if err := os.WriteFile(settings, []byte(misplaced), 0o600); err != nil {
		t.Fatal(err)
	}
	if report := doctorJSON(t, dir); strings.Join(report.Missing, "|") != "Codex's Stop hook|Codex's push gate" {
		t.Fatalf("misplaced hooks counted: %+v", report)
	}
}

// A harness installed after init ran has nothing until init runs again.
func TestDoctorHarnessInstalledAfterInit(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	initNoReviewers(t)
	makeHarness(t, filepath.Join(home, ".grok"))
	report := doctorJSON(t, t.TempDir())
	if report.State != setupIncomplete {
		t.Fatalf("%+v", report)
	}
	for _, h := range report.Harnesses {
		// Grok reads the shared ~/.agents skill init wrote.
		if h.Name == "grok" && (!h.Skill || h.StopHook || h.PushGate) {
			t.Fatalf("grok: %+v", h)
		}
	}
}

func TestDoctorWithoutAHarness(t *testing.T) {
	isolateAgentHome(t)
	report := doctorJSON(t, t.TempDir())
	if report.State != setupNotInstalled || len(report.Harnesses) != 0 || strings.Join(report.Missing, "|") != "the slopcop skill" {
		t.Fatalf("%+v", report)
	}
	initNoReviewers(t, "--no-hooks")
	if report := doctorJSON(t, t.TempDir()); report.State != setupReady {
		t.Fatalf("%+v", report)
	}
}

func TestDoctorListsLegacyRepositoryFiles(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	// Git reports the resolved path, /private/var on macOS.
	repo := gitAt(t, testInitRepo(t), "rev-parse", "--show-toplevel")
	hook := filepath.Join(repo, ".claude/settings.local.json")
	skill := filepath.Join(repo, ".agents/skills/slopcop")
	makeHarness(t, filepath.Dir(hook))
	makeHarness(t, skill)
	if err := os.WriteFile(hook, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"slopcop hook claude || true"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, skillFileName), []byte("---\nname: slopcop\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := doctorJSON(t, repo)
	if strings.Join(report.Legacy, "|") != hook+"|"+skill || !strings.Contains(strings.Join(report.Notes, "\n"), "removes the per-repository leftovers") {
		t.Fatalf("%+v", report)
	}
	// A command under another event is beyond the cleanup, so it is not
	// listed as something init removes.
	if err := os.WriteFile(hook, []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"slopcop hook claude || true"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if report := doctorJSON(t, repo); strings.Join(report.Legacy, "|") != skill {
		t.Fatalf("misplaced legacy hook listed: %+v", report.Legacy)
	}
	if err := os.WriteFile(hook, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"slopcop hook claude || true"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A tracked settings file is the repository's own; init leaves it, so
	// doctor does not list it.
	gitAt(t, repo, "add", "-f", hook)
	if report := doctorJSON(t, repo); strings.Join(report.Legacy, "|") != skill {
		t.Fatalf("tracked hook listed: %+v", report.Legacy)
	}
	// Outside the checkout there is nothing to clean.
	if report := doctorJSON(t, t.TempDir()); len(report.Legacy) != 0 {
		t.Fatalf("%+v", report)
	}
}

func TestDoctorHumanOutput(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	code, out, stderr := runCLI(t, "doctor", "--root", t.TempDir())
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"Setup:       not installed", "Claude Code: skill missing, Stop hook missing, push gate missing", "Next:        run `slopcop init`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
