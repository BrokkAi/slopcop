package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/BrokkAi/slopcop/internal/attribution"
	"github.com/BrokkAi/slopcop/internal/service"
)

const hookSession = "session-one"

// fakeService answers the repository listing and the check the hook sends,
// and counts both.
type fakeService struct {
	// active names the repositories the workspace checks.
	active []string
	// findings are the paths the check reports a new finding in.
	findings []string
	// refusal, when set, is the error body every request is answered with.
	refusal string
	// outages is how many requests a gateway answers with a bare 503 first.
	outages int
	lists   int
	checks  int
	// workspaces are the SlopCop-Workspace headers the requests carried.
	workspaces []string
}

func (f *fakeService) start(t *testing.T, repo string) *httptest.Server {
	t.Helper()
	base := runGit(t, repo, "rev-parse", "HEAD")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.workspaces = append(f.workspaces, r.Header.Get("SlopCop-Workspace"))
		w.Header().Set("Content-Type", "application/json")
		if f.refusal != "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(f.refusal))
			return
		}
		if f.outages > 0 {
			f.outages--
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("<html>Service Unavailable</html>"))
			return
		}
		switch r.URL.Path {
		case "/v1/repositories":
			f.lists++
			listed := service.RepositoryList{}
			for _, name := range f.active {
				listed.Repositories = append(listed.Repositories, service.Repository{ID: name, Provider: "github", FullName: name, Enabled: true, Active: true})
			}
			_ = json.NewEncoder(w).Encode(listed)
		case "/v1/check":
			f.checks++
			response := service.Response{Status: service.StatusClean, BaseCommit: base, Applied: true, Findings: []service.Finding{}, Incomplete: []service.IncompletePolicy{}}
			line := 1
			for i, path := range f.findings {
				response.Status = service.StatusFindings
				response.Findings = append(response.Findings, service.Finding{
					ID: fakeFindingID(i), PolicyID: "demo.policy", Severity: "warning", Certainty: "definite",
					Path: path, StartLine: &line, Title: "Fix " + path, New: true,
				})
			}
			response.Summary.Findings = len(response.Findings)
			response.Summary.NewFindings = len(response.Findings)
			response.Summary.FailOn = failOnFinding
			_ = json.NewEncoder(w).Encode(response)
		case "/v1/ignore":
			var body service.IgnoreRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(service.IgnoreResponse{FindingID: body.FindingID, Status: "ignored"})
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// fakeFindingID is the id the fake service gives its i-th finding.
func fakeFindingID(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
}

// isolateHook isolates the user's configuration, signs in, and gives Claude
// Code and Codex directories to keep session records in.
func isolateHook(t *testing.T) {
	t.Helper()
	isolateUserConfig(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("SLOPCOP_TOKEN", "test-token")
}

// writeCodexEdits writes the rollout of the Codex session's root thread,
// which wrote files in repo, and returns its path.
func writeCodexEdits(t *testing.T, repo string, files ...string) string {
	t.Helper()
	return writeCodexThread(t, hookSession, repo, files...)
}

// writeCodexThread writes the rollout of thread, of the Codex session, in
// which it wrote files in repo, and returns its path.
func writeCodexThread(t *testing.T, thread, repo string, files ...string) string {
	t.Helper()
	meta, _ := json.Marshal(map[string]any{
		"type":    "session_meta",
		"payload": map[string]any{"session_id": hookSession, "id": thread},
	})
	context, _ := json.Marshal(map[string]any{
		"type":    "turn_context",
		"payload": map[string]any{"turn_id": "turn-1", "model": "gpt-6-luna", "effort": "high"},
	})
	lines := []string{string(meta), string(context)}
	for i, file := range files {
		change, _ := json.Marshal(map[string]any{
			"type": "event_msg",
			"payload": map[string]any{"type": "item_completed", "turn_id": "turn-1", "item": map[string]any{
				"type": "FileChange", "id": fmt.Sprintf("exec-%d", i), "status": "completed",
				"changes": map[string]any{filepath.Join(repo, filepath.FromSlash(file)): map[string]any{"type": "add"}},
			}},
		})
		lines = append(lines, string(change))
	}
	path := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "2026", "10", "29", "rollout-2026-10-29T10-00-00-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSessionEdits writes the transcript of a Claude Code session that
// wrote files in repo.
func writeSessionEdits(t *testing.T, repo string, files ...string) {
	t.Helper()
	var lines []string
	for i, file := range files {
		id := fmt.Sprintf("toolu_%d", i)
		use, _ := json.Marshal(map[string]any{
			"type": "assistant",
			"message": map[string]any{"model": "claude-opus-5-5", "content": []any{map[string]any{
				"type": "tool_use", "id": id, "name": "Write",
				"input": map[string]any{"file_path": filepath.Join(repo, filepath.FromSlash(file))},
			}}},
		})
		result, _ := json.Marshal(map[string]any{
			"type":    "user",
			"message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id}}},
		})
		lines = append(lines, string(use), string(result))
	}
	path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", "test", hookSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stopPayload(repo string, active bool) string {
	return fmt.Sprintf(`{"hook_event_name":"Stop","cwd":%q,"session_id":%q,"stop_hook_active":%t}`, repo, hookSession, active)
}

func runTestHook(t *testing.T, serviceURL, payload string) hookOutput {
	t.Helper()
	return runHookAs(t, attribution.ClaudeHarness, serviceURL, payload)
}

func runHookAs(t *testing.T, harness, serviceURL, payload string) hookOutput {
	t.Helper()
	command := &cobra.Command{}
	command.SetContext(context.Background())
	command.SetIn(strings.NewReader(payload))
	return runHook(command, harness, serviceURL, "test")
}

func TestHookHoldsTheAgentOnceForFindingsInItsFiles(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix demo.go") {
		t.Fatalf("output = %+v", output)
	}
	// Held back, the agent stops again without a change: it is let go and
	// the user hears what remains.
	output = runTestHook(t, server.URL, stopPayload(repo, true))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "remain") {
		t.Fatalf("held output = %+v", output)
	}
	// A later turn with the same tree is not held again.
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("unchanged output = %+v", output)
	}
	if fake.checks != 1 || fake.lists != 1 {
		t.Fatalf("checks = %d, lists = %d, want 1 and 1", fake.checks, fake.lists)
	}

	// A changed tree is checked again, and new findings hold the agent again.
	writeFile(t, repo, "demo.go", "package demo // changed\n")
	output = runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "block" || fake.checks != 2 || fake.lists != 1 {
		t.Fatalf("changed output = %+v, checks = %d, lists = %d", output, fake.checks, fake.lists)
	}
}

func TestHookRunsInTheWorkspaceACheckByHandWould(t *testing.T) {
	isolateHook(t)
	const workspace = "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b"
	t.Setenv("SLOPCOP_WORKSPACE", workspace)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}}
	server := fake.start(t, repo)

	runTestHook(t, server.URL, stopPayload(repo, false))
	if fake.lists != 1 || fake.checks != 1 {
		t.Fatalf("lists = %d, checks = %d, want 1 and 1", fake.lists, fake.checks)
	}
	for _, got := range fake.workspaces {
		if got != workspace {
			t.Fatalf("workspaces = %v, want every request in %s", fake.workspaces, workspace)
		}
	}
}

func TestHookSendsNothingForASessionThatWroteNothing(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("output = %+v", output)
	}
	if fake.checks != 0 || fake.lists != 0 {
		t.Fatalf("checks = %d, lists = %d, want none", fake.checks, fake.lists)
	}
}

func TestHookDoesNotHoldTheAgentForFindingsOutsideItsFiles(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "mine.go", "package demo\n")
	writeFile(t, repo, "theirs.go", "package demo\n")
	writeSessionEdits(t, repo, "mine.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"theirs.go"}}
	server := fake.start(t, repo)

	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "1 new finding(s) in files this session did not write") {
		t.Fatalf("output = %+v", output)
	}
	// The user is told once, not every turn.
	writeFile(t, repo, "mine.go", "package demo // changed\n")
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("second output = %+v", output)
	}
}

func TestHookSendsNothingForARepositoryTheWorkspaceDoesNotCheck(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/someone/private.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	for i := 0; i < 2; i++ {
		writeFile(t, repo, "demo.go", fmt.Sprintf("package demo // %d\n", i))
		if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
			t.Fatalf("output = %+v", output)
		}
	}
	if fake.checks != 0 || fake.lists != 1 {
		t.Fatalf("checks = %d, lists = %d, want 0 and 1", fake.checks, fake.lists)
	}
}

func TestHookSendsNothingWhenSignedOut(t *testing.T) {
	isolateHook(t)
	t.Setenv("SLOPCOP_TOKEN", "")
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("output = %+v", output)
	}
	if fake.checks != 0 || fake.lists != 0 {
		t.Fatalf("checks = %d, lists = %d, want none", fake.checks, fake.lists)
	}
}

func TestHookUsesTheResultOfACheckRunByHand(t *testing.T) {
	isolateHook(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", hookSession)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL)
	if code != 1 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	// The agent saw the findings and stopped without fixing them: the hook
	// holds it back without sending the same check again.
	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix demo.go") {
		t.Fatalf("output = %+v", output)
	}
	if fake.checks != 1 || fake.lists != 0 {
		t.Fatalf("checks = %d, lists = %d, want 1 and 0", fake.checks, fake.lists)
	}
}

func TestHookReportsARefusalOnceAndStops(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{refusal: `{"code":"auth_expired","message":"Sign in again.","remedy":"Run slopcop login.","retryable":false}`}
	server := fake.start(t, repo)

	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "Run slopcop login.") {
		t.Fatalf("output = %+v", output)
	}
	writeFile(t, repo, "demo.go", "package demo // changed\n")
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("second output = %+v", output)
	}
}

func TestHookIgnoresWhatIsNotAStopOfASession(t *testing.T) {
	isolateHook(t)
	output := runTestHook(t, "http://127.0.0.1:1", `{not json`)
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "could not read") {
		t.Fatalf("malformed output = %+v", output)
	}
	for _, payload := range []string{
		`{"hook_event_name":"PostToolUse","cwd":"/","session_id":"s"}`,
		`{"hook_event_name":"Stop","cwd":"","session_id":"s"}`,
		fmt.Sprintf(`{"hook_event_name":"Stop","cwd":%q,"session_id":"s"}`, t.TempDir()),
	} {
		if output := runTestHook(t, "http://127.0.0.1:1", payload); output != (hookOutput{}) {
			t.Fatalf("%s: output = %+v", payload, output)
		}
	}
}

func TestHookCommandNeverFails(t *testing.T) {
	isolateHook(t)
	for _, args := range [][]string{{"hook", "other"}, {"hook"}, {"hook", "claude", "--unknown"}} {
		var stdout, stderr bytes.Buffer
		code := execute(context.Background(), args, "test", &stdout, &stderr)
		if code != 0 || strings.TrimSpace(stdout.String()) != "{}" {
			t.Fatalf("%v: exit = %d, stdout = %q, stderr = %q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestHookCacheFailureDoesNotHoldTheAgentTwice(t *testing.T) {
	isolateHook(t)
	// A file where the cache directory belongs makes every cache write fail.
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output.Decision != "block" {
		t.Fatalf("first output = %+v", output)
	}
	if output := runTestHook(t, server.URL, stopPayload(repo, true)); output.Decision != "" {
		t.Fatalf("continued hook blocked again: %+v", output)
	}
}

func TestHookTreatsABriefOutageAsPassing(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}, outages: 1}
	server := fake.start(t, repo)

	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "did not run") {
		t.Fatalf("output during the outage = %+v", output)
	}
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output.Decision != "block" {
		t.Fatalf("output after the outage = %+v", output)
	}
}

func TestHookMatchesTheRepositoryProvider(t *testing.T) {
	isolateHook(t)
	// The workspace has github.com/BrokkAi/slopcop; this is a GitLab clone.
	repo := newRepo(t, "https://gitlab.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("output = %+v", output)
	}
	if fake.checks != 0 {
		t.Fatalf("checks = %d, want none", fake.checks)
	}
}

func TestACheckByHandTurnsTheHookBackOn(t *testing.T) {
	isolateHook(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", hookSession)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	// Not active yet: the hook turns off.
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) || fake.checks != 0 {
		t.Fatalf("output = %+v, checks = %d", output, fake.checks)
	}
	// The person adds the repository with a check by hand.
	fake.active = []string{"BrokkAi/slopcop"}
	if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 1 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	output := runTestHook(t, server.URL, stopPayload(repo, false))
	if output.Decision != "block" || fake.checks != 1 {
		t.Fatalf("output = %+v, checks = %d", output, fake.checks)
	}
}

func TestACheckByHandOfTheSameTreeDoesNotHoldAgain(t *testing.T) {
	isolateHook(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", hookSession)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output.Decision != "block" {
		t.Fatalf("first output = %+v", output)
	}
	runTestHook(t, server.URL, stopPayload(repo, true))
	if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 1 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("output after the same check by hand = %+v", output)
	}
}

func TestACheckByHandInAnotherWorkspaceDoesNotOpenTheGate(t *testing.T) {
	isolateHook(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", hookSession)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{}
	server := fake.start(t, repo)

	const other = "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b"
	if code, _, stderr := runCLI(t, "check", "--root", repo, "-w", other, "--service", server.URL); code != 0 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	// The login's own workspace does not have the repository.
	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("output = %+v", output)
	}
	if fake.lists != 1 || fake.checks != 1 {
		t.Fatalf("lists = %d, checks = %d, want 1 and only the check by hand", fake.lists, fake.checks)
	}
}

func TestAnIgnoredFindingIsNotHandedBack(t *testing.T) {
	isolateHook(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", hookSession)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output.Decision != "block" {
		t.Fatalf("first output = %+v", output)
	}
	t.Chdir(repo)
	if code, _, stderr := runCLI(t, "ignore", fakeFindingID(0), "--service", server.URL); code != 0 {
		t.Fatalf("ignore exit = %d, stderr = %s", code, stderr)
	}
	if output := runTestHook(t, server.URL, stopPayload(repo, true)); output != (hookOutput{}) {
		t.Fatalf("output after the ignore = %+v", output)
	}
}

func TestHookHoldsAfterAnotherHookHeld(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}}
	server := fake.start(t, repo)

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) {
		t.Fatalf("clean output = %+v", output)
	}
	// Another Stop hook held the agent, which then wrote a finding.
	fake.findings = []string{"demo.go"}
	writeFile(t, repo, "demo.go", "package demo // changed\n")
	if output := runTestHook(t, server.URL, stopPayload(repo, true)); output.Decision != "block" {
		t.Fatalf("output = %+v", output)
	}
}

func TestHookHoldsAtMostTwiceInARow(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	for i, want := range []string{"block", "block", ""} {
		writeFile(t, repo, "demo.go", fmt.Sprintf("package demo // %d\n", i))
		output := runTestHook(t, server.URL, stopPayload(repo, i > 0))
		if output.Decision != want {
			t.Fatalf("stop %d: output = %+v, want decision %q", i, output, want)
		}
	}
}

func TestSendCheckDropsARefusedAttributionForLaterSends(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if _, ok := body["attribution"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"invalid_change","message":"Unknown field.","remedy":"Update.","retryable":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"clean","baseCommit":"0000000000000000000000000000000000000000","applied":true,"summary":{},"findings":[],"incomplete":[]}`))
	}))
	defer server.Close()
	client, err := service.New(server.URL, "", "slopcop/test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := service.Request{
		RepoURL:     "https://github.com/BrokkAi/slopcop",
		BaseCommit:  "0000000000000000000000000000000000000000",
		Attribution: &service.Attribution{Harness: "claude-code"},
	}
	if _, err := sendCheck(context.Background(), client, &request, nil); err != nil {
		t.Fatal(err)
	}
	if request.Attribution != nil {
		t.Fatal("the request still carries the attribution the service refused")
	}
}

func TestCodexHookHoldsTheAgentForFindingsInItsFiles(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeFile(t, repo, "theirs.go", "package demo\n")
	rollout := writeCodexEdits(t, repo, "demo.go")
	// The hook's environment names another Codex home; the payload's
	// rollout path is the one read.
	t.Setenv("CODEX_HOME", t.TempDir())
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go", "theirs.go"}}
	server := fake.start(t, repo)

	payload := func(active bool) string {
		return fmt.Sprintf(`{"hook_event_name":"Stop","cwd":%q,"session_id":%q,"transcript_path":%q,"stop_hook_active":%t,"turn_id":"turn-1","model":"gpt-6-luna"}`,
			repo, hookSession, rollout, active)
	}
	output := runHookAs(t, attribution.CodexHarness, server.URL, payload(false))
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix demo.go") || strings.Contains(output.Reason, "theirs.go") {
		t.Fatalf("output = %+v", output)
	}
	output = runHookAs(t, attribution.CodexHarness, server.URL, payload(true))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "remain") || fake.checks != 1 {
		t.Fatalf("held output = %+v, checks = %d", output, fake.checks)
	}
}

func TestCodexHookUsesTheResultOfACheckRunByHand(t *testing.T) {
	isolateHook(t)
	t.Setenv("CODEX_THREAD_ID", hookSession)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeCodexEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 1 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	output := runHookAs(t, attribution.CodexHarness, server.URL, stopPayload(repo, false))
	if output.Decision != "block" || fake.checks != 1 {
		t.Fatalf("output = %+v, checks = %d", output, fake.checks)
	}
}

func TestCodexCheckByHandInASubagentIsTheSessions(t *testing.T) {
	isolateHook(t)
	const subagent = "01a182c8-0000-7000-8000-000000000002"
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeCodexEdits(t, repo)
	writeCodexThread(t, subagent, repo, "demo.go")
	// The check runs in the subagent, whose commands name its own thread.
	t.Setenv("CODEX_THREAD_ID", subagent)
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	if code, _, stderr := runCLI(t, "check", "--root", repo, "--service", server.URL); code != 1 {
		t.Fatalf("check exit = %d, stderr = %s", code, stderr)
	}
	// The session's Stop hook finds the subagent's edit and its check.
	output := runHookAs(t, attribution.CodexHarness, server.URL, stopPayload(repo, false))
	if output.Decision != "block" || fake.checks != 1 {
		t.Fatalf("output = %+v, checks = %d", output, fake.checks)
	}
}

func TestCodexHookWaitsForALateRollout(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	edited, err := os.ReadFile(writeCodexEdits(t, repo, "demo.go"))
	if err != nil {
		t.Fatal(err)
	}
	rollout := writeCodexEdits(t, repo)
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	// Codex writes the turn's edit just after the hook first reads.
	done := make(chan error)
	go func() {
		time.Sleep(rolloutSettle / 4)
		done <- os.WriteFile(rollout, edited, 0o600)
	}()
	payload := fmt.Sprintf(`{"hook_event_name":"Stop","cwd":%q,"session_id":%q,"transcript_path":%q}`, repo, hookSession, rollout)
	output := runHookAs(t, attribution.CodexHarness, server.URL, payload)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if output.Decision != "block" || fake.checks != 1 {
		t.Fatalf("output = %+v, checks = %d", output, fake.checks)
	}
}

func TestHookStatesArePrunedOnceADay(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-2 * hookStateLifetime)
	stale := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := stale("first.json")
	saveHookState(filepath.Join(dir, "current.json"), hookState{})
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("an over session's state was kept: %v", err)
	}
	second := stale("second.json")
	saveHookState(filepath.Join(dir, "current.json"), hookState{})
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("pruned again within the day: %v", err)
	}
}

func TestBoundedHookText(t *testing.T) {
	got := boundedHookText(strings.Repeat("界", maxHookOutputRunes+1))
	if len([]rune(got)) != maxHookOutputRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded length = %d", len([]rune(got)))
	}
}
