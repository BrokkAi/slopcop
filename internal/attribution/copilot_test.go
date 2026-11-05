package attribution

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

const testCopilotSession = "0d6c5f0e-5a7b-4c1e-9f3d-2b8a1c4e6f70"

// installCopilotJournal writes a fixture into a Copilot CLI home as the
// journal name of testCopilotSession, with {{repo}} replaced by repo.
//
// copilot-events-synthesized.jsonl is not a captured session. It is
// synthesized from the session event schema the Copilot SDK publishes,
// github/copilot-sdk nodejs/src/generated/session-events.ts at
// 99d90edf504f824c77dcdf0faa978f4b35848a87 (Copilot CLI 1.0.106-1), since the
// CLI itself is not open source.
func installCopilotJournal(t *testing.T, home, repo, fixture, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "session-state", testCopilotSession, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(string(data), "{{repo}}", filepath.ToSlash(repo))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCopilotAttributesEachCommittedFileEditToItsModel(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	installCopilotJournal(t, home, repo, "copilot-events-synthesized.jsonl", "events.jsonl")

	got := detect(environment(map[string]string{
		"COPILOT_AGENT_SESSION_ID": testCopilotSession,
		"COPILOT_HOME":             home,
	}), repo)

	want := &service.Attribution{
		Harness:        "copilot-cli",
		HarnessVersion: "1.0.105",
		SessionID:      testCopilotSession,
		Models: []service.AttributedModel{
			// The subagent's own model and the effort it was configured
			// with.
			{Model: "claude-haiku-5-5", Effort: "medium", Source: SourceTranscript, Files: []string{"d.go"}},
			// The edit that failed committed nothing; the patch that
			// failed part way committed pkg/c.go, its model taken from
			// the call's start. The delete, the call with no file edits
			// and /elsewhere are not attributed.
			{Model: "claude-sonnet-5-5", Effort: "high", Source: SourceTranscript, Files: []string{"a.go", "pkg/c.go"}},
			{Model: "gpt-6-luna", Effort: "low", Source: SourceTranscript, Files: []string{"a.go"}},
			// The effort still set is bound to gpt-6-luna, so it is not
			// this model's.
			{Model: "gpt-6-sol", Source: SourceTranscript, Files: []string{"e.go"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
	}
}

func TestCopilotReadsTheJournalArchiveALinkNames(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	installCopilotJournal(t, home, repo, "copilot-events-synthesized.jsonl", "events.1.jsonl")
	head := `{"type":"session.journal_link","data":{"previousFile":"events.1.jsonl","sessionId":"S","version":1},"id":"h-1","timestamp":"2026-11-05T11:00:00.000Z","parentId":null}
{"type":"session.resume","data":{"resumeTime":"2026-11-05T11:00:00.000Z","eventCount":17,"reasoningEffort":"xhigh"},"id":"h-2","timestamp":"2026-11-05T11:00:00.000Z","parentId":"h-1"}
{"type":"tool.execution_complete","data":{"toolCallId":"call-10","success":true,"model":"gpt-6-sol","fileEdits":[{"kind":"create","path":"{{repo}}/f.go"}]},"id":"h-3","timestamp":"2026-11-05T11:00:01.000Z","parentId":"h-2"}
`
	path := filepath.Join(home, "session-state", testCopilotSession, "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(head, "{{repo}}", filepath.ToSlash(repo))), 0o600); err != nil {
		t.Fatal(err)
	}
	got := detect(environment(map[string]string{
		"COPILOT_AGENT_SESSION_ID": testCopilotSession,
		"COPILOT_HOME":             home,
	}), repo)
	if got == nil || got.HarnessVersion != "1.0.105" {
		t.Fatalf("attribution = %+v", got)
	}
	var sol []service.AttributedModel
	for _, model := range got.Models {
		if model.Model == "gpt-6-sol" {
			sol = append(sol, model)
		}
	}
	want := []service.AttributedModel{
		{Model: "gpt-6-sol", Source: SourceTranscript, Files: []string{"e.go"}},
		{Model: "gpt-6-sol", Effort: "xhigh", Source: SourceTranscript, Files: []string{"f.go"}},
	}
	if !reflect.DeepEqual(sol, want) {
		t.Fatalf("gpt-6-sol =\n%+v\nwant\n%+v", sol, want)
	}
}

func TestCopilotJournalLinksStayInTheSessionDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, previous := range []string{"../other/events.jsonl", "/etc/passwd", "..", "events.jsonl"} {
		line := `{"type":"session.journal_link","data":{"previousFile":"` + previous + `"}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := copilotJournals(dir); len(got) != 1 {
			t.Errorf("previousFile %q: journals = %v, want events.jsonl alone", previous, got)
		}
	}
}

func TestCopilotWithoutAJournalSendsTheSessionOnly(t *testing.T) {
	got := detect(environment(map[string]string{
		"COPILOT_AGENT_SESSION_ID": testCopilotSession,
		"COPILOT_HOME":             t.TempDir(),
	}), t.TempDir())
	want := &service.Attribution{Harness: "copilot-cli", SessionID: testCopilotSession}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution = %+v, want %+v", got, want)
	}
	for _, session := range []string{"", "../escape", "*", ".."} {
		if got := detect(environment(map[string]string{"COPILOT_AGENT_SESSION_ID": session}), t.TempDir()); got != nil {
			t.Errorf("session %q: attribution = %+v, want none", session, got)
		}
	}
}
