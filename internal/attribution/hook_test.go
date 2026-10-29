package attribution

import "testing"

func TestDetectClaudeHookUsesThePayloadSession(t *testing.T) {
	configDir, repo := t.TempDir(), t.TempDir()
	installTranscript(t, configDir, repo, "claude-subagent.jsonl", testSession+".jsonl")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("AI_AGENT", "claude-code_2-1-312_harness")

	got := DetectClaudeHook(repo, testSession)
	if got == nil || got.Harness != "claude-code" || got.SessionID != testSession || got.HarnessVersion != "2.1.312" {
		t.Fatalf("attribution = %+v", got)
	}
	if len(got.Models) != 1 || got.Models[0].Model != "claude-haiku-5-5" {
		t.Fatalf("models = %+v", got.Models)
	}
}
