package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/attribution"
)

// The session records below follow each harness's source, as
// internal/attribution/grok.go and kimi.go cite it; none is a captured
// session.

// writeGrokEdits writes a Grok session under GROK_HOME in which the agent
// wrote files in repo, and returns its updates.jsonl.
func writeGrokEdits(t *testing.T, repo string, files ...string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("GROK_HOME"), "sessions", "%2Frepo", hookSession)
	var updates, calls []string
	chunk, _ := json.Marshal(map[string]any{"method": "session/update", "params": map[string]any{
		"sessionId": hookSession, "update": map[string]any{
			"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "go"},
			"_meta": map[string]any{"modelId": "grok-4.3"},
		},
	}})
	updates = append(updates, string(chunk))
	for i, file := range files {
		id := fmt.Sprintf("call_%d", i)
		calls = append(calls, fmt.Sprintf(`{"id":%q,"name":"write","arguments":"{}"}`, id))
		for _, update := range []map[string]any{
			{"sessionUpdate": "tool_call_update", "toolCallId": id, "kind": "edit", "locations": []any{map[string]any{"path": filepath.Join(repo, file)}}},
			{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "completed"},
		} {
			line, _ := json.Marshal(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": hookSession, "update": update}})
			updates = append(updates, string(line))
		}
	}
	history := fmt.Sprintf(`{"type":"assistant","content":"","tool_calls":[%s],"model_id":"grok-4.3","reasoning_effort":"high"}`, strings.Join(calls, ","))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(history+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "updates.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(updates, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGrokHookHoldsTheAgentForFindingsInItsFiles(t *testing.T) {
	isolateHook(t)
	t.Setenv("GROK_HOME", t.TempDir())
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	updates := writeGrokEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	// Grok sends its keys in camelCase and adds snake_case aliases for some.
	payload := func(active bool) string {
		return fmt.Sprintf(`{"hookEventName":"stop","hook_event_name":"Stop","sessionId":%[2]q,"session_id":%[2]q,"cwd":%[1]q,"workspaceRoot":%[1]q,"transcriptPath":%[3]q,"transcript_path":%[3]q,"reason":"end_turn","stopHookActive":%[4]t}`,
			repo, hookSession, updates, active)
	}
	output := runHookAs(t, attribution.GrokHarness, server.URL, payload(false))
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix demo.go") {
		t.Fatalf("output = %+v", output)
	}
	output = runHookAs(t, attribution.GrokHarness, server.URL, payload(true))
	if output.Decision != "" || !strings.Contains(output.SystemMessage, "remain") || fake.checks != 1 {
		t.Fatalf("held output = %+v, checks = %d", output, fake.checks)
	}
}

func TestTheClaudeCodeHookDoesNothingUnderGrok(t *testing.T) {
	isolateHook(t)
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeSessionEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)
	t.Setenv("GROK_HOOK_EVENT", "stop")

	if output := runTestHook(t, server.URL, stopPayload(repo, false)); output != (hookOutput{}) || fake.checks != 0 {
		t.Fatalf("output = %+v, checks = %d", output, fake.checks)
	}
}

// writeKimiEdits writes a Kimi Code session under KIMI_CODE_HOME in which
// the main agent wrote files in repo.
func writeKimiEdits(t *testing.T, repo string, files ...string) {
	t.Helper()
	home := os.Getenv("KIMI_CODE_HOME")
	lines := []string{`{"type":"profile.bind","agentId":"main","modelAlias":"kimi-code/k3","thinkingEffort":"high","systemPrompt":"","disallowedTools":[]}`}
	for i, file := range files {
		id := fmt.Sprintf("tool_%d", i)
		call, _ := json.Marshal(map[string]any{"type": "context.append_loop_event", "agentId": "main", "event": map[string]any{
			"type": "tool.call", "toolCallId": id, "name": "Write", "args": map[string]any{"path": file},
		}})
		result, _ := json.Marshal(map[string]any{"type": "context.append_loop_event", "agentId": "main", "event": map[string]any{
			"type": "tool.result", "toolCallId": id, "result": map[string]any{"output": "ok"},
		}})
		lines = append(lines, string(call), string(result))
	}
	wire := filepath.Join(home, "sessions", "wd_repo_0123456789ab", hookSession, "agents", "main", "wire.jsonl")
	if err := os.MkdirAll(filepath.Dir(wire), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wire, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, _ := json.Marshal(map[string]any{"sessionId": hookSession, "sessionDir": filepath.Dir(filepath.Dir(filepath.Dir(wire))), "workDir": repo})
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), append(index, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestKimiHookHoldsTheAgentForFindingsInItsFiles(t *testing.T) {
	isolateHook(t)
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	repo := newRepo(t, "https://github.com/BrokkAi/slopcop.git")
	writeFile(t, repo, "demo.go", "package demo\n")
	writeKimiEdits(t, repo, "demo.go")
	fake := &fakeService{active: []string{"BrokkAi/slopcop"}, findings: []string{"demo.go"}}
	server := fake.start(t, repo)

	payload := fmt.Sprintf(`{"hook_event_name":"Stop","session_id":%q,"session_title":"t","client_type":"kimi_code_cli","cwd":%q,"stop_hook_active":false}`, hookSession, repo)
	output := runHookAs(t, attribution.KimiHarness, server.URL, payload)
	if output.Decision != "block" || !strings.Contains(output.Reason, "Fix demo.go") {
		t.Fatalf("output = %+v", output)
	}
	answer, _ := json.Marshal(kimiAnswer(output))
	if !bytes.Contains(answer, []byte(`"permissionDecision":"deny"`)) || !bytes.Contains(answer, []byte("Fix demo.go")) {
		t.Fatalf("Kimi answer = %s", answer)
	}
}

func TestKimiHookAnswersInKimisShape(t *testing.T) {
	isolateHook(t)
	var stdout, stderr bytes.Buffer
	code := execute(context.Background(), []string{"hook", "kimi"}, "test", &stdout, &stderr)
	var answer map[string]any
	if code != 0 || json.Unmarshal(stdout.Bytes(), &answer) != nil || !strings.Contains(fmt.Sprint(answer["message"]), "could not read") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if got, _ := json.Marshal(kimiAnswer(hookOutput{})); string(got) != "{}" {
		t.Fatalf("empty answer = %s", got)
	}
}
