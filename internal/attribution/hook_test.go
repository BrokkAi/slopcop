package attribution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectHookUsesThePayloadSession(t *testing.T) {
	t.Run("Claude Code", func(t *testing.T) {
		configDir, repo := t.TempDir(), t.TempDir()
		installTranscript(t, configDir, repo, "claude-subagent.jsonl", testSession+".jsonl")
		t.Setenv("CLAUDE_CONFIG_DIR", configDir)
		t.Setenv("CLAUDE_CODE_SESSION_ID", "")
		t.Setenv("AI_AGENT", "claude-code_2-1-312_harness")

		got := DetectHook(ClaudeHarness, repo, testSession, "")
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

		got := DetectHook(CodexHarness, repo, testThread, "")
		if got == nil || got.Harness != "codex" || got.SessionID != testThread || len(got.Models) == 0 {
			t.Fatalf("attribution = %+v", got)
		}
	})
}

func TestDetectHookReadsThePayloadTranscript(t *testing.T) {
	t.Run("Claude Code", func(t *testing.T) {
		configDir, repo := t.TempDir(), t.TempDir()
		transcript := installTranscript(t, configDir, repo, "claude-subagent.jsonl", testSession+".jsonl")
		// The hook's own environment points elsewhere; the payload's path wins.
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		t.Setenv("CLAUDE_CODE_SESSION_ID", "")

		got := DetectHook(ClaudeHarness, repo, testSession, transcript)
		if got == nil || len(got.Models) != 1 || got.Models[0].Model != "claude-haiku-5-5" {
			t.Fatalf("attribution = %+v", got)
		}
	})

	t.Run("Codex", func(t *testing.T) {
		codexHome, repo := t.TempDir(), t.TempDir()
		rollout := installRollout(t, codexHome, repo)
		t.Setenv("CODEX_HOME", t.TempDir())
		t.Setenv("CODEX_THREAD_ID", "")

		got := DetectHook(CodexHarness, repo, testThread, rollout)
		if got == nil || len(got.Models) == 0 {
			t.Fatalf("attribution = %+v", got)
		}
	})
}

func TestDetectHookRejectsAnUnknownHarness(t *testing.T) {
	if got := DetectHook("other", t.TempDir(), testSession, ""); got != nil {
		t.Fatalf("attribution = %+v, want none", got)
	}
}

// writeRollout writes a Codex rollout for thread of session, in the day dir
// under codexHome, in which one turn of model wrote file in repo.
func writeRollout(t *testing.T, codexHome, day, session, thread, repo, file string) string {
	t.Helper()
	lines := []string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"session_id":%q,"id":%q}}`, session, thread),
		`{"type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-6-luna","effort":"high"}}`,
		fmt.Sprintf(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"turn-1","item":{"type":"FileChange","status":"completed","changes":{%q:{"type":"add"}}}}}`,
			filepath.Join(repo, file)),
	}
	path := filepath.Join(codexHome, "sessions", filepath.FromSlash(day), "rollout-2026-10-29T10-00-00-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexHookReadsTheSessionsSubagents(t *testing.T) {
	codexHome, repo := t.TempDir(), t.TempDir()
	const subagent = "01a182c8-0000-7000-8000-000000000002"
	root := writeRollout(t, codexHome, "2026/10/29", testThread, testThread, repo, "root.go")
	// A subagent past midnight, and a thread of another session.
	writeRollout(t, codexHome, "2026/10/30", testThread, subagent, repo, "sub.go")
	writeRollout(t, codexHome, "2026/10/29", "01a182c8-0000-7000-8000-000000000009", "01a182c8-0000-7000-8000-000000000003", repo, "other.go")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")

	got := DetectHook(CodexHarness, repo, testThread, root)
	var files []string
	for _, model := range got.Models {
		files = append(files, model.Files...)
	}
	if strings.Join(files, ",") != "root.go,sub.go" {
		t.Fatalf("files = %v, want root.go and sub.go", files)
	}

	// A command run in the subagent names its thread; its hook names the
	// session.
	t.Setenv("CODEX_HOME", codexHome)
	if got := HookSession(CodexHarness, subagent); got != testThread {
		t.Fatalf("HookSession = %q, want %q", got, testThread)
	}
	t.Setenv("CODEX_THREAD_ID", subagent)
	if got := HookSessions()[CodexHarness]; got != testThread {
		t.Fatalf("HookSessions = %q, want %q", got, testThread)
	}
}

func TestCodexHookNeedsNoHomeWithThePayloadRollout(t *testing.T) {
	codexHome, repo := t.TempDir(), t.TempDir()
	root := writeRollout(t, codexHome, "2026/10/29", testThread, testThread, repo, "root.go")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_THREAD_ID", "")

	got := DetectHook(CodexHarness, repo, testThread, root)
	if got == nil || len(got.Models) != 1 {
		t.Fatalf("attribution = %+v", got)
	}
}
