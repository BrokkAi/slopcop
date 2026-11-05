package attribution

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BrokkAi/slopcop/internal/service"
)

const testOpenCodeSession = "ses_0a1b2c3d4e5fRootSessionAAAA"

// installOpenCodeDatabase builds a database at path from the synthesized
// fixture, with {{repo}} replaced by repo, then runs extra statements.
func installOpenCodeDatabase(t *testing.T, path, repo string, extra ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "opencode-session.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range append([]string{strings.ReplaceAll(string(data), "{{repo}}", filepath.ToSlash(repo))}, extra...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenCodeAttributesEachCompletedEditToItsStepsModel(t *testing.T) {
	data, repo := t.TempDir(), t.TempDir()
	installOpenCodeDatabase(t, filepath.Join(data, "opencode", "opencode.db"), repo)

	got := detect(environment(map[string]string{
		"OPENCODE_SESSION_ID": testOpenCodeSession,
		"XDG_DATA_HOME":       data,
	}), repo)

	want := &service.Attribution{
		Harness:        "opencode",
		HarnessVersion: "2.2.5",
		SessionID:      testOpenCodeSession,
		Models: []service.AttributedModel{
			// The subagent's session is read too, with its own model.
			{Model: "claude-haiku-5-5", Source: SourceTranscript, Files: []string{"a.go"}},
			// b.go's edit failed, d.go's never finished and the read names
			// no author; /elsewhere is outside the repository and z.go is
			// another session's.
			{Model: "claude-sonnet-5-5", Effort: "high", Source: SourceTranscript, Files: []string{"a.go", "pkg/c.go"}},
			// A patch from part rows: a move writes its destination, a
			// delete writes nothing, and the failed patch is not
			// attributed. The default variant names no effort.
			{Model: "gpt-6-luna", Source: SourceTranscript, Files: []string{"e.go", "pkg/moved.go"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attribution =\n%+v\nwant\n%+v", got, want)
	}
}

func TestOpenCodeReadsADatabaseWithoutPartRows(t *testing.T) {
	data, repo := t.TempDir(), t.TempDir()
	// OpenCode 2.1.2 keeps every part inline and has no session_part table.
	installOpenCodeDatabase(t, filepath.Join(data, "elsewhere.db"), repo, "DROP TABLE session_part")

	got := detect(environment(map[string]string{
		"OPENCODE_SESSION_ID": testOpenCodeSession,
		"OPENCODE_DB":         filepath.Join(data, "elsewhere.db"),
	}), repo)
	if got == nil || len(got.Models) != 2 || got.Models[0].Model != "claude-haiku-5-5" || got.Models[1].Model != "claude-sonnet-5-5" {
		t.Fatalf("attribution = %+v", got)
	}
}

func TestOpenCodeFindsTheSessionInAChannelDatabase(t *testing.T) {
	data, repo := t.TempDir(), t.TempDir()
	installOpenCodeDatabase(t, filepath.Join(data, "opencode", "opencode.db"), repo, "DELETE FROM session_v2")
	installOpenCodeDatabase(t, filepath.Join(data, "opencode", "opencode-beta-2.db"), repo)

	got := detect(environment(map[string]string{
		"OPENCODE_SESSION_ID": testOpenCodeSession,
		"XDG_DATA_HOME":       data,
	}), repo)
	if got == nil || got.HarnessVersion != "2.2.5" || len(got.Models) != 3 {
		t.Fatalf("attribution = %+v", got)
	}
}

func TestOpenCodeWithoutTheSessionSendsTheSessionOnly(t *testing.T) {
	data := t.TempDir()
	installOpenCodeDatabase(t, filepath.Join(data, "opencode", "opencode.db"), t.TempDir())
	for _, env := range []map[string]string{
		{"OPENCODE_SESSION_ID": "ses_unknown", "XDG_DATA_HOME": data},
		{"OPENCODE_SESSION_ID": "ses_unknown", "XDG_DATA_HOME": t.TempDir()},
		{"OPENCODE_SESSION_ID": "ses_unknown", "OPENCODE_DB": ":memory:"},
	} {
		got := detect(environment(env), t.TempDir())
		want := &service.Attribution{Harness: "opencode", SessionID: "ses_unknown"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("env %v: attribution = %+v, want %+v", env, got, want)
		}
	}
}

func TestOpenCodeLeavesTheDatabaseUnchanged(t *testing.T) {
	data, repo := t.TempDir(), t.TempDir()
	path := filepath.Join(data, "opencode", "opencode.db")
	installOpenCodeDatabase(t, path, repo)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	detect(environment(map[string]string{"OPENCODE_SESSION_ID": testOpenCodeSession, "XDG_DATA_HOME": data}), repo)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("reading the session changed the database")
	}
	// A missing database is not created.
	missing := filepath.Join(t.TempDir(), "opencode.db")
	detect(environment(map[string]string{"OPENCODE_SESSION_ID": testOpenCodeSession, "OPENCODE_DB": missing}), repo)
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("stat missing database: %v", err)
	}
}
