package attribution

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

// The Grok and Kimi fixtures are synthesized from each harness's source, as
// grok.go and kimi.go cite it, not captured from a live session.

const (
	testGrokSession = "019a5f2e-7c1d-7e4b-9a10-3f2b8c6d5e41"
	testKimiSession = "session_0b7f3c2a-5d1e-4f6a-9b8c-7d6e5f4a3b21"
)

// installFixture writes a fixture at path with {{repo}} and {{home}}
// replaced, and returns path.
func installFixture(t *testing.T, fixture, path, repo, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(string(data), "{{repo}}", filepath.ToSlash(repo))
	content = strings.ReplaceAll(content, "{{home}}", filepath.ToSlash(home))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// installGrokSession writes the Grok fixtures as testGrokSession's session
// under grokHome and returns its updates.jsonl.
func installGrokSession(t *testing.T, grokHome, repo string) string {
	t.Helper()
	dir := filepath.Join(grokHome, "sessions", "%2Fsome%2Fproject", testGrokSession)
	installFixture(t, "grok-chat_history.jsonl", filepath.Join(dir, "chat_history.jsonl"), repo, grokHome)
	installFixture(t, "grok-summary.json", filepath.Join(dir, "summary.json"), repo, grokHome)
	return installFixture(t, "grok-updates.jsonl", filepath.Join(dir, "updates.jsonl"), repo, grokHome)
}

func TestGrokAttributesEachCompletedEditToItsModel(t *testing.T) {
	grokHome, repo := t.TempDir(), t.TempDir()
	installGrokSession(t, grokHome, repo)

	got := detect(environment(map[string]string{
		"GROK_SESSION_ID": testGrokSession,
		"GROK_HOME":       grokHome,
	}), repo)

	want := &service.Attribution{
		Harness:   "grok",
		SessionID: testGrokSession,
		Models: []service.AttributedModel{
			// d.go's call is gone from chat_history.jsonl, as after a
			// compaction, so it falls back to its turn's model, with no
			// effort.
			{Model: "grok-4.3", Source: SourceTranscript, Files: []string{"pkg/d.go"}},
			// b.go's edit failed, f.go's never finished, c.go was only
			// read, and /elsewhere is outside the repository.
			{Model: "grok-code-fast-1", Effort: "high", Source: SourceTranscript, Files: []string{"a.go"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
	}
}

func TestGrokWithoutASessionSendsTheSessionOnly(t *testing.T) {
	got := detect(environment(map[string]string{
		"GROK_SESSION_ID": testGrokSession,
		"GROK_HOME":       t.TempDir(),
	}), t.TempDir())
	want := &service.Attribution{Harness: "grok", SessionID: testGrokSession}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution = %+v, want %+v", got, want)
	}
}

func TestDetectHookReadsGroksPayloadTranscript(t *testing.T) {
	grokHome, repo := t.TempDir(), t.TempDir()
	updates := installGrokSession(t, grokHome, repo)
	// The hook's own environment points elsewhere; the payload's path wins.
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GROK_SESSION_ID", "")

	got := DetectHook(GrokHarness, repo, testGrokSession, updates)
	if got == nil || got.Harness != "grok" || len(got.Models) != 2 {
		t.Fatalf("attribution = %+v", got)
	}
}

// installKimiSession writes the Kimi fixtures as testKimiSession's session
// under kimiHome.
func installKimiSession(t *testing.T, kimiHome, repo string) {
	t.Helper()
	dir := filepath.Join(kimiHome, "sessions", "wd_repo_0123456789ab", testKimiSession, "agents")
	installFixture(t, "kimi-wire-main.jsonl", filepath.Join(dir, "main", "wire.jsonl"), repo, kimiHome)
	installFixture(t, "kimi-wire-subagent.jsonl", filepath.Join(dir, "agent-0", "wire.jsonl"), repo, kimiHome)
	installFixture(t, "kimi-session_index.jsonl", filepath.Join(kimiHome, "session_index.jsonl"), repo, kimiHome)
}

func TestKimiHookAttributesEachSucceededEditToItsModel(t *testing.T) {
	kimiHome, repo := t.TempDir(), t.TempDir()
	installKimiSession(t, kimiHome, repo)
	t.Setenv("KIMI_CODE_HOME", kimiHome)

	got := DetectHook(KimiHarness, repo, testKimiSession, "")

	want := &service.Attribution{
		Harness:   "kimi-code",
		SessionID: testKimiSession,
		Models: []service.AttributedModel{
			// b.go's edit failed, f.go's never answered, c.go was only
			// read, and /elsewhere is outside the repository.
			{Model: "kimi-code/k3", Effort: "high", Source: SourceTranscript, Files: []string{"a.go"}},
			// The model switched before d.go was written.
			{Model: "kimi-code/kimi-for-coding", Effort: "off", Source: SourceTranscript, Files: []string{"pkg/d.go"}},
			// The subagent's own model wrote a.go too.
			{Model: "kimi-code/kimi-for-coding-highspeed", Effort: "low", Source: SourceTranscript, Files: []string{"a.go"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
	}
}

func TestKimiIsNotDetectedFromTheAgentsShell(t *testing.T) {
	kimiHome, repo := t.TempDir(), t.TempDir()
	installKimiSession(t, kimiHome, repo)
	// Kimi Code puts nothing in its agent's commands that names the session.
	got := detect(environment(map[string]string{"KIMI_CODE_HOME": kimiHome}), repo)
	if got != nil {
		t.Fatalf("attribution = %+v, want none", got)
	}
}
