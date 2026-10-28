package attribution

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
