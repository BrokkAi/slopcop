package attribution

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BrokkAi/slopcop/internal/service"
)

const testSession = "ad904a2f-b013-4c97-8bdb-3ccd08e38b31"

// installTranscript writes a fixture into a Claude Code config directory as
// the transcript of testSession, with {{repo}} replaced by repo, and returns
// the transcript's path.
func installTranscript(t *testing.T, configDir, repo, fixture, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "projects", "-some-project", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(string(data), "{{repo}}", filepath.ToSlash(repo))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestClaudeCodeAttributesEachSucceededEditToItsModel(t *testing.T) {
	configDir, repo := t.TempDir(), t.TempDir()
	installTranscript(t, configDir, repo, "claude-session.jsonl", testSession+".jsonl")
	installTranscript(t, configDir, repo, "claude-subagent.jsonl", testSession+"/subagents/agent-a1.jsonl")

	got := detect(environment(map[string]string{
		"CLAUDE_CODE_SESSION_ID": testSession,
		"CLAUDE_CONFIG_DIR":      configDir,
		"AI_AGENT":               "claude-code_2-1-312_agent",
	}), repo)

	want := &service.Attribution{
		Harness:        "claude-code",
		HarnessVersion: "2.1.312",
		SessionID:      testSession,
		Models: []service.AttributedModel{
			// The subagent's own model wrote a.go too.
			{Model: "claude-haiku-5-5", Effort: "low", Source: SourceTranscript, Files: []string{"a.go"}},
			// b.go's edit failed and d.go's never answered, so neither is
			// attributed.
			{Model: "claude-opus-5-5", Effort: "high", Source: SourceTranscript, Files: []string{"n.ipynb", "pkg/c.go"}},
			// A Read names no author, and /elsewhere is outside the
			// repository.
			{Model: "claude-sonnet-5-5", Effort: "medium", Source: SourceTranscript, Files: []string{"a.go"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
	}
}

func TestClaudeCodeMatchesARepositoryReachedThroughASymlink(t *testing.T) {
	configDir, real := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	// The harness recorded the resolved path; the check runs from the link.
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	installTranscript(t, configDir, resolved, "claude-subagent.jsonl", testSession+".jsonl")

	got := detect(environment(map[string]string{
		"CLAUDE_CODE_SESSION_ID": testSession,
		"CLAUDE_CONFIG_DIR":      configDir,
	}), link)
	if got == nil || len(got.Models) != 1 || !reflect.DeepEqual(got.Models[0].Files, []string{"a.go"}) {
		t.Fatalf("attribution = %+v", got)
	}
}

func TestClaudeCodeWithoutATranscriptSendsTheSessionOnly(t *testing.T) {
	got := detect(environment(map[string]string{
		"CLAUDE_CODE_SESSION_ID": testSession,
		"CLAUDE_CONFIG_DIR":      t.TempDir(),
	}), t.TempDir())
	want := &service.Attribution{Harness: "claude-code", SessionID: testSession}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution = %+v, want %+v", got, want)
	}
}

func TestNoAttributionOutsideAnAgentSession(t *testing.T) {
	for _, session := range []string{"", "../escape", "*", "..", strings.Repeat("a", 129)} {
		got := detect(environment(map[string]string{
			"CLAUDE_CODE_SESSION_ID": session,
			"CLAUDE_CONFIG_DIR":      t.TempDir(),
		}), t.TempDir())
		if got != nil {
			t.Errorf("session %q: attribution = %+v, want none", session, got)
		}
	}
}

const testThread = "01a182c8-4697-7bb3-9bcb-d87ea6f2eb99"

// installRollout writes a fixture into a Codex home as the rollout of
// testThread, with {{repo}} replaced by repo.
func installRollout(t *testing.T, codexHome, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "codex-rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(codexHome, "sessions", "2026", "10", "28", "rollout-2026-10-28T14-52-05-"+testThread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(string(data), "{{repo}}", filepath.ToSlash(repo))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexAttributesEachCompletedFileChangeToItsTurnsModel(t *testing.T) {
	codexHome, repo := t.TempDir(), t.TempDir()
	installRollout(t, codexHome, repo)

	got := detect(environment(map[string]string{
		"CODEX_THREAD_ID": testThread,
		"CODEX_HOME":      codexHome,
		"CODEX_VERSION":   "0.165.0",
	}), repo)

	want := &service.Attribution{
		Harness:        "codex",
		HarnessVersion: "0.165.0",
		SessionID:      testThread,
		Models: []service.AttributedModel{
			// A move writes its destination. The delete, the failed change
			// and the change from a turn with no context are not
			// attributed.
			{Model: "gpt-6-luna", Effort: "high", Source: SourceTranscript, Files: []string{"a.go", "pkg/moved.go"}},
			// A turn from before collaboration modes keeps its effort on
			// the turn; /elsewhere is outside the repository.
			{Model: "gpt-6-sol", Effort: "low", Source: SourceTranscript, Files: []string{"a.go"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
	}
}

func TestNestedHarnessesPickTheSessionWrittenLast(t *testing.T) {
	configDir, codexHome, repo := t.TempDir(), t.TempDir(), t.TempDir()
	transcript := installTranscript(t, configDir, repo, "claude-subagent.jsonl", testSession+".jsonl")
	rollout := installRollout(t, codexHome, repo)
	env := environment(map[string]string{
		"CLAUDE_CODE_SESSION_ID": testSession,
		"CLAUDE_CONFIG_DIR":      configDir,
		"CODEX_THREAD_ID":        testThread,
		"CODEX_HOME":             codexHome,
	})
	older, newer := time.Now().Add(-time.Hour), time.Now()
	for _, tc := range []struct {
		stale, fresh string
		want         string
	}{
		{stale: rollout, fresh: transcript, want: "claude-code"},
		{stale: transcript, fresh: rollout, want: "codex"},
	} {
		if err := os.Chtimes(tc.stale, older, older); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(tc.fresh, newer, newer); err != nil {
			t.Fatal(err)
		}
		if got := detect(env, repo); got == nil || got.Harness != tc.want {
			t.Errorf("harness = %+v, want %s", got, tc.want)
		}
	}
}
