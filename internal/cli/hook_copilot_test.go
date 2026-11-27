package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/attribution"
)

// writeCopilotEdits writes a Copilot CLI session under COPILOT_HOME in which
// the agent wrote files in repo, and returns its events.jsonl. The lines
// follow internal/attribution/testdata/copilot-events-1.0.114.jsonl, which
// was captured from Copilot CLI 1.0.114.
func writeCopilotEdits(t *testing.T, repo string, files ...string) string {
	t.Helper()
	lines := []string{fmt.Sprintf(`{"type":"session.start","data":{"sessionId":%q,"version":1,"producer":"copilot-agent","copilotVersion":"1.0.114"}}`, hookSession)}
	for i, file := range files {
		edit, _ := json.Marshal(map[string]any{"type": "tool.execution_complete", "data": map[string]any{
			"toolCallId": fmt.Sprintf("call_%d", i), "model": "gpt-6-luna", "turnId": fmt.Sprint(i), "success": true,
			"fileEdits": []any{map[string]any{"path": filepath.Join(repo, file), "kind": "create"}},
		}})
		lines = append(lines, string(edit))
	}
	path := filepath.Join(os.Getenv("COPILOT_HOME"), "session-state", hookSession, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// copilotStopPayload is the payload Copilot CLI 1.0.114 sent a hook
// registered under Claude Code's Stop event, captured live.
func copilotStopPayload(repo, transcript string, active bool) string {
	return fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"timestamp":"2026-11-27T08:59:52.054Z","cwd":%q,"transcript_path":%q,"stop_reason":"end_turn","stop_hook_active":%t}`,
		hookSession, repo, transcript, active)
}

func TestCopilotHookHoldsTheAgentForFindingsInItsFiles(t *testing.T) {
	isolateHook(t)
	t.Setenv("COPILOT_HOME", t.TempDir())
	t.Setenv("COPILOT_HOOK_DEPTH", "1")
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	transcript := writeCopilotEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	output := runHookAs(t, attribution.CopilotHarness, server.URL, copilotStopPayload(repo, transcript, false))
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix demo.go") {
		t.Fatalf("output = %+v", output)
	}
	output = runHookAs(t, attribution.CopilotHarness, server.URL, copilotStopPayload(repo, transcript, true))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "remain") || fake.checks != 1 {
		t.Fatalf("held output = %+v, checks = %d", output, fake.checks)
	}
}

func TestTheClaudeCodeHookDoesNothingUnderCopilot(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)
	t.Setenv("COPILOT_HOOK_DEPTH", "1")

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) || fake.checks != 0 {
		t.Fatalf("output = %+v, checks = %d", output, fake.checks)
	}
}

func TestTheGateRunsForCopilotAndOnlyOnceUnderIt(t *testing.T) {
	t.Setenv("GROK_HOOK_EVENT", "")
	t.Setenv("COPILOT_HOOK_DEPTH", "1")
	unchecked := armedRepo(t, false)
	// The payload Copilot CLI 1.0.114 sent a PreToolUse hook under Claude
	// Code's event name, captured live.
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "session_id": hookSession, "cwd": unchecked, "tool_name": "Bash",
		"tool_input": map[string]any{"command": "git push origin HEAD", "description": "Push", "mode": "sync"},
	})
	for hook, blocks := range map[string]bool{"copilot": true, "claude": false} {
		cmd := newGateCommand()
		cmd.SetIn(bytes.NewReader(payload))
		cmd.SetArgs([]string{"--hook", hook})
		var errOut bytes.Buffer
		cmd.SetErr(&errOut)
		err := cmd.Execute()
		if blocked := err != nil && strings.Contains(errOut.String(), "push blocked"); blocked != blocks {
			t.Errorf("%s gate: err %v, stderr %q, want blocked %t", hook, err, errOut.String(), blocks)
		}
	}
}
