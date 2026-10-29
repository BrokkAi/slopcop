package attribution

import "testing"

func TestDetectHookUsesThePayloadSession(t *testing.T) {
	t.Run("Claude Code", func(t *testing.T) {
		configDir, repo := t.TempDir(), t.TempDir()
		installTranscript(t, configDir, repo, "claude-subagent.jsonl", testSession+".jsonl")
		t.Setenv("CLAUDE_CONFIG_DIR", configDir)
		t.Setenv("CLAUDE_CODE_SESSION_ID", "")
		t.Setenv("AI_AGENT", "claude-code_2-1-312_harness")

		got := DetectHook(repo, "claude-code", testSession)
		if got == nil || got.Harness != "claude-code" || got.SessionID != testSession || got.HarnessVersion != "2.1.312" {
			t.Fatalf("attribution = %+v", got)
		}
		if len(got.Models) != 1 || got.Models[0].Model != "claude-haiku-5-5" {
			t.Fatalf("models = %+v", got.Models)
		}
	})

	t.Run("Codex", func(t *testing.T) {
		codexHome, repo := t.TempDir(), t.TempDir()
		installRollout(t, codexHome, repo)
		t.Setenv("CODEX_HOME", codexHome)
		t.Setenv("CODEX_THREAD_ID", "")

		got := DetectHook(repo, "codex", testThread)
		if got == nil || got.Harness != "codex" || got.SessionID != testThread {
			t.Fatalf("attribution = %+v", got)
		}
		if len(got.Models) == 0 {
			t.Fatal("hook attribution did not read the rollout")
		}
	})
}

func TestDetectHookRejectsAnUnknownHarness(t *testing.T) {
	if got := DetectHook(t.TempDir(), "other", testSession); got != nil {
		t.Fatalf("attribution = %+v, want none", got)
	}
}
