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

	"github.com/spf13/cobra"

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
					ID: fmt.Sprintf("finding-%d", i), PolicyID: "demo.policy", Severity: "warning", Certainty: "definite",
					Path: path, StartLine: &line, Title: "Fix " + path, New: true,
				})
			}
			response.Summary.Findings = len(response.Findings)
			response.Summary.NewFindings = len(response.Findings)
			response.Summary.FailOn = failOnFinding
			_ = json.NewEncoder(w).Encode(response)
		default:
			t.Errorf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// isolateHook isolates the user's configuration, signs in, and gives Claude
// Code a configuration directory to keep session transcripts in.
func isolateHook(t *testing.T) {
	t.Helper()
	isolateUserConfig(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("SLOPCOP_TOKEN", "test-token")
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
	command := &cobra.Command{}
	command.SetContext(context.Background())
	command.SetIn(strings.NewReader(payload))
	return runHook(command, serviceURL, "test")
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
	for _, args := range [][]string{{"hook", "codex"}, {"hook"}, {"hook", "claude", "--unknown"}} {
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

func TestBoundedHookText(t *testing.T) {
	got := boundedHookText(strings.Repeat("界", maxHookOutputRunes+1))
	if len([]rune(got)) != maxHookOutputRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded length = %d", len([]rune(got)))
	}
}
