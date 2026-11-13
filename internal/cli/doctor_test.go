package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func doctorJSON(t *testing.T, repo string) setupReport {
	t.Helper()
	code, out, stderr := runCLI(t, "doctor", "--root", repo, "--format", "json")
	if code != 0 {
		t.Fatalf("doctor exited %d: %s", code, stderr)
	}
	var report setupReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return report
}

// sessionStart runs the SessionStart hook for harness in dir and returns
// what it printed.
func sessionStart(t *testing.T, harness, dir string) string {
	t.Helper()
	cmd := newDoctorCommand()
	payload, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "cwd": dir, "source": "startup"})
	cmd.SetIn(bytes.NewReader(payload))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--hook", harness})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func initNoReviewers(t *testing.T, repo string, flags ...string) {
	t.Helper()
	code, _, stderr := runCLI(t, append([]string{"init", "--no-reviewers", "--root", repo}, flags...)...)
	if code != 0 {
		t.Fatal(stderr)
	}
}

// addWorktree commits once in repo and adds a linked worktree of it, which
// has none of the untracked files init wrote in repo.
func addWorktree(t *testing.T, repo string) string {
	t.Helper()
	gitAt(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "base")
	dir := filepath.Join(t.TempDir(), "wt")
	gitAt(t, repo, "worktree", "add", "-q", dir)
	return dir
}

func TestDoctorStates(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	makeHarness(t, filepath.Join(home, ".codex"))
	repo := testInitRepo(t)

	fresh := doctorJSON(t, repo)
	if fresh.State != setupNotSetUp || fresh.Next != "slopcop init" || len(fresh.Harnesses) != 2 || len(fresh.Missing) != 6 {
		t.Fatalf("fresh: %+v", fresh)
	}

	initNoReviewers(t, repo)
	ready := doctorJSON(t, repo)
	if ready.State != setupReady || !ready.Initialized || len(ready.Missing) != 0 || ready.Next != "" {
		t.Fatalf("ready: %+v", ready)
	}
	// Codex hooks are installed, and their trust cannot be read, so doctor
	// says what Codex needs.
	if !strings.Contains(strings.Join(ready.Notes, "\n"), "/hooks") {
		t.Fatalf("no Codex trust note: %+v", ready.Notes)
	}

	// A hook taken out by hand is missing, and doctor only reports it.
	settings := filepath.Join(repo, ".codex/hooks.json")
	if err := os.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	partial := doctorJSON(t, repo)
	if partial.State != setupPartial || strings.Join(partial.Missing, "|") != "Codex's Stop hook|Codex's push gate" || partial.Next != "slopcop init" {
		t.Fatalf("partial: %+v", partial)
	}
	if data, _ := os.ReadFile(settings); string(data) != "{}\n" {
		t.Fatalf("doctor changed the settings: %s", data)
	}
}

func TestDoctorRespectsDeclinedPieces(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	initNoReviewers(t, repo, "--no-hooks")
	report := doctorJSON(t, repo)
	if report.State != setupReady || !report.HooksDeclined || report.SkillDeclined {
		t.Fatalf("--no-hooks: %+v", report)
	}

	// --remove-hooks records the hooks as declined and keeps the skill.
	repo = testInitRepo(t)
	initNoReviewers(t, repo)
	initNoReviewers(t, repo, "--remove-hooks")
	report = doctorJSON(t, repo)
	if report.State != setupReady || !report.HooksDeclined || report.SkillDeclined {
		t.Fatalf("--remove-hooks: %+v", report)
	}

	// With the skill declined and since removed by hand from a hooks-only
	// setup, nothing is reported missing.
	repo = testInitRepo(t)
	initNoReviewers(t, repo, "--no-skill")
	report = doctorJSON(t, repo)
	if report.State != setupReady || !report.SkillDeclined {
		t.Fatalf("--no-skill: %+v", report)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude/settings.local.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	report = doctorJSON(t, repo)
	if report.State != setupPartial || report.Next != "slopcop init --no-skill" {
		t.Fatalf("--no-skill, hooks removed: %+v", report)
	}
}

func TestDoctorFreshWorktreeOfAnInitializedRepository(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)
	initNoReviewers(t, repo, "--no-hooks")
	worktree := addWorktree(t, repo)
	report := doctorJSON(t, worktree)
	if !report.Worktree || !report.Initialized || !report.HooksDeclined || report.State != setupPartial {
		t.Fatalf("worktree: %+v", report)
	}
	// The worktree lacks the skill only, and the fix keeps hooks declined.
	if report.Next != "slopcop init --no-hooks" || len(report.Missing) != 1 {
		t.Fatalf("worktree: %+v", report)
	}
	reminder := sessionStart(t, "claude", worktree)
	if !strings.Contains(reminder, "belongs to a repository set up for SlopCop") || !strings.Contains(reminder, "slopcop init --no-hooks") {
		t.Fatal(reminder)
	}
}

func TestDoctorOutsideGit(t *testing.T) {
	isolateAgentHome(t)
	dir := t.TempDir()
	if report := doctorJSON(t, dir); report.State != setupNotGit {
		t.Fatalf("%+v", report)
	}
	if out := sessionStart(t, "claude", dir); out != "" {
		t.Fatalf("non-Git directory got a reminder: %s", out)
	}
}

func TestSessionCheckRemindsOncePerState(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	repo := testInitRepo(t)

	first := sessionStart(t, "claude", repo)
	var answer sessionStartOutput
	if err := json.Unmarshal([]byte(first), &answer); err != nil || answer.HookSpecificOutput == nil {
		t.Fatalf("%v: %q", err, first)
	}
	if answer.HookSpecificOutput.HookEventName != "SessionStart" || !strings.Contains(answer.HookSpecificOutput.AdditionalContext, "not set up") ||
		!strings.Contains(answer.HookSpecificOutput.AdditionalContext, "ask the user") || !strings.Contains(answer.SystemMessage, "slopcop init") {
		t.Fatalf("%+v", answer)
	}
	// Opening the same checkout again does not repeat it.
	if again := sessionStart(t, "claude", repo); again != "" {
		t.Fatalf("repeated reminder: %s", again)
	}
	// A ready checkout is quiet.
	initNoReviewers(t, repo)
	if out := sessionStart(t, "claude", repo); out != "" {
		t.Fatalf("ready checkout got a reminder: %s", out)
	}
	// A new gap is a new state, told once.
	if err := os.WriteFile(filepath.Join(repo, ".claude/settings.local.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := sessionStart(t, "claude", repo); !strings.Contains(out, "incomplete") {
		t.Fatalf("changed state not reported: %q", out)
	}
	if out := sessionStart(t, "claude", repo); out != "" {
		t.Fatalf("repeated reminder: %s", out)
	}
}

func TestSessionCheckSilencedAndOtherPayloads(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".codex"))
	repo := testInitRepo(t)
	gitAt(t, repo, "config", "slopcop.sessionCheck", "false")
	if out := sessionStart(t, "codex", repo); out != "" {
		t.Fatalf("silenced repository got a reminder: %s", out)
	}
	if report := doctorJSON(t, repo); !report.SessionCheck.Silenced || report.State != setupNotSetUp {
		t.Fatalf("%+v", report)
	}

	repo = testInitRepo(t)
	// Grok runs Claude Code's user hooks too; its output is unverified.
	t.Setenv("GROK_HOOK_EVENT", "SessionStart")
	if out := sessionStart(t, "claude", repo); out != "" {
		t.Fatalf("Grok got a reminder: %s", out)
	}
	t.Setenv("GROK_HOOK_EVENT", "")
	for _, payload := range []string{`{"hook_event_name":"Stop","cwd":"` + repo + `"}`, "not json", ""} {
		cmd := newDoctorCommand()
		cmd.SetIn(strings.NewReader(payload))
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"--hook", "codex"})
		if err := cmd.Execute(); err != nil || out.Len() != 0 {
			t.Fatalf("payload %q: %v %s", payload, err, out.String())
		}
	}
	if out := sessionStart(t, "codex", repo); !strings.Contains(out, "Codex's Stop hook") {
		t.Fatalf("codex reminder: %s", out)
	}
}

func TestInitSessionCheckInstallsAndRemoves(t *testing.T) {
	home := isolateAgentHome(t)
	makeHarness(t, filepath.Join(home, ".claude"))
	makeHarness(t, filepath.Join(home, ".codex"))
	repo := testInitRepo(t)
	code, out, stderr := runCLI(t, "init", "--no-reviewers", "--session-check", "--root", repo)
	if code != 0 {
		t.Fatal(stderr)
	}
	if !strings.Contains(out, "installed the session check") || !strings.Contains(out, "trust it with /hooks") {
		t.Fatal(out)
	}
	claude, _ := os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	codex, _ := os.ReadFile(filepath.Join(home, ".codex/hooks.json"))
	if !strings.Contains(string(claude), `"SessionStart"`) || !strings.Contains(string(claude), "slopcop doctor --hook claude || true") {
		t.Fatal(string(claude))
	}
	if !strings.Contains(string(codex), "slopcop doctor --hook codex || exit /b 0") {
		t.Fatal(string(codex))
	}
	if report := doctorJSON(t, repo); !report.SessionCheck.Installed {
		t.Fatalf("%+v", report)
	}
	// Running it twice changes nothing.
	_, out, _ = runCLI(t, "init", "--no-reviewers", "--session-check", "--root", repo)
	if strings.Contains(out, "installed the session check") {
		t.Fatal(out)
	}
	code, out, stderr = runCLI(t, "init", "--no-reviewers", "--remove-hooks", "--root", repo)
	if code != 0 || !strings.Contains(out, "removed the session check") {
		t.Fatal(out, stderr)
	}
	claude, _ = os.ReadFile(filepath.Join(home, ".claude/settings.json"))
	if strings.Contains(string(claude), "slopcop doctor") {
		t.Fatal(string(claude))
	}
}
